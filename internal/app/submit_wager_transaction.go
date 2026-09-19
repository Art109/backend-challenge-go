package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"backend-challenge-go/internal/domain/events"
	"backend-challenge-go/internal/domain/money"
	"backend-challenge-go/internal/domain/wagertransaction"
	"backend-challenge-go/internal/domain/wallet"
	"backend-challenge-go/internal/platform/metrics"
	"backend-challenge-go/internal/platform/postgres"
)

// maxOptimisticRetries bounds how many times SubmitWagerTransaction re-reads
// and retries a wallet mutation after losing a version-conflict race. In
// practice a handful of retries absorbs even fairly heavy contention on one
// wallet; hitting the cap means something is pathologically hot.
const maxOptimisticRetries = 20

type SubmitWagerTransactionCommand struct {
	ProviderID                     string
	ExternalTransactionID          string
	IdempotencyKey                 string
	PlayerID                       uuid.UUID
	WalletID                       uuid.UUID
	RoundID                        string
	GameID                         string
	Kind                           wagertransaction.Kind
	Money                          money.Money
	ReferenceExternalTransactionID string

	// InboxConsumerName/InboxMessageID are set only for SQS-originated
	// submissions. When present, the inbox row is recorded in the exact
	// same database transaction as the domain change and outbox events -
	// the spec requires these share one commit, not two related writes.
	InboxConsumerName string
	InboxMessageID    string
}

type SubmitWagerTransactionResult struct {
	TransactionID    uuid.UUID
	Status           wagertransaction.Status
	Balance          money.Money
	HasBalance       bool
	IdempotentReplay bool
	FailureCode      string
}

// SubmitWagerTransaction is the single entry point both the HTTP handler
// and the SQS consumer call - same use case, same idempotency guarantees,
// exactly as the spec requires ("HTTP e SQS devem compartilhar o caso de
// uso"). It resolves idempotency first (replay / conflict / genuinely new),
// then - for a genuinely new operation - retries the wallet mutation on an
// optimistic-concurrency conflict.
func (uc *UseCases) SubmitWagerTransaction(ctx context.Context, cmd SubmitWagerTransactionCommand) (SubmitWagerTransactionResult, error) {
	if err := validateCommand(cmd); err != nil {
		return SubmitWagerTransactionResult{}, err
	}

	payloadHash, err := canonicalPayloadHash(cmd)
	if err != nil {
		return SubmitWagerTransactionResult{}, err
	}

	if result, isReplay, err := uc.tryReplay(ctx, cmd, payloadHash); err != nil {
		return SubmitWagerTransactionResult{}, err
	} else if isReplay {
		recordResult(cmd, result)
		return result, nil
	}

	for attempt := 0; attempt < maxOptimisticRetries; attempt++ {
		result, err := uc.submitOnce(ctx, cmd, payloadHash)
		switch {
		case errors.Is(err, postgres.ErrVersionConflict):
			metrics.VersionConflictsTotal.Inc()
			continue // another writer committed first; re-read and retry
		case errors.Is(err, postgres.ErrIdempotencyConflict), errors.Is(err, postgres.ErrDuplicateMessage):
			// Either a concurrent identical submission won the race to
			// insert first (ErrIdempotencyConflict), or this exact SQS
			// message was already durably handled before
			// (ErrDuplicateMessage, inbox-level redelivery). Both cases
			// mean the real result already exists and committed -
			// resolve it as a replay instead of failing this call (this
			// is what makes "same bet sent 50 times in parallel", or a
			// redelivered message, converge on one debit).
			replay, isReplay, rErr := uc.tryReplay(ctx, cmd, payloadHash)
			if rErr != nil {
				return SubmitWagerTransactionResult{}, rErr
			}
			if isReplay {
				recordResult(cmd, replay)
				return replay, nil
			}
			return SubmitWagerTransactionResult{}, err
		default:
			if err == nil {
				recordResult(cmd, result)
			}
			return result, err
		}
	}
	return SubmitWagerTransactionResult{}, ErrTooManyConflicts
}

