package app

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"backend-challenge-go/internal/domain/wagertransaction"
	"backend-challenge-go/internal/domain/wallet"
	"backend-challenge-go/internal/platform/postgres"
)

// GetWallet reads a single wallet by id.
func (uc *UseCases) GetWallet(ctx context.Context, id uuid.UUID) (wallet.Wallet, error) {
	w, err := uc.walletRepo.GetByID(ctx, uc.pool, id)
	if err != nil {
		if errors.Is(err, postgres.ErrWalletNotFound) {
			return wallet.Wallet{}, ErrWalletNotFound
		}
		return wallet.Wallet{}, err
	}
	return w, nil
}

// GetWagerTransaction reads a single transaction by its internal id.
func (uc *UseCases) GetWagerTransaction(ctx context.Context, id uuid.UUID) (wagertransaction.WagerTransaction, error) {
	t, err := uc.txRepo.GetByID(ctx, uc.pool, id)
	if err != nil {
		if errors.Is(err, postgres.ErrWagerTransactionNotFound) {
			return wagertransaction.WagerTransaction{}, ErrWagerTransactionNotFound
		}
		return wagertransaction.WagerTransaction{}, err
	}
	return t, nil
}

// GetWagerTransactionByProviderAndExternalID looks a transaction up the way
// a provider naturally would: by their own external id, not our internal one.
func (uc *UseCases) GetWagerTransactionByProviderAndExternalID(ctx context.Context, providerID, externalTransactionID string) (wagertransaction.WagerTransaction, error) {
	t, found, err := uc.txRepo.GetByProviderAndExternalID(ctx, uc.pool, providerID, externalTransactionID)
	if err != nil {
		return wagertransaction.WagerTransaction{}, err
	}
	if !found {
		return wagertransaction.WagerTransaction{}, ErrWagerTransactionNotFound
	}
	return t, nil
}

// ListLedgerEntries returns one page of a wallet's ledger, oldest first.
// It 404s (via ErrWalletNotFound) up front if the wallet doesn't exist,
// rather than silently returning an empty page for a typo'd id.
func (uc *UseCases) ListLedgerEntries(ctx context.Context, walletID uuid.UUID, cursor *postgres.LedgerCursor, limit int) ([]wallet.LedgerEntry, bool, error) {
	if _, err := uc.GetWallet(ctx, walletID); err != nil {
		return nil, false, err
	}
	return uc.ledgerRepo.ListEntries(ctx, uc.pool, walletID, cursor, limit)
}

// DuePendingReferenceIDs lists transaction ids the reference-retry worker
// should re-evaluate right now.
func (uc *UseCases) DuePendingReferenceIDs(ctx context.Context, now time.Time, limit int) ([]uuid.UUID, error) {
	due, err := uc.txRepo.ListDuePendingReference(ctx, uc.pool, now, limit)
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, len(due))
	for i, t := range due {
		ids[i] = t.ID()
	}
	return ids, nil
}
