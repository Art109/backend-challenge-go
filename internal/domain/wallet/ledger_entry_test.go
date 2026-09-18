package wallet_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"backend-challenge-go/internal/domain/money"
	"backend-challenge-go/internal/domain/wallet"
)

func TestNewLedgerEntry_DebitInvariant(t *testing.T) {
	entry, err := wallet.NewLedgerEntry(
		uuid.New(), uuid.New(), uuid.New(),
		wallet.DirectionDebit,
		mustMoney(t, "80.00", "BRL"),
		mustMoney(t, "100.00", "BRL"),
		mustMoney(t, "20.00", "BRL"),
		fixedTime,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if entry.Amount().String() != "80.00" {
		t.Errorf("Amount() = %s, want 80.00", entry.Amount())
	}
}

func TestNewLedgerEntry_CreditInvariant(t *testing.T) {
	_, err := wallet.NewLedgerEntry(
		uuid.New(), uuid.New(), uuid.New(),
		wallet.DirectionCredit,
		mustMoney(t, "25.00", "BRL"),
		mustMoney(t, "100.00", "BRL"),
		mustMoney(t, "125.00", "BRL"),
		fixedTime,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNewLedgerEntry_RejectsBrokenInvariant(t *testing.T) {
	_, err := wallet.NewLedgerEntry(
		uuid.New(), uuid.New(), uuid.New(),
		wallet.DirectionDebit,
		mustMoney(t, "80.00", "BRL"),
		mustMoney(t, "100.00", "BRL"),
		mustMoney(t, "50.00", "BRL"), // wrong: should be 20.00
		fixedTime,
	)
	if !errors.Is(err, wallet.ErrBalanceMismatch) {
		t.Fatalf("expected ErrBalanceMismatch, got %v", err)
	}
}

func TestNewLedgerEntry_RejectsNonPositiveAmount(t *testing.T) {
	_, err := wallet.NewLedgerEntry(
		uuid.New(), uuid.New(), uuid.New(),
		wallet.DirectionDebit,
		money.Zero("BRL"),
		mustMoney(t, "100.00", "BRL"),
		mustMoney(t, "100.00", "BRL"),
		fixedTime,
	)
	if !errors.Is(err, wallet.ErrNonPositiveAmount) {
		t.Fatalf("expected ErrNonPositiveAmount, got %v", err)
	}
}

func TestNewLedgerEntry_RejectsInvalidDirection(t *testing.T) {
	_, err := wallet.NewLedgerEntry(
		uuid.New(), uuid.New(), uuid.New(),
		wallet.Direction("SIDEWAYS"),
		mustMoney(t, "10.00", "BRL"),
		mustMoney(t, "100.00", "BRL"),
		mustMoney(t, "110.00", "BRL"),
		fixedTime,
	)
	if !errors.Is(err, wallet.ErrInvalidDirection) {
		t.Fatalf("expected ErrInvalidDirection, got %v", err)
	}
}

func TestNewLedgerEntry_RejectsNilIDs(t *testing.T) {
	valid := func() (uuid.UUID, uuid.UUID, uuid.UUID) { return uuid.New(), uuid.New(), uuid.New() }

	id, walletID, txID := valid()
	if _, err := wallet.NewLedgerEntry(uuid.Nil, walletID, txID, wallet.DirectionCredit,
		mustMoney(t, "10.00", "BRL"), mustMoney(t, "100.00", "BRL"), mustMoney(t, "110.00", "BRL"), fixedTime); !errors.Is(err, wallet.ErrInvalidLedgerEntryID) {
		t.Errorf("expected ErrInvalidLedgerEntryID, got %v", err)
	}

	id, walletID, txID = valid()
	if _, err := wallet.NewLedgerEntry(id, uuid.Nil, txID, wallet.DirectionCredit,
		mustMoney(t, "10.00", "BRL"), mustMoney(t, "100.00", "BRL"), mustMoney(t, "110.00", "BRL"), fixedTime); !errors.Is(err, wallet.ErrInvalidWalletID) {
		t.Errorf("expected ErrInvalidWalletID, got %v", err)
	}

	id, walletID, _ = valid()
	if _, err := wallet.NewLedgerEntry(id, walletID, uuid.Nil, wallet.DirectionCredit,
		mustMoney(t, "10.00", "BRL"), mustMoney(t, "100.00", "BRL"), mustMoney(t, "110.00", "BRL"), fixedTime); !errors.Is(err, wallet.ErrInvalidTransactionID) {
		t.Errorf("expected ErrInvalidTransactionID, got %v", err)
	}
}

func TestNewLedgerEntry_RejectsCurrencyMismatch(t *testing.T) {
	_, err := wallet.NewLedgerEntry(
		uuid.New(), uuid.New(), uuid.New(),
		wallet.DirectionCredit,
		mustMoney(t, "10.00", "USD"),
		mustMoney(t, "100.00", "BRL"),
		mustMoney(t, "110.00", "BRL"),
		fixedTime,
	)
	if !errors.Is(err, money.ErrCurrencyMismatch) {
		t.Fatalf("expected money.ErrCurrencyMismatch, got %v", err)
	}
}
