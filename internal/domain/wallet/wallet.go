// Package wallet implements the Wallet aggregate: a player's balance in a
// single currency, together with the append-only ledger entries that
// justify every change to that balance.
package wallet

import (
	"errors"
	"time"

	"github.com/google/uuid"

	"backend-challenge-go/internal/domain/money"
)

var (
	ErrInvalidWalletID      = errors.New("wallet: invalid wallet id")
	ErrInvalidPlayerID      = errors.New("wallet: invalid player id")
	ErrInvalidTransactionID = errors.New("wallet: invalid transaction id")
	ErrNegativeBalance      = errors.New("wallet: balance must not be negative")
	ErrInvalidVersion       = errors.New("wallet: version must be >= 1")
	ErrCurrencyMismatch     = errors.New("wallet: movement currency does not match wallet currency")
	ErrNonPositiveAmount    = errors.New("wallet: amount must be positive")
	ErrInsufficientBalance  = errors.New("wallet: insufficient balance")
)

// Wallet is the aggregate root for a player's balance in one currency. The
// pair (playerID, currency) identifies a single wallet; that uniqueness is
// enforced by the persistence layer's schema, not here.
//
// Concurrency strategy: Version implements optimistic concurrency control.
// Every balance-changing operation increments it by exactly one. The
// repository persists a Wallet with `UPDATE ... WHERE id = $1 AND version =
// $2`; zero rows affected means another writer won the race, and the
// caller must re-read and retry. This keeps coordination scoped to a
// single wallet row - no global lock ever spans multiple wallets, so
// unrelated wallets always proceed in parallel.
type Wallet struct {
	id        uuid.UUID
	playerID  uuid.UUID
	balance   money.Money
	version   int64
	createdAt time.Time
	updatedAt time.Time
}

// Open creates a brand-new wallet. initialBalance may be zero (allowed by
// the spec) but never negative; its currency becomes the wallet's
// currency. The freshly created wallet always starts at version 1.
//
// Open does not decide whether an OPENING transaction/ledger entry/events
// should be produced for a positive initial balance - that policy belongs
// to the application layer, which owns transaction orchestration.
func Open(id, playerID uuid.UUID, initialBalance money.Money, now time.Time) (Wallet, error) {
	if id == uuid.Nil {
		return Wallet{}, ErrInvalidWalletID
	}
	if playerID == uuid.Nil {
		return Wallet{}, ErrInvalidPlayerID
	}
	if initialBalance.IsNegative() {
		return Wallet{}, ErrNegativeBalance
	}
	return Wallet{
		id:        id,
		playerID:  playerID,
		balance:   initialBalance,
		version:   1,
		createdAt: now,
		updatedAt: now,
	}, nil
}

// Rehydrate reconstructs a Wallet exactly as persisted, without reapplying
// any movement, transition or event emission - it is a pure mapping from
// stored state back to the aggregate.
func Rehydrate(id, playerID uuid.UUID, balance money.Money, version int64, createdAt, updatedAt time.Time) (Wallet, error) {
	if id == uuid.Nil {
		return Wallet{}, ErrInvalidWalletID
	}
	if playerID == uuid.Nil {
		return Wallet{}, ErrInvalidPlayerID
	}
	if balance.IsNegative() {
		return Wallet{}, ErrNegativeBalance
	}
	if version < 1 {
		return Wallet{}, ErrInvalidVersion
	}
	return Wallet{
		id:        id,
		playerID:  playerID,
		balance:   balance,
		version:   version,
		createdAt: createdAt,
		updatedAt: updatedAt,
	}, nil
}

// Debit subtracts amount from the wallet balance for the given
// transactionID, producing the matching ledger entry. It fails (leaving
// the receiver's caller-visible copy untouched, since Wallet is an
// immutable value) if the movement currency doesn't match, the amount
// isn't strictly positive, or the resulting balance would go negative.
func (w Wallet) Debit(entryID, transactionID uuid.UUID, amount money.Money, now time.Time) (Wallet, LedgerEntry, error) {
	if err := w.validateMovement(amount); err != nil {
		return Wallet{}, LedgerEntry{}, err
	}
	newBalance, err := w.balance.Sub(amount)
	if err != nil {
		return Wallet{}, LedgerEntry{}, err
	}
	if newBalance.IsNegative() {
		return Wallet{}, LedgerEntry{}, ErrInsufficientBalance
	}
	return w.applyMovement(entryID, transactionID, DirectionDebit, amount, newBalance, now)
}

// Credit adds amount to the wallet balance for the given transactionID,
// producing the matching ledger entry.
func (w Wallet) Credit(entryID, transactionID uuid.UUID, amount money.Money, now time.Time) (Wallet, LedgerEntry, error) {
	if err := w.validateMovement(amount); err != nil {
		return Wallet{}, LedgerEntry{}, err
	}
	newBalance, err := w.balance.Add(amount)
	if err != nil {
		return Wallet{}, LedgerEntry{}, err
	}
	return w.applyMovement(entryID, transactionID, DirectionCredit, amount, newBalance, now)
}

func (w Wallet) validateMovement(amount money.Money) error {
	if !amount.SameCurrency(w.balance) {
		return ErrCurrencyMismatch
	}
	if !amount.IsPositive() {
		return ErrNonPositiveAmount
	}
	return nil
}

func (w Wallet) applyMovement(entryID, transactionID uuid.UUID, direction Direction, amount, newBalance money.Money, now time.Time) (Wallet, LedgerEntry, error) {
	entry, err := NewLedgerEntry(entryID, w.id, transactionID, direction, amount, w.balance, newBalance, now)
	if err != nil {
		return Wallet{}, LedgerEntry{}, err
	}
	updated := w
	updated.balance = newBalance
	updated.version = w.version + 1
	updated.updatedAt = now
	return updated, entry, nil
}

func (w Wallet) ID() uuid.UUID        { return w.id }
func (w Wallet) PlayerID() uuid.UUID  { return w.playerID }
func (w Wallet) Currency() string     { return w.balance.Currency() }
func (w Wallet) Balance() money.Money { return w.balance }
func (w Wallet) Version() int64       { return w.version }
func (w Wallet) CreatedAt() time.Time { return w.createdAt }
func (w Wallet) UpdatedAt() time.Time { return w.updatedAt }

// HasSufficientBalanceFor reports whether debiting amount would keep the
// balance at or above zero, without performing the debit.
func (w Wallet) HasSufficientBalanceFor(amount money.Money) (bool, error) {
	remaining, err := w.balance.Sub(amount)
	if err != nil {
		return false, err
	}
	return !remaining.IsNegative(), nil
}
