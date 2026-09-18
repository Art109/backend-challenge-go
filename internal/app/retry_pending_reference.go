package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"backend-challenge-go/internal/domain/events"
	"backend-challenge-go/internal/domain/wagertransaction"
	"backend-challenge-go/internal/domain/wallet"
)

// maxReferenceRetryAttempts bounds how many times the reference-retry
// worker will re-check a REFUND/ROLLBACK whose reference hasn't arrived
// yet, before giving up and rejecting it for a not-found reference. This
// is the "número máximo de tentativas" the spec requires be defined.
const maxReferenceRetryAttempts = 5

// referenceRetryBackoff is a simple doubling backoff (30s, 1m, 2m, 4m, 8m),
// capped at 10 minutes, so a burst of early-arriving reversals doesn't
// hammer the database while genuinely waiting on a slow provider.
func referenceRetryBackoff(attempt int) time.Duration {
	d := 30 * time.Second
	for i := 0; i < attempt; i++ {
		d *= 2
	}
	if d > 10*time.Minute {
		d = 10 * time.Minute
	}
	return d
}

// RetryPendingReference re-evaluates a single PENDING_REFERENCE transaction:
// called by the reference-retry worker for each row ListDuePendingReference
// returns. It resolves to PROCESSED/REJECTED exactly like the original
// submission would have, or - if still unresolved and under the attempt
// budget - reschedules itself with backoff. Safe to call redundantly (e.g.
// two worker instances picking the same row): if the row is no longer
// PENDING_REFERENCE by the time this runs, it's a no-op.
func (uc *UseCases) RetryPendingReference(ctx context.Context, transactionID uuid.UUID) error {
	now := uc.now()
	dbTx, err := uc.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("app: begin tx: %w", err)
	}
	defer dbTx.Rollback(ctx) //nolint:errcheck

	t, err := uc.txRepo.GetByID(ctx, dbTx, transactionID)
	if err != nil {
		return err
	}
	if t.Status() != wagertransaction.StatusPendingReference {
		return dbTx.Commit(ctx) // already resolved by someone else
	}

	correlationID := uuid.New()

	ref, found, err := uc.txRepo.GetByProviderAndExternalID(ctx, dbTx, t.ProviderID(), t.ReferenceExternalTransactionID())
	if err != nil {
		return err
	}

	switch {
	case found && ref.Status() == wagertransaction.StatusProcessed:
		return uc.resolveReadyReference(ctx, dbTx, t, ref, correlationID, now)

	case found && (ref.Status() == wagertransaction.StatusRejected || ref.Status() == wagertransaction.StatusFailed):
		return uc.rejectExistingAndCommit(ctx, dbTx, t, wagertransaction.FailureCodeReferenceNotProcessable, correlationID, now)

	default: // not found, or found but still PENDING/PENDING_REFERENCE itself
		if t.Attempts() >= maxReferenceRetryAttempts {
			return uc.rejectExistingAndCommit(ctx, dbTx, t, wagertransaction.FailureCodeReferenceNotFound, correlationID, now)
		}
		retried, err := t.IncrementAttempt(now.Add(referenceRetryBackoff(t.Attempts())), now)
		if err != nil {
			return err
		}
		if err := uc.txRepo.UpdateStatus(ctx, dbTx, retried, wagertransaction.StatusPendingReference); err != nil {
			return err
		}
		return dbTx.Commit(ctx)
	}
}

func (uc *UseCases) resolveReadyReference(ctx context.Context, dbTx pgx.Tx, t, ref wagertransaction.WagerTransaction, correlationID uuid.UUID, now time.Time) error {
	if referenceMismatchTx(t, ref) {
		return uc.rejectExistingAndCommit(ctx, dbTx, t, wagertransaction.FailureCodeReferenceMismatch, correlationID, now)
	}
	direction, ok := reversalDirectionFor(t.Kind(), ref.Kind())
	if !ok {
		return uc.rejectExistingAndCommit(ctx, dbTx, t, wagertransaction.FailureCodeReferenceMismatch, correlationID, now)
	}
	duplicate, err := uc.txRepo.ExistsProcessedReversal(ctx, dbTx, ref.ID(), t.Kind())
	if err != nil {
		return err
	}
	if duplicate {
		return uc.rejectExistingAndCommit(ctx, dbTx, t, wagertransaction.FailureCodeDuplicateReversal, correlationID, now)
	}
	return uc.applyMovementForExisting(ctx, dbTx, t, ref.ID(), direction, correlationID, now)
}

