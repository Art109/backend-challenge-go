package wallet

import (
	"errors"
	"time"

	"github.com/google/uuid"

	"backend-challenge-go/internal/domain/money"
)

// Direction is the side of a ledger movement.
type Direction string

const (
	DirectionDebit  Direction = "DEBIT"
	DirectionCredit Direction = "CREDIT"
)

var (
	ErrInvalidLedgerEntryID = errors.New("wallet: invalid ledger entry id")
	ErrInvalidDirection     = errors.New("wallet: invalid ledger direction")
	ErrBalanceMismatch      = errors.New("wallet: balanceAfter does not equal balanceBefore +/- amount")
)

// LedgerEntry is a single, immutable append-only movement of a Wallet.
// Once constructed it is never edited: a correction is a new entry, never a
// mutation of an existing one (enforced here in-memory, and by a DB
// constraint once persisted).
type LedgerEntry struct {
	id            uuid.UUID
	walletID      uuid.UUID
	transactionID uuid.UUID
	direction     Direction
	amount        money.Money
	balanceBefore money.Money
	balanceAfter  money.Money
	createdAt     time.Time
}

// NewLedgerEntry validates and builds a LedgerEntry. It is used both when a
// Wallet records a fresh movement and when rehydrating a persisted entry:
// in both cases the same invariant (balanceAfter = balanceBefore +/- amount)
// must hold, and there is no extra side effect (no event emission, no
// further movement) to avoid on rehydration.
func NewLedgerEntry(
	id, walletID, transactionID uuid.UUID,
	direction Direction,
	amount, balanceBefore, balanceAfter money.Money,
	createdAt time.Time,
) (LedgerEntry, error) {
	if id == uuid.Nil {
		return LedgerEntry{}, ErrInvalidLedgerEntryID
	}
	if walletID == uuid.Nil {
		return LedgerEntry{}, ErrInvalidWalletID
	}
	if transactionID == uuid.Nil {
		return LedgerEntry{}, ErrInvalidTransactionID
	}
	if direction != DirectionDebit && direction != DirectionCredit {
		return LedgerEntry{}, ErrInvalidDirection
	}
	if !amount.IsPositive() {
		return LedgerEntry{}, ErrNonPositiveAmount
	}
	if !amount.SameCurrency(balanceBefore) || !balanceBefore.SameCurrency(balanceAfter) {
		return LedgerEntry{}, money.ErrCurrencyMismatch
	}

	var (
		expected money.Money
		err      error
	)
	switch direction {
	case DirectionDebit:
		expected, err = balanceBefore.Sub(amount)
	case DirectionCredit:
		expected, err = balanceBefore.Add(amount)
	}
	if err != nil {
		return LedgerEntry{}, err
	}
	if !expected.Equal(balanceAfter) {
		return LedgerEntry{}, ErrBalanceMismatch
	}

	return LedgerEntry{
		id:            id,
		walletID:      walletID,
		transactionID: transactionID,
		direction:     direction,
		amount:        amount,
		balanceBefore: balanceBefore,
		balanceAfter:  balanceAfter,
		createdAt:     createdAt,
	}, nil
}

func (e LedgerEntry) ID() uuid.UUID              { return e.id }
func (e LedgerEntry) WalletID() uuid.UUID        { return e.walletID }
func (e LedgerEntry) TransactionID() uuid.UUID   { return e.transactionID }
func (e LedgerEntry) Direction() Direction       { return e.direction }
func (e LedgerEntry) Amount() money.Money        { return e.amount }
func (e LedgerEntry) BalanceBefore() money.Money { return e.balanceBefore }
func (e LedgerEntry) BalanceAfter() money.Money  { return e.balanceAfter }
func (e LedgerEntry) CreatedAt() time.Time       { return e.createdAt }