// recordResult covers the spec's "resultados por status" and "duplicatas"
// metrics, and the observability section's log line ("logs JSON com os
// identificadores disponíveis para rastrear a operação: ... transactionId,
// walletId e providerId"), in one place regardless of which of
// SubmitWagerTransaction's several return points produced the result - so
// every path (HTTP or SQS, new or replayed) gets exactly the same
// treatment.
func recordResult(cmd SubmitWagerTransactionCommand, result SubmitWagerTransactionResult) {
	metrics.TransactionsTotal.WithLabelValues(string(cmd.Kind), string(result.Status)).Inc()
	if result.IdempotentReplay {
		metrics.IdempotentReplaysTotal.Inc()
	}
	slog.Info("wager_transaction_result",
		"transactionId", result.TransactionID,
		"walletId", cmd.WalletID,
		"providerId", cmd.ProviderID,
		"kind", cmd.Kind,
		"status", result.Status,
		"idempotentReplay", result.IdempotentReplay,
		"failureCode", result.FailureCode,
	)
}

func validateCommand(cmd SubmitWagerTransactionCommand) error {
	if cmd.ProviderID == "" {
		return fmt.Errorf("app: providerId is required")
	}
	if cmd.ExternalTransactionID == "" {
		return fmt.Errorf("app: externalTransactionId is required")
	}
	if cmd.IdempotencyKey == "" {
		return fmt.Errorf("app: Idempotency-Key is required")
	}
	if cmd.PlayerID == uuid.Nil {
		return fmt.Errorf("app: playerId is required")
	}
	if cmd.WalletID == uuid.Nil {
		return fmt.Errorf("app: walletId is required")
	}
	return nil
}

// tryReplay implements the Idempotency-Key contract: same key + same
// content -> replay the original result; same key + different content ->
// conflict; this exact (providerId, externalTransactionId) already exists
// under a different key -> also a conflict (an operation may not be
// resubmitted under a new key). Returns isReplay=false, err=nil only when
// this is genuinely a brand-new operation.
//
// The two lookups below are separate round trips, not one atomic read: under
// heavy concurrency (many identical submissions racing each other) the row
// can be committed by the eventual winner in between them, so the first
// lookup (by key) misses while the second (by external id) hits. That is
// not actually a "reused under a different key" conflict - it is the same
// row, observed late - so the second lookup's result is only treated as a
// real conflict if its idempotency key genuinely differs from cmd's.
func (uc *UseCases) tryReplay(ctx context.Context, cmd SubmitWagerTransactionCommand, payloadHash string) (SubmitWagerTransactionResult, bool, error) {
	byKey, found, err := uc.txRepo.GetByProviderAndIdempotencyKey(ctx, uc.pool, cmd.ProviderID, cmd.IdempotencyKey)
	if err != nil {
		return SubmitWagerTransactionResult{}, false, err
	}
	if found {
		return resolveReplayOrConflict(byKey, payloadHash)
	}

	byExternal, found, err := uc.txRepo.GetByProviderAndExternalID(ctx, uc.pool, cmd.ProviderID, cmd.ExternalTransactionID)
	if err != nil {
		return SubmitWagerTransactionResult{}, false, err
	}
	if found {
		if byExternal.IdempotencyKey() == cmd.IdempotencyKey {
			return resolveReplayOrConflict(byExternal, payloadHash)
		}
		return SubmitWagerTransactionResult{}, false, ErrExternalTransactionKeyMismatch
	}
	return SubmitWagerTransactionResult{}, false, nil
}

func resolveReplayOrConflict(existing wagertransaction.WagerTransaction, payloadHash string) (SubmitWagerTransactionResult, bool, error) {
	if existing.PayloadHash() != payloadHash {
		return SubmitWagerTransactionResult{}, false, ErrIdempotencyKeyConflict
	}
	return replayResultFrom(existing), true, nil
}