func referenceMismatchTx(t, ref wagertransaction.WagerTransaction) bool {
	return ref.ProviderID() != t.ProviderID() ||
		ref.PlayerID() != t.PlayerID() ||
		ref.WalletID() != t.WalletID() ||
		ref.RoundID() != t.RoundID() ||
		!ref.Money().Equal(t.Money())
}

// applyMovementForExisting mirrors applyMovementAndFinish, but for a
// transaction row that was already persisted as PENDING_REFERENCE - so it
// UPDATEs that row (with a status-CAS) instead of INSERTing a new one.
func (uc *UseCases) applyMovementForExisting(ctx context.Context, dbTx pgx.Tx, t wagertransaction.WagerTransaction, resolvedReferenceID uuid.UUID, direction wallet.Direction, correlationID uuid.UUID, now time.Time) error {
	w, err := uc.walletRepo.GetByID(ctx, dbTx, t.WalletID())
	if err != nil {
		return err
	}

	entryID := uuid.New()
	var updated wallet.Wallet
	var entry wallet.LedgerEntry
	if direction == wallet.DirectionDebit {
		updated, entry, err = w.Debit(entryID, t.ID(), t.Money(), now)
	} else {
		updated, entry, err = w.Credit(entryID, t.ID(), t.Money(), now)
	}
	if err != nil {
		if !errors.Is(err, wallet.ErrInsufficientBalance) {
			return err
		}
		return uc.rejectExistingAndCommit(ctx, dbTx, t, wagertransaction.FailureCodeInsufficientBalanceForReversal, correlationID, now)
	}

	if err := uc.walletRepo.UpdateBalance(ctx, dbTx, updated, w.Version()); err != nil {
		return err // ErrVersionConflict: nothing committed, the worker's next tick retries this row
	}

	processed, err := t.MarkProcessed(resolvedReferenceID, updated.Balance(), now)
	if err != nil {
		return err
	}
	if err := uc.txRepo.UpdateStatus(ctx, dbTx, processed, wagertransaction.StatusPendingReference); err != nil {
		return err
	}
	if err := uc.ledgerRepo.InsertEntry(ctx, dbTx, entry); err != nil {
		return err
	}

	if err := uc.stageEvent(ctx, dbTx, processedEventPayload(processed), correlationID, nil, now); err != nil {
		return err
	}
	if err := uc.stageEvent(ctx, dbTx, events.WalletBalanceChangedData{
		WalletID: t.WalletID(), TransactionID: processed.ID(), Direction: direction,
		Money: t.Money(), BalanceBefore: entry.BalanceBefore(), BalanceAfter: entry.BalanceAfter(),
		WalletVersion: updated.Version(),
	}, correlationID, nil, now); err != nil {
		return err
	}

	if err := dbTx.Commit(ctx); err != nil {
		return fmt.Errorf("app: commit: %w", err)
	}
	return nil
}

func (uc *UseCases) rejectExistingAndCommit(ctx context.Context, dbTx pgx.Tx, t wagertransaction.WagerTransaction, failureCode string, correlationID uuid.UUID, now time.Time) error {
	rejected, err := t.MarkRejected(failureCode, now)
	if err != nil {
		return err
	}
	if err := uc.txRepo.UpdateStatus(ctx, dbTx, rejected, wagertransaction.StatusPendingReference); err != nil {
		return err
	}
	if err := uc.stageEvent(ctx, dbTx, rejectedEventPayload(rejected), correlationID, nil, now); err != nil {
		return err
	}
	if err := dbTx.Commit(ctx); err != nil {
		return fmt.Errorf("app: commit: %w", err)
	}
	return nil
}
