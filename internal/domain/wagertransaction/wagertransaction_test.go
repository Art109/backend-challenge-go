package wagertransaction_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"backend-challenge-go/internal/domain/money"
	"backend-challenge-go/internal/domain/wagertransaction"
)

var fixedTime = time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

func mustMoney(t *testing.T, amount string) money.Money {
	t.Helper()
	m, err := money.ParseExternal(amount, "BRL")
	if err != nil {
		t.Fatalf("ParseExternal(%q): %v", amount, err)
	}
	return m
}

func baseParams(t *testing.T, kind wagertransaction.Kind, amount string, reference string) wagertransaction.NewExternalParams {
	t.Helper()
	return wagertransaction.NewExternalParams{
		ID:                             uuid.New(),
		ProviderID:                     "provider-a",
		ExternalTransactionID:          "transaction-123",
		IdempotencyKey:                 "provider-a:transaction-123",
		PayloadHash:                    "deadbeef",
		WalletID:                       uuid.New(),
		PlayerID:                       uuid.New(),
		RoundID:                        "round-987",
		GameID:                         "fortune-chimp",
		Kind:                           kind,
		Money:                          mustMoney(t, amount),
		ReferenceExternalTransactionID: reference,
		Now:                            fixedTime,
	}
}

func TestNewExternal_BET_Valid(t *testing.T) {
	tx, err := wagertransaction.NewExternal(baseParams(t, wagertransaction.KindBet, "25.00", ""))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tx.Status() != wagertransaction.StatusPending {
		t.Errorf("Status() = %s, want PENDING", tx.Status())
	}
}

func TestNewExternal_BET_RejectsReference(t *testing.T) {
	_, err := wagertransaction.NewExternal(baseParams(t, wagertransaction.KindBet, "25.00", "some-bet"))
	if !errors.Is(err, wagertransaction.ErrUnexpectedReference) {
		t.Fatalf("expected ErrUnexpectedReference, got %v", err)
	}
}

func TestNewExternal_BET_RejectsNonPositive(t *testing.T) {
	_, err := wagertransaction.NewExternal(baseParams(t, wagertransaction.KindBet, "0.00", ""))
	if !errors.Is(err, wagertransaction.ErrNonPositiveAmount) {
		t.Fatalf("expected ErrNonPositiveAmount, got %v", err)
	}
}

func TestNewExternal_WIN_ReferenceOptional(t *testing.T) {
	if _, err := wagertransaction.NewExternal(baseParams(t, wagertransaction.KindWin, "50.00", "")); err != nil {
		t.Errorf("WIN without reference: unexpected error: %v", err)
	}
	if _, err := wagertransaction.NewExternal(baseParams(t, wagertransaction.KindWin, "50.00", "bet-1")); err != nil {
		t.Errorf("WIN with reference: unexpected error: %v", err)
	}
}

func TestNewExternal_LOSS_RequiresExactZero(t *testing.T) {
	if _, err := wagertransaction.NewExternal(baseParams(t, wagertransaction.KindLoss, "0.00", "")); err != nil {
		t.Errorf("LOSS 0.00: unexpected error: %v", err)
	}
	if _, err := wagertransaction.NewExternal(baseParams(t, wagertransaction.KindLoss, "10.00", "")); !errors.Is(err, wagertransaction.ErrNonZeroLossAmount) {
		t.Errorf("LOSS 10.00: expected ErrNonZeroLossAmount, got %v", err)
	}
}

func TestNewExternal_LOSS_RejectsReference(t *testing.T) {
	_, err := wagertransaction.NewExternal(baseParams(t, wagertransaction.KindLoss, "0.00", "bet-1"))
	if !errors.Is(err, wagertransaction.ErrUnexpectedReference) {
		t.Fatalf("expected ErrUnexpectedReference, got %v", err)
	}
}

func TestNewExternal_REFUND_RequiresReference(t *testing.T) {
	_, err := wagertransaction.NewExternal(baseParams(t, wagertransaction.KindRefund, "25.00", ""))
	if !errors.Is(err, wagertransaction.ErrMissingReference) {
		t.Fatalf("expected ErrMissingReference, got %v", err)
	}
	if _, err := wagertransaction.NewExternal(baseParams(t, wagertransaction.KindRefund, "25.00", "bet-1")); err != nil {
		t.Errorf("unexpected error with reference set: %v", err)
	}
}