func resultFrom(t wagertransaction.WagerTransaction) SubmitWagerTransactionResult {
	r := SubmitWagerTransactionResult{TransactionID: t.ID(), Status: t.Status(), FailureCode: t.FailureCode()}
	if balance, ok := t.ResultBalance(); ok {
		r.Balance = balance
		r.HasBalance = true
	}
	return r
}

func replayResultFrom(t wagertransaction.WagerTransaction) SubmitWagerTransactionResult {
	r := resultFrom(t)
	r.IdempotentReplay = true
	return r
}

// submitOnce runs exactly one attempt of a genuinely new operation, in its
// own database transaction. A postgres.ErrVersionConflict or
// postgres.ErrIdempotencyConflict bubbling out means nothing committed;
// the caller decides whether/how to retry.
func (uc *UseCases) submitOnce(ctx context.Context, cmd SubmitWagerTransactionCommand, payloadHash string) (SubmitWagerTransactionResult, error) {
	now := uc.now()
	txID := uuid.New()
	correlationID := uuid.New()

	dbTx, err := uc.pool.Begin(ctx)
	if err != nil {
		return SubmitWagerTransactionResult{}, fmt.Errorf("app: begin tx: %w", err)
	}
	defer dbTx.Rollback(ctx) //nolint:errcheck // no-op once committed

	if cmd.InboxMessageID != "" {
		// Recorded first, in this same transaction: if anything below
		// fails, this rolls back with it, so a genuinely failed attempt
		// never blocks a legitimate future redelivery from being recorded.
		if err := uc.inboxRepo.Record(ctx, dbTx, cmd.InboxConsumerName, cmd.InboxMessageID, payloadHash, now); err != nil {
			return SubmitWagerTransactionResult{}, err // postgres.ErrDuplicateMessage handled by the caller's retry loop
		}
	}

	base, err := wagertransaction.NewExternal(wagertransaction.NewExternalParams{
		ID: txID, ProviderID: cmd.ProviderID, ExternalTransactionID: cmd.ExternalTransactionID,
		IdempotencyKey: cmd.IdempotencyKey, PayloadHash: payloadHash,
		WalletID: cmd.WalletID, PlayerID: cmd.PlayerID, RoundID: cmd.RoundID, GameID: cmd.GameID,
		Kind: cmd.Kind, Money: cmd.Money, ReferenceExternalTransactionID: cmd.ReferenceExternalTransactionID, Now: now,
	})
	if err != nil {
		return SubmitWagerTransactionResult{}, err
	}

	switch cmd.Kind {
	case wagertransaction.KindBet:
		return uc.applyMovementAndFinish(ctx, dbTx, base, cmd.WalletID, cmd.PlayerID, cmd.Money,
			wallet.DirectionDebit, uuid.Nil, correlationID, wagertransaction.FailureCodeInsufficientBalance, now)
	case wagertransaction.KindWin:
		return uc.applyMovementAndFinish(ctx, dbTx, base, cmd.WalletID, cmd.PlayerID, cmd.Money,
			wallet.DirectionCredit, uuid.Nil, correlationID, "", now)
	case wagertransaction.KindLoss:
		return uc.submitLoss(ctx, dbTx, base, cmd, correlationID, now)
	case wagertransaction.KindRefund, wagertransaction.KindRollback:
		return uc.submitReversal(ctx, dbTx, base, cmd, correlationID, now)
	default:
		return SubmitWagerTransactionResult{}, fmt.Errorf("app: unsupported kind %q", cmd.Kind)
	}
}

