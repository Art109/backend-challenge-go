package events_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"backend-challenge-go/internal/domain/events"
	"backend-challenge-go/internal/domain/money"
)

var fixedTime = time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

func TestNewEnvelope_Valid(t *testing.T) {
	txID := uuid.New()
	payload := events.WagerTransactionProcessedData{
		TransactionID: txID,
		Kind:          "BET",
		Money:         mustMoney(t, "25.00"),
	}
	env, err := events.NewEnvelope(uuid.New(), payload, uuid.New(), nil, fixedTime)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if env.EventType != "WagerTransactionProcessed" {
		t.Errorf("EventType = %s, want WagerTransactionProcessed", env.EventType)
	}
	if env.Version != 1 {
		t.Errorf("Version = %d, want 1", env.Version)
	}
	if env.AggregateID != txID {
		t.Errorf("AggregateID = %s, want %s", env.AggregateID, txID)
	}
}

func TestNewEnvelope_RejectsNilEventID(t *testing.T) {
	payload := events.WagerTransactionProcessedData{TransactionID: uuid.New()}
	_, err := events.NewEnvelope(uuid.Nil, payload, uuid.New(), nil, fixedTime)
	if !errors.Is(err, events.ErrInvalidEventID) {
		t.Fatalf("expected ErrInvalidEventID, got %v", err)
	}
}

func TestNewEnvelope_RejectsNilAggregateID(t *testing.T) {
	payload := events.WagerTransactionProcessedData{} // TransactionID left as uuid.Nil
	_, err := events.NewEnvelope(uuid.New(), payload, uuid.New(), nil, fixedTime)
	if !errors.Is(err, events.ErrInvalidAggregateID) {
		t.Fatalf("expected ErrInvalidAggregateID, got %v", err)
	}
}

func TestNewEnvelope_RejectsNilCorrelationID(t *testing.T) {
	payload := events.WagerTransactionProcessedData{TransactionID: uuid.New()}
	_, err := events.NewEnvelope(uuid.New(), payload, uuid.Nil, nil, fixedTime)
	if !errors.Is(err, events.ErrInvalidCorrelationID) {
		t.Fatalf("expected ErrInvalidCorrelationID, got %v", err)
	}
}

func TestWalletBalanceChanged_AggregateIsWallet(t *testing.T) {
	walletID := uuid.New()
	payload := events.WalletBalanceChangedData{
		WalletID:      walletID,
		TransactionID: uuid.New(),
		WalletVersion: 2,
	}
	env, err := events.NewEnvelope(uuid.New(), payload, uuid.New(), nil, fixedTime)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if env.AggregateID != walletID {
		t.Errorf("AggregateID = %s, want wallet id %s", env.AggregateID, walletID)
	}
	if env.EventType != "WalletBalanceChanged" {
		t.Errorf("EventType = %s, want WalletBalanceChanged", env.EventType)
	}
}

func mustMoney(t *testing.T, amount string) money.Money {
	t.Helper()
	m, err := money.ParseExternal(amount, "BRL")
	if err != nil {
		t.Fatalf("ParseExternal(%q): %v", amount, err)
	}
	return m
}