func TestNewExternal_ROLLBACK_RequiresReference(t *testing.T) {
	_, err := wagertransaction.NewExternal(baseParams(t, wagertransaction.KindRollback, "25.00", ""))
	if !errors.Is(err, wagertransaction.ErrMissingReference) {
		t.Fatalf("expected ErrMissingReference, got %v", err)
	}
}

func TestNewExternal_RejectsOpeningKind(t *testing.T) {
	_, err := wagertransaction.NewExternal(baseParams(t, wagertransaction.KindOpening, "100.00", ""))
	if !errors.Is(err, wagertransaction.ErrInvalidKind) {
		t.Fatalf("expected ErrInvalidKind, got %v", err)
	}
}

func TestNewExternal_RequiresCoreIdentifiers(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*wagertransaction.NewExternalParams)
		want   error
	}{
		{"missing provider", func(p *wagertransaction.NewExternalParams) { p.ProviderID = "" }, wagertransaction.ErrMissingProviderID},
		{"missing external id", func(p *wagertransaction.NewExternalParams) { p.ExternalTransactionID = "" }, wagertransaction.ErrMissingExternalID},
		{"missing idempotency key", func(p *wagertransaction.NewExternalParams) { p.IdempotencyKey = "" }, wagertransaction.ErrMissingIdempotencyKey},
		{"missing payload hash", func(p *wagertransaction.NewExternalParams) { p.PayloadHash = "" }, wagertransaction.ErrMissingPayloadHash},
		{"missing round id", func(p *wagertransaction.NewExternalParams) { p.RoundID = "" }, wagertransaction.ErrMissingRoundID},
		{"missing game id", func(p *wagertransaction.NewExternalParams) { p.GameID = "" }, wagertransaction.ErrMissingGameID},
		{"nil wallet id", func(p *wagertransaction.NewExternalParams) { p.WalletID = uuid.Nil }, wagertransaction.ErrInvalidWalletID},
		{"nil player id", func(p *wagertransaction.NewExternalParams) { p.PlayerID = uuid.Nil }, wagertransaction.ErrInvalidPlayerID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := baseParams(t, wagertransaction.KindBet, "25.00", "")
			tc.mutate(&p)
			_, err := wagertransaction.NewExternal(p)
			if !errors.Is(err, tc.want) {
				t.Fatalf("expected %v, got %v", tc.want, err)
			}
		})
	}
}

func TestNewOpening_Valid(t *testing.T) {
	tx, err := wagertransaction.NewOpening(uuid.New(), uuid.New(), uuid.New(), mustMoney(t, "1000.00"), fixedTime)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tx.Status() != wagertransaction.StatusProcessed {
		t.Errorf("Status() = %s, want PROCESSED", tx.Status())
	}
	if tx.Kind() != wagertransaction.KindOpening {
		t.Errorf("Kind() = %s, want OPENING", tx.Kind())
	}
	balance, ok := tx.ResultBalance()
	if !ok || balance.String() != "1000.00" {
		t.Errorf("ResultBalance() = %s, %v; want 1000.00, true", balance, ok)
	}
}

func TestNewOpening_RejectsNonPositive(t *testing.T) {
	_, err := wagertransaction.NewOpening(uuid.New(), uuid.New(), uuid.New(), money.Zero("BRL"), fixedTime)
	if !errors.Is(err, wagertransaction.ErrNonPositiveAmount) {
		t.Fatalf("expected ErrNonPositiveAmount, got %v", err)
	}
}

func TestTransitions_PendingToProcessed(t *testing.T) {
	tx, _ := wagertransaction.NewExternal(baseParams(t, wagertransaction.KindBet, "25.00", ""))
	processed, err := tx.MarkProcessed(uuid.Nil, mustMoney(t, "975.00"), fixedTime)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if processed.Status() != wagertransaction.StatusProcessed {
		t.Errorf("Status() = %s, want PROCESSED", processed.Status())
	}
	balance, ok := processed.ResultBalance()
	if !ok || balance.String() != "975.00" {
		t.Errorf("ResultBalance() = %s, %v", balance, ok)
	}
}