func (uc *UseCases) submitLoss(ctx context.Context, dbTx pgx.Tx, base wagertransaction.WagerTransaction, cmd SubmitWagerTransactionCommand, correlationID uuid.UUID, now time.Time) (SubmitWagerTransactionResult, error) {
	w, err := uc.walletRepo.GetByID(ctx, dbTx, cmd.WalletID)
	if err != nil {
		if errors.Is(err, postgres.ErrWalletNotFound) {
			return SubmitWagerTransactionResult{}, ErrWalletNotFound
		}
		return SubmitWagerTransactionResult{}, err
	}
	if w.PlayerID() != cmd.PlayerID {
		return SubmitWagerTransactionResult{}, ErrWalletPlayerMismatch
	}

	// LOSS never touches the wallet: no ledger entry, no version bump,
	// no WalletBalanceChanged - only WagerTransactionProcessed.
	processed, err := base.MarkProcessed(uuid.Nil, w.Balance(), now)
	if err != nil {
		return SubmitWagerTransactionResult{}, err
	}
	if err := uc.txRepo.Insert(ctx, dbTx, processed); err != nil {
		return SubmitWagerTransactionResult{}, err
	}
	if err := uc.stageEvent(ctx, dbTx, processedEventPayload(processed), correlationID, nil, now); err != nil {
		return SubmitWagerTransactionResult{}, err
	}
	if err := dbTx.Commit(ctx); err != nil {
		return SubmitWagerTransactionResult{}, fmt.Errorf("app: commit: %w", err)
	}
	return resultFrom(processed), nil
}

// submitReversal handles REFUND and ROLLBACK: resolve the reference, decide
// whether to wait, reject or proceed, and - only once PROCESSED - apply the
// opposite-direction wallet movement.
func (uc *UseCases) submitReversal(ctx context.Context, dbTx pgx.Tx, base wagertransaction.WagerTransaction, cmd SubmitWagerTransactionCommand, correlationID uuid.UUID, now time.Time) (SubmitWagerTransactionResult, error) {
	ref, found, err := uc.txRepo.GetByProviderAndExternalID(ctx, dbTx, cmd.ProviderID, cmd.ReferenceExternalTransactionID)
	if err != nil {
		return SubmitWagerTransactionResult{}, err
	}

	if !found || ref.Status() == wagertransaction.StatusPending || ref.Status() == wagertransaction.StatusPendingReference {
		return uc.markPendingReferenceAndCommit(ctx, dbTx, base, correlationID, now)
	}
	if ref.Status() == wagertransaction.StatusRejected || ref.Status() == wagertransaction.StatusFailed {
		return uc.rejectAndCommit(ctx, dbTx, base, wagertransaction.FailureCodeReferenceNotProcessable, correlationID, now)
	}

	// ref.Status() == PROCESSED from here.
	if referenceMismatch(cmd, ref) {
		return uc.rejectAndCommit(ctx, dbTx, base, wagertransaction.FailureCodeReferenceMismatch, correlationID, now)
	}
	direction, ok := reversalDirectionFor(cmd.Kind, ref.Kind())
	if !ok {
		return uc.rejectAndCommit(ctx, dbTx, base, wagertransaction.FailureCodeReferenceMismatch, correlationID, now)
	}
	duplicate, err := uc.txRepo.ExistsProcessedReversal(ctx, dbTx, ref.ID(), cmd.Kind)
	if err != nil {
		return SubmitWagerTransactionResult{}, err
	}
	if duplicate {
		return uc.rejectAndCommit(ctx, dbTx, base, wagertransaction.FailureCodeDuplicateReversal, correlationID, now)
	}

	return uc.applyMovementAndFinish(ctx, dbTx, base, cmd.WalletID, cmd.PlayerID, cmd.Money,
		direction, ref.ID(), correlationID, wagertransaction.FailureCodeInsufficientBalanceForReversal, now)
}

