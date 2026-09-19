package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"backend-challenge-go/internal/domain/money"
	"backend-challenge-go/internal/platform/metrics"
	"backend-challenge-go/internal/platform/postgres"
)

type ReconciliationResult struct {
	WalletID          uuid.UUID
	StoredBalance     money.Money
	CalculatedBalance money.Money
	Difference        money.Money
	Consistent        bool
	CheckedEntries    int
}

// Reconcile reconstructs a wallet's balance purely from its ledger (never
// alters anything - a read-only check) and compares it against the stored
// balance. Both reads happen inside one REPEATABLE READ transaction so they
// see the exact same snapshot even if other operations commit against this
// wallet concurrently - without that, a concurrent debit landing between
// the two reads could manufacture a phantom "inconsistency" that was never
// really there at any single instant.
func (uc *UseCases) Reconcile(ctx context.Context, walletID uuid.UUID) (ReconciliationResult, error) {
	tx, err := uc.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return ReconciliationResult{}, fmt.Errorf("app: begin reconciliation tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	w, err := uc.walletRepo.GetByID(ctx, tx, walletID)
	if err != nil {
		if errors.Is(err, postgres.ErrWalletNotFound) {
			return ReconciliationResult{}, ErrWalletNotFound
		}
		return ReconciliationResult{}, err
	}

	calculated, count, err := uc.ledgerRepo.SumEntries(ctx, tx, walletID, w.Currency())
	if err != nil {
		return ReconciliationResult{}, err
	}

	// difference = stored - calculated, per the spec's definition.
	diff, err := w.Balance().Sub(calculated)
	if err != nil {
		return ReconciliationResult{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return ReconciliationResult{}, fmt.Errorf("app: commit reconciliation tx: %w", err)
	}

	if !diff.IsZero() {
		metrics.ReconciliationDivergencesTotal.Inc()
	}

	return ReconciliationResult{
		WalletID:          walletID,
		StoredBalance:     w.Balance(),
		CalculatedBalance: calculated,
		Difference:        diff,
		Consistent:        diff.IsZero(),
		CheckedEntries:    count,
	}, nil
}