func TestTransitions_TerminalRejectsFurtherTransitions(t *testing.T) {
	tx, _ := wagertransaction.NewExternal(baseParams(t, wagertransaction.KindBet, "25.00", ""))
	processed, _ := tx.MarkProcessed(uuid.Nil, mustMoney(t, "975.00"), fixedTime)

	if _, err := processed.MarkProcessed(uuid.Nil, mustMoney(t, "975.00"), fixedTime); !errors.Is(err, wagertransaction.ErrTerminalTransaction) {
		t.Errorf("MarkProcessed on terminal: expected ErrTerminalTransaction, got %v", err)
	}
	if _, err := processed.MarkRejected("SOME_CODE", fixedTime); !errors.Is(err, wagertransaction.ErrTerminalTransaction) {
		t.Errorf("MarkRejected on terminal: expected ErrTerminalTransaction, got %v", err)
	}
	if _, err := processed.MarkFailed("SOME_CODE", fixedTime); !errors.Is(err, wagertransaction.ErrTerminalTransaction) {
		t.Errorf("MarkFailed on terminal: expected ErrTerminalTransaction, got %v", err)
	}
}

func TestTransitions_PendingReferenceFlow(t *testing.T) {
	tx, _ := wagertransaction.NewExternal(baseParams(t, wagertransaction.KindRefund, "25.00", "bet-1"))

	pendingRef, err := tx.MarkPendingReference(fixedTime)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pendingRef.Status() != wagertransaction.StatusPendingReference {
		t.Fatalf("Status() = %s, want PENDING_REFERENCE", pendingRef.Status())
	}

	retried, err := pendingRef.IncrementAttempt(fixedTime.Add(time.Minute), fixedTime)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if retried.Attempts() != 1 {
		t.Errorf("Attempts() = %d, want 1", retried.Attempts())
	}

	rejected, err := retried.MarkRejected(wagertransaction.FailureCodeReferenceNotFound, fixedTime)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rejected.Status() != wagertransaction.StatusRejected {
		t.Errorf("Status() = %s, want REJECTED", rejected.Status())
	}
	if rejected.FailureCode() != wagertransaction.FailureCodeReferenceNotFound {
		t.Errorf("FailureCode() = %s, want %s", rejected.FailureCode(), wagertransaction.FailureCodeReferenceNotFound)
	}
}

func TestTransitions_IncrementAttempt_RequiresPendingReference(t *testing.T) {
	tx, _ := wagertransaction.NewExternal(baseParams(t, wagertransaction.KindRefund, "25.00", "bet-1"))
	_, err := tx.IncrementAttempt(fixedTime.Add(time.Minute), fixedTime)
	if !errors.Is(err, wagertransaction.ErrInvalidTransition) {
		t.Fatalf("expected ErrInvalidTransition, got %v", err)
	}
}

func TestTransitions_MarkPendingReference_OnlyFromPending(t *testing.T) {
	tx, _ := wagertransaction.NewExternal(baseParams(t, wagertransaction.KindRefund, "25.00", "bet-1"))
	pendingRef, _ := tx.MarkPendingReference(fixedTime)
	if _, err := pendingRef.MarkPendingReference(fixedTime); !errors.Is(err, wagertransaction.ErrInvalidTransition) {
		t.Fatalf("expected ErrInvalidTransition on double MarkPendingReference, got %v", err)
	}
}

func TestTransitions_RejectRequiresFailureCode(t *testing.T) {
	tx, _ := wagertransaction.NewExternal(baseParams(t, wagertransaction.KindBet, "25.00", ""))
	if _, err := tx.MarkRejected("", fixedTime); !errors.Is(err, wagertransaction.ErrMissingFailureCode) {
		t.Fatalf("expected ErrMissingFailureCode, got %v", err)
	}
}

func TestFailureCodes_InsufficientBalanceDistinctFromReversal(t *testing.T) {
	if wagertransaction.FailureCodeInsufficientBalance == wagertransaction.FailureCodeInsufficientBalanceForReversal {
		t.Fatalf("bet-insufficient-balance and reversal-insufficient-balance must use different failure codes")
	}
}