// referenceMismatch checks that the reversal and the operation it
// references agree on provider, player, wallet, round, currency and
// amount, per the spec's "concordar em provedor, jogador, carteira, moeda
// e rodada" requirement, plus an exact amount match (partial reversals are
// out of scope).
func referenceMismatch(cmd SubmitWagerTransactionCommand, ref wagertransaction.WagerTransaction) bool {
	return ref.ProviderID() != cmd.ProviderID ||
		ref.PlayerID() != cmd.PlayerID ||
		ref.WalletID() != cmd.WalletID ||
		ref.RoundID() != cmd.RoundID ||
		!ref.Money().Equal(cmd.Money)
}

// reversalDirectionFor decides which way money moves for a reversal: a
// REFUND only ever reverses a BET (credit, undoing its debit); a ROLLBACK
// can reverse a BET (credit), or a WIN/REFUND (debit, undoing their
// credit). Any other combination isn't a valid reversal.
func reversalDirectionFor(reversalKind, referencedKind wagertransaction.Kind) (wallet.Direction, bool) {
	switch reversalKind {
	case wagertransaction.KindRefund:
		if referencedKind == wagertransaction.KindBet {
			return wallet.DirectionCredit, true
		}
	case wagertransaction.KindRollback:
		switch referencedKind {
		case wagertransaction.KindBet:
			return wallet.DirectionCredit, true
		case wagertransaction.KindWin, wagertransaction.KindRefund:
			return wallet.DirectionDebit, true
		}
	}
	return "", false
}

func (uc *UseCases) markPendingReferenceAndCommit(ctx context.Context, dbTx pgx.Tx, base wagertransaction.WagerTransaction, correlationID uuid.UUID, now time.Time) (SubmitWagerTransactionResult, error) {
	pending, err := base.MarkPendingReference(now)
	if err != nil {
		return SubmitWagerTransactionResult{}, err
	}
	if err := uc.txRepo.Insert(ctx, dbTx, pending); err != nil {
		return SubmitWagerTransactionResult{}, err
	}
	if err := uc.stageEvent(ctx, dbTx, events.WagerTransactionPendingReferenceData{
		TransactionID: pending.ID(), ExternalTransactionID: pending.ExternalTransactionID(), ProviderID: pending.ProviderID(),
		Kind: pending.Kind(), ReferenceExternalTransactionID: pending.ReferenceExternalTransactionID(),
		WalletID: pending.WalletID(), PlayerID: pending.PlayerID(),
	}, correlationID, nil, now); err != nil {
		return SubmitWagerTransactionResult{}, err
	}
	if err := dbTx.Commit(ctx); err != nil {
		return SubmitWagerTransactionResult{}, fmt.Errorf("app: commit: %w", err)
	}
	return resultFrom(pending), nil
}

func (uc *UseCases) rejectAndCommit(ctx context.Context, dbTx pgx.Tx, base wagertransaction.WagerTransaction, failureCode string, correlationID uuid.UUID, now time.Time) (SubmitWagerTransactionResult, error) {
	rejected, err := base.MarkRejected(failureCode, now)
	if err != nil {
		return SubmitWagerTransactionResult{}, err
	}
	if err := uc.txRepo.Insert(ctx, dbTx, rejected); err != nil {
		return SubmitWagerTransactionResult{}, err
	}
	if err := uc.stageEvent(ctx, dbTx, rejectedEventPayload(rejected), correlationID, nil, now); err != nil {
		return SubmitWagerTransactionResult{}, err
	}
	if err := dbTx.Commit(ctx); err != nil {
		return SubmitWagerTransactionResult{}, fmt.Errorf("app: commit: %w", err)
	}
	return resultFrom(rejected), nil
}

