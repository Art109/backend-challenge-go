package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"backend-challenge-go/internal/domain/money"
	"backend-challenge-go/internal/domain/wallet"
)

// ErrVersionConflict is returned by UpdateBalance when the row's version in
// the database no longer matches what the caller last read - another
// writer committed first. Callers must re-read the wallet and retry the
// whole operation; this is the concrete mechanism behind the optimistic
// concurrency strategy documented on wallet.Wallet.
var ErrVersionConflict = errors.New("postgres: wallet version conflict")

// ErrWalletNotFound is returned when a lookup finds no matching row.
var ErrWalletNotFound = errors.New("postgres: wallet not found")

// ErrWalletAlreadyExists is returned by Insert when (player_id, currency)
// already has a wallet - the HTTP layer maps this to a 409 Conflict.
var ErrWalletAlreadyExists = errors.New("postgres: wallet already exists for this player and currency")

type WalletRepository struct{}

func NewWalletRepository() *WalletRepository {
	return &WalletRepository{}
}

// Insert persists a brand-new wallet (version 1). Call within the same
// transaction as the OPENING transaction row and its ledger entry, if any,
// so they all commit together.
func (r *WalletRepository) Insert(ctx context.Context, q Querier, w wallet.Wallet) error {
	_, err := q.Exec(ctx, `
		INSERT INTO wallets (id, player_id, currency, balance_minor_units, version, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, w.ID(), w.PlayerID(), w.Currency(), w.Balance().MinorUnits(), w.Version(), w.CreatedAt(), w.UpdatedAt())
	if err != nil {
		if isUniqueViolation(err) {
			return ErrWalletAlreadyExists
		}
		return fmt.Errorf("postgres: insert wallet: %w", err)
	}
	return nil
}

// GetByID reads a wallet by its primary key.
func (r *WalletRepository) GetByID(ctx context.Context, q Querier, id uuid.UUID) (wallet.Wallet, error) {
	row := q.QueryRow(ctx, `
		SELECT id, player_id, currency, balance_minor_units, version, created_at, updated_at
		FROM wallets WHERE id = $1
	`, id)
	return scanWallet(row)
}

// GetByPlayerAndCurrency reads a wallet by its natural key. The second
// return value is false (with a zero Wallet and nil error) when no wallet
// exists yet - this is the expected, non-error case for "has this player
// opened a wallet in this currency."
func (r *WalletRepository) GetByPlayerAndCurrency(ctx context.Context, q Querier, playerID uuid.UUID, currency string) (wallet.Wallet, bool, error) {
	row := q.QueryRow(ctx, `
		SELECT id, player_id, currency, balance_minor_units, version, created_at, updated_at
		FROM wallets WHERE player_id = $1 AND currency = $2
	`, playerID, currency)
	w, err := scanWallet(row)
	if errors.Is(err, ErrWalletNotFound) {
		return wallet.Wallet{}, false, nil
	}
	if err != nil {
		return wallet.Wallet{}, false, err
	}
	return w, true, nil
}

// GetByIDForUpdate is like GetByID but takes a row-level lock, for callers
// that will immediately recompute and write a new balance in the same
// transaction. Combined with the version check in UpdateBalance this is
// belt-and-suspenders: the lock avoids two writers doing redundant wasted
// work, the version check is what actually guarantees correctness even if
// the lock were somehow bypassed.
func (r *WalletRepository) GetByIDForUpdate(ctx context.Context, tx pgx.Tx, id uuid.UUID) (wallet.Wallet, error) {
	row := tx.QueryRow(ctx, `
		SELECT id, player_id, currency, balance_minor_units, version, created_at, updated_at
		FROM wallets WHERE id = $1 FOR UPDATE
	`, id)
	return scanWallet(row)
}

// UpdateBalance persists w's new balance/version with an optimistic
// concurrency check: the UPDATE only matches the row if its version is
// still expectedVersion (which must be w.Version()-1, i.e. what the caller
// read before applying Debit/Credit). Zero rows affected means someone
// else won the race first - the caller must re-read and retry.
func (r *WalletRepository) UpdateBalance(ctx context.Context, q Querier, w wallet.Wallet, expectedVersion int64) error {
	tag, err := q.Exec(ctx, `
		UPDATE wallets
		SET balance_minor_units = $1, version = $2, updated_at = $3
		WHERE id = $4 AND version = $5
	`, w.Balance().MinorUnits(), w.Version(), w.UpdatedAt(), w.ID(), expectedVersion)
	if err != nil {
		return fmt.Errorf("postgres: update wallet balance: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrVersionConflict
	}
	return nil
}

func scanWallet(row pgx.Row) (wallet.Wallet, error) {
	var (
		id, playerID      uuid.UUID
		currency          string
		balanceMinorUnits int64
		version           int64
		createdAt         time.Time
		updatedAt         time.Time
	)
	if err := row.Scan(&id, &playerID, &currency, &balanceMinorUnits, &version, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return wallet.Wallet{}, ErrWalletNotFound
		}
		return wallet.Wallet{}, fmt.Errorf("postgres: scan wallet: %w", err)
	}
	balance, err := money.New(balanceMinorUnits, currency)
	if err != nil {
		return wallet.Wallet{}, fmt.Errorf("postgres: rehydrate wallet balance: %w", err)
	}
	w, err := wallet.Rehydrate(id, playerID, balance, version, createdAt, updatedAt)
	if err != nil {
		return wallet.Wallet{}, fmt.Errorf("postgres: rehydrate wallet: %w", err)
	}
	return w, nil
}
