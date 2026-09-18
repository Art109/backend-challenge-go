package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"backend-challenge-go/internal/domain/events"
	"backend-challenge-go/internal/domain/money"
	"backend-challenge-go/internal/domain/wagertransaction"
	"backend-challenge-go/internal/domain/wallet"
	"backend-challenge-go/internal/platform/postgres"
)

type OpenWalletCommand struct {
	PlayerID       uuid.UUID
	InitialBalance money.Money
}

type OpenWalletResult struct {
	Wallet wallet.Wallet
}

// OpenWallet creates a wallet, and - only when initialBalance is positive -
// an OPENING transaction, its ledger entry, and the WagerTransactionProcessed
// / WalletBalanceChanged events, all in the same commit. A zero initial
// balance creates only the wallet: no OPENING row, no ledger entry, no
// events, exactly as the spec requires.
func (uc *UseCases) OpenWallet(ctx context.Context, cmd OpenWalletCommand) (OpenWalletResult, error) {
	now := uc.now()
	walletID := uuid.New()

	w, err := wallet.Open(walletID, cmd.PlayerID, cmd.InitialBalance, now)
	if err != nil {
		return OpenWalletResult{}, err
	}

	dbTx, err := uc.pool.Begin(ctx)
	if err != nil {
		return OpenWalletResult{}, fmt.Errorf("app: begin tx: %w", err)
	}
	defer dbTx.Rollback(ctx) //nolint:errcheck // no-op once committed

	if err := uc.walletRepo.Insert(ctx, dbTx, w); err != nil {
		if errors.Is(err, postgres.ErrWalletAlreadyExists) {
			return OpenWalletResult{}, ErrWalletConflict
		}
		return OpenWalletResult{}, fmt.Errorf("app: insert wallet: %w", err)
	}

	if cmd.InitialBalance.IsPositive() {
		if err := uc.recordOpeningCredit(ctx, dbTx, w, now); err != nil {
			return OpenWalletResult{}, err
		}
	}

	if err := dbTx.Commit(ctx); err != nil {
		return OpenWalletResult{}, fmt.Errorf("app: commit: %w", err)
	}
	return OpenWalletResult{Wallet: w}, nil
}

// recordOpeningCredit inserts the OPENING transaction, its ledger entry,
// and the WagerTransactionProcessed / WalletBalanceChanged events for a
// wallet that was just opened with a positive balance.
func (uc *UseCases) recordOpeningCredit(ctx context.Context, dbTx postgres.Querier, w wallet.Wallet, now time.Time) error {
	openingID := uuid.New()
	openingTx, err := wagertransaction.NewOpening(openingID, w.ID(), w.PlayerID(), w.Balance(), now)
	if err != nil {
		return fmt.Errorf("app: build opening transaction: %w", err)
	}
	if err := uc.txRepo.Insert(ctx, dbTx, openingTx); err != nil {
		return fmt.Errorf("app: insert opening transaction: %w", err)
	}

	entryID := uuid.New()
	entry, err := wallet.NewLedgerEntry(entryID, w.ID(), openingID, wallet.DirectionCredit, w.Balance(), money.Zero(w.Currency()), w.Balance(), now)
	if err != nil {
		return fmt.Errorf("app: build opening ledger entry: %w", err)
	}
	if err := uc.ledgerRepo.InsertEntry(ctx, dbTx, entry); err != nil {
		return fmt.Errorf("app: insert opening ledger entry: %w", err)
	}

	correlationID := uuid.New()
	if err := uc.stageEvent(ctx, dbTx, events.WagerTransactionProcessedData{
		TransactionID: openingID, Kind: wagertransaction.KindOpening, Money: w.Balance(),
		WalletID: w.ID(), PlayerID: w.PlayerID(),
	}, correlationID, nil, now); err != nil {
		return err
	}
	if err := uc.stageEvent(ctx, dbTx, events.WalletBalanceChangedData{
		WalletID: w.ID(), TransactionID: openingID, Direction: wallet.DirectionCredit,
		Money: w.Balance(), BalanceBefore: entry.BalanceBefore(), BalanceAfter: entry.BalanceAfter(),
		WalletVersion: w.Version(),
	}, correlationID, nil, now); err != nil {
		return err
	}
	return nil
}