// applyMovementAndFinish is shared by BET/WIN (resolvedReferenceID =
// uuid.Nil) and REFUND/ROLLBACK (resolvedReferenceID = the resolved
// reference's internal id): read the wallet, apply the debit/credit,
// persist the ledger entry and the now-PROCESSED transaction, stage its
// events, commit. A version conflict here bubbles up unwrapped so
// SubmitWagerTransaction's retry loop can catch it specifically.
func (uc *UseCases) applyMovementAndFinish(
	ctx context.Context, dbTx pgx.Tx, base wagertransaction.WagerTransaction,
	walletID, playerID uuid.UUID, amount money.Money, direction wallet.Direction, resolvedReferenceID uuid.UUID,
	correlationID uuid.UUID, insufficientBalanceFailureCode string, now time.Time,
) (SubmitWagerTransactionResult, error) {
	w, err := uc.walletRepo.GetByID(ctx, dbTx, walletID)
	if err != nil {
		if errors.Is(err, postgres.ErrWalletNotFound) {
			return SubmitWagerTransactionResult{}, ErrWalletNotFound
		}
		return SubmitWagerTransactionResult{}, err
	}
	if w.PlayerID() != playerID {
		return SubmitWagerTransactionResult{}, ErrWalletPlayerMismatch
	}

	entryID := uuid.New()
	var updated wallet.Wallet
	var entry wallet.LedgerEntry
	if direction == wallet.DirectionDebit {
		updated, entry, err = w.Debit(entryID, base.ID(), amount, now)
	} else {
		updated, entry, err = w.Credit(entryID, base.ID(), amount, now)
	}
	if err != nil {
		if !errors.Is(err, wallet.ErrInsufficientBalance) {
			return SubmitWagerTransactionResult{}, err
		}
		return uc.rejectAndCommit(ctx, dbTx, base, insufficientBalanceFailureCode, correlationID, now)
	}

	if err := uc.walletRepo.UpdateBalance(ctx, dbTx, updated, w.Version()); err != nil {
		return SubmitWagerTransactionResult{}, err // postgres.ErrVersionConflict -> caller retries
	}

	// The wager_transactions row must exist before the ledger entry that
	// references it: wallet_ledger_entries.transaction_id has a (non
	// deferrable) FK, checked at INSERT time, not at commit time.
	processed, err := base.MarkProcessed(resolvedReferenceID, updated.Balance(), now)
	if err != nil {
		return SubmitWagerTransactionResult{}, err
	}
	if err := uc.txRepo.Insert(ctx, dbTx, processed); err != nil {
		return SubmitWagerTransactionResult{}, err
	}
	if err := uc.ledgerRepo.InsertEntry(ctx, dbTx, entry); err != nil {
		return SubmitWagerTransactionResult{}, err
	}

	if err := uc.stageEvent(ctx, dbTx, processedEventPayload(processed), correlationID, nil, now); err != nil {
		return SubmitWagerTransactionResult{}, err
	}
	if err := uc.stageEvent(ctx, dbTx, events.WalletBalanceChangedData{
		WalletID: walletID, TransactionID: processed.ID(), Direction: direction,
		Money: amount, BalanceBefore: entry.BalanceBefore(), BalanceAfter: entry.BalanceAfter(),
		WalletVersion: updated.Version(),
	}, correlationID, nil, now); err != nil {
		return SubmitWagerTransactionResult{}, err
	}

	if err := dbTx.Commit(ctx); err != nil {
		return SubmitWagerTransactionResult{}, fmt.Errorf("app: commit: %w", err)
	}
	return resultFrom(processed), nil
}

func processedEventPayload(t wagertransaction.WagerTransaction) events.Event {
	return events.WagerTransactionProcessedData{
		TransactionID: t.ID(), ExternalTransactionID: t.ExternalTransactionID(), ProviderID: t.ProviderID(),
		Kind: t.Kind(), Money: t.Money(), WalletID: t.WalletID(), PlayerID: t.PlayerID(),
	}
}

func rejectedEventPayload(t wagertransaction.WagerTransaction) events.Event {
	return events.WagerTransactionRejectedData{
		TransactionID: t.ID(), ExternalTransactionID: t.ExternalTransactionID(), ProviderID: t.ProviderID(),
		Kind: t.Kind(), FailureCode: t.FailureCode(), WalletID: t.WalletID(), PlayerID: t.PlayerID(),
	}
}
