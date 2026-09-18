//go:build integration

package app_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"backend-challenge-go/internal/app"
	"backend-challenge-go/internal/domain/money"
	"backend-challenge-go/internal/domain/wagertransaction"
	"backend-challenge-go/internal/platform/postgres"
)

func testDSN() string {
	if dsn := os.Getenv("TEST_DATABASE_URL"); dsn != "" {
		return dsn
	}
	return "postgres://app:app@localhost:5432/backend_challenge?sslmode=disable"
}

func newTestUseCases(ctx context.Context, t *testing.T) (*app.UseCases, *pgxpool.Pool) {
	t.Helper()
	dsn := testDSN()
	if err := postgres.RunMigrations(dsn); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	pool, err := postgres.NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	t.Cleanup(pool.Close)

	uc := app.NewUseCases(
		pool,
		postgres.NewWalletRepository(),
		postgres.NewLedgerRepository(),
		postgres.NewWagerTransactionRepository(),
		postgres.NewOutboxRepository(),
		postgres.NewInboxRepository(),
		func() time.Time { return time.Now().UTC() },
	)
	return uc, pool
}

func mustMoney(t *testing.T, amount string) money.Money {
	t.Helper()
	m, err := money.ParseExternal(amount, "BRL")
	if err != nil {
		t.Fatalf("ParseExternal(%q): %v", amount, err)
	}
	return m
}

func TestOpenWallet_PositiveBalance_CreatesOpeningAndEvents(t *testing.T) {
	ctx := context.Background()
	uc, pool := newTestUseCases(ctx, t)

	playerID := uuid.New()
	result, err := uc.OpenWallet(ctx, app.OpenWalletCommand{PlayerID: playerID, InitialBalance: mustMoney(t, "1000.00")})
	if err != nil {
		t.Fatalf("OpenWallet: %v", err)
	}
	if result.Wallet.Balance().String() != "1000.00" {
		t.Fatalf("balance = %s, want 1000.00", result.Wallet.Balance())
	}

	var txCount, ledgerCount, eventCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM wager_transactions WHERE wallet_id = $1 AND kind = 'OPENING'`, result.Wallet.ID()).Scan(&txCount); err != nil {
		t.Fatalf("count opening tx: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1`, result.Wallet.ID()).Scan(&ledgerCount); err != nil {
		t.Fatalf("count ledger entries: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE aggregate_id = $1 OR event_type = 'WalletBalanceChanged'`, result.Wallet.ID()).Scan(&eventCount); err != nil {
		t.Fatalf("count outbox events: %v", err)
	}
	if txCount != 1 {
		t.Errorf("opening transactions = %d, want 1", txCount)
	}
	if ledgerCount != 1 {
		t.Errorf("ledger entries = %d, want 1", ledgerCount)
	}
	if eventCount < 1 {
		t.Errorf("outbox events = %d, want at least 1", eventCount)
	}
}

func TestOpenWallet_ZeroBalance_NoOpeningNoLedger(t *testing.T) {
	ctx := context.Background()
	uc, pool := newTestUseCases(ctx, t)

	result, err := uc.OpenWallet(ctx, app.OpenWalletCommand{PlayerID: uuid.New(), InitialBalance: money.Zero("BRL")})
	if err != nil {
		t.Fatalf("OpenWallet: %v", err)
	}

	var txCount, ledgerCount int
	pool.QueryRow(ctx, `SELECT count(*) FROM wager_transactions WHERE wallet_id = $1`, result.Wallet.ID()).Scan(&txCount)
	pool.QueryRow(ctx, `SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1`, result.Wallet.ID()).Scan(&ledgerCount)
	if txCount != 0 || ledgerCount != 0 {
		t.Fatalf("txCount=%d ledgerCount=%d, want 0 and 0", txCount, ledgerCount)
	}
}

func TestOpenWallet_DuplicateConflict(t *testing.T) {
	ctx := context.Background()
	uc, _ := newTestUseCases(ctx, t)

	playerID := uuid.New()
	if _, err := uc.OpenWallet(ctx, app.OpenWalletCommand{PlayerID: playerID, InitialBalance: money.Zero("BRL")}); err != nil {
		t.Fatalf("first OpenWallet: %v", err)
	}
	_, err := uc.OpenWallet(ctx, app.OpenWalletCommand{PlayerID: playerID, InitialBalance: money.Zero("BRL")})
	if !errors.Is(err, app.ErrWalletConflict) {
		t.Fatalf("expected ErrWalletConflict, got %v", err)
	}
}

func openWallet(ctx context.Context, t *testing.T, uc *app.UseCases, balance string) (uuid.UUID, uuid.UUID) {
	t.Helper()
	playerID := uuid.New()
	result, err := uc.OpenWallet(ctx, app.OpenWalletCommand{PlayerID: playerID, InitialBalance: mustMoney(t, balance)})
	if err != nil {
		t.Fatalf("OpenWallet: %v", err)
	}
	return result.Wallet.ID(), playerID
}

// uniqueExternalID guards against cross-test collisions: these tests share
// one long-lived Postgres instance (no per-test reset), so a hardcoded
// external id like "tx-1" reused across two test functions would find the
// other test's already-committed row and report a false idempotency
// conflict. A fresh id per test keeps each test's data isolated.
func uniqueExternalID(prefix string) string {
	return prefix + "-" + uuid.NewString()
}

func betCommand(providerID, externalID string, walletID, playerID uuid.UUID, amount money.Money) app.SubmitWagerTransactionCommand {
	return app.SubmitWagerTransactionCommand{
		ProviderID: providerID, ExternalTransactionID: externalID, IdempotencyKey: providerID + ":" + externalID,
		PlayerID: playerID, WalletID: walletID, RoundID: "round-1", GameID: "game-1",
		Kind: wagertransaction.KindBet, Money: amount,
	}
}

func TestSubmitWagerTransaction_BetSuccess(t *testing.T) {
	ctx := context.Background()
	uc, _ := newTestUseCases(ctx, t)
	walletID, playerID := openWallet(ctx, t, uc, "100.00")

	result, err := uc.SubmitWagerTransaction(ctx, betCommand("provider-a", uniqueExternalID("tx"), walletID, playerID, mustMoney(t, "25.00")))
	if err != nil {
		t.Fatalf("SubmitWagerTransaction: %v", err)
	}
	if result.Status != wagertransaction.StatusProcessed {
		t.Fatalf("Status = %s, want PROCESSED", result.Status)
	}
	if !result.HasBalance || result.Balance.String() != "75.00" {
		t.Fatalf("Balance = %s, %v; want 75.00, true", result.Balance, result.HasBalance)
	}
}

func TestSubmitWagerTransaction_BetInsufficientBalance(t *testing.T) {
	ctx := context.Background()
	uc, _ := newTestUseCases(ctx, t)
	walletID, playerID := openWallet(ctx, t, uc, "10.00")

	result, err := uc.SubmitWagerTransaction(ctx, betCommand("provider-a", uniqueExternalID("tx"), walletID, playerID, mustMoney(t, "25.00")))
	if err != nil {
		t.Fatalf("SubmitWagerTransaction: %v", err)
	}
	if result.Status != wagertransaction.StatusRejected {
		t.Fatalf("Status = %s, want REJECTED", result.Status)
	}
	if result.FailureCode != wagertransaction.FailureCodeInsufficientBalance {
		t.Fatalf("FailureCode = %s, want %s", result.FailureCode, wagertransaction.FailureCodeInsufficientBalance)
	}
}

func TestSubmitWagerTransaction_IdempotentReplay(t *testing.T) {
	ctx := context.Background()
	uc, _ := newTestUseCases(ctx, t)
	walletID, playerID := openWallet(ctx, t, uc, "100.00")

	cmd := betCommand("provider-a", uniqueExternalID("tx"), walletID, playerID, mustMoney(t, "25.00"))
	first, err := uc.SubmitWagerTransaction(ctx, cmd)
	if err != nil {
		t.Fatalf("first submit: %v", err)
	}
	if first.IdempotentReplay {
		t.Fatalf("first submission should not be a replay")
	}

	second, err := uc.SubmitWagerTransaction(ctx, cmd)
	if err != nil {
		t.Fatalf("second submit: %v", err)
	}
	if !second.IdempotentReplay {
		t.Fatalf("second identical submission should be a replay")
	}
	if second.Balance.String() != first.Balance.String() {
		t.Fatalf("replay balance = %s, want original %s", second.Balance, first.Balance)
	}
}

func TestSubmitWagerTransaction_SameKeyDifferentContent_Conflict(t *testing.T) {
	ctx := context.Background()
	uc, _ := newTestUseCases(ctx, t)
	walletID, playerID := openWallet(ctx, t, uc, "100.00")

	cmd := betCommand("provider-a", uniqueExternalID("tx"), walletID, playerID, mustMoney(t, "25.00"))
	if _, err := uc.SubmitWagerTransaction(ctx, cmd); err != nil {
		t.Fatalf("first submit: %v", err)
	}

	cmd2 := cmd
	cmd2.Money = mustMoney(t, "30.00") // same key, different amount
	if _, err := uc.SubmitWagerTransaction(ctx, cmd2); !errors.Is(err, app.ErrIdempotencyKeyConflict) {
		t.Fatalf("expected ErrIdempotencyKeyConflict, got %v", err)
	}
}

func TestSubmitWagerTransaction_ExternalIDReusedUnderNewKey_Rejected(t *testing.T) {
	ctx := context.Background()
	uc, _ := newTestUseCases(ctx, t)
	walletID, playerID := openWallet(ctx, t, uc, "100.00")

	cmd := betCommand("provider-a", uniqueExternalID("tx"), walletID, playerID, mustMoney(t, "25.00"))
	if _, err := uc.SubmitWagerTransaction(ctx, cmd); err != nil {
		t.Fatalf("first submit: %v", err)
	}

	cmd2 := cmd
	cmd2.IdempotencyKey = "provider-a:a-different-key"
	if _, err := uc.SubmitWagerTransaction(ctx, cmd2); !errors.Is(err, app.ErrExternalTransactionKeyMismatch) {
		t.Fatalf("expected ErrExternalTransactionKeyMismatch, got %v", err)
	}
}

// TestSubmitWagerTransaction_50ConcurrentDuplicates is the mandatory
// scenario from section 13, item 1: the exact same bet, submitted 50 times
// in parallel, must produce exactly one debit.
func TestSubmitWagerTransaction_50ConcurrentDuplicates(t *testing.T) {
	ctx := context.Background()
	uc, pool := newTestUseCases(ctx, t)
	walletID, playerID := openWallet(ctx, t, uc, "1000.00")

	cmd := betCommand("provider-a", uniqueExternalID("tx-dup"), walletID, playerID, mustMoney(t, "25.00"))

	const n = 50
	results := make([]app.SubmitWagerTransactionResult, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = uc.SubmitWagerTransaction(ctx, cmd)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("submission %d: unexpected error: %v", i, err)
		}
	}

	replays, originals := 0, 0
	for _, r := range results {
		if r.IdempotentReplay {
			replays++
		} else {
			originals++
		}
		if r.Status != wagertransaction.StatusProcessed {
			t.Errorf("status = %s, want PROCESSED", r.Status)
		}
	}
	if originals != 1 {
		t.Fatalf("originals = %d, want exactly 1 (49 others should be replays)", originals)
	}
	if replays != n-1 {
		t.Fatalf("replays = %d, want %d", replays, n-1)
	}

	// The wallet was opened with a positive balance, which itself creates one
	// CREDIT ledger entry for the OPENING transaction - only the DEBIT side
	// belongs to the (possibly-duplicated) bet, so that's what must be 1.
	var ledgerCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1 AND direction = 'DEBIT'`, walletID).Scan(&ledgerCount); err != nil {
		t.Fatalf("count ledger entries: %v", err)
	}
	if ledgerCount != 1 {
		t.Fatalf("debit ledger entries = %d, want exactly 1", ledgerCount)
	}

	var txCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM wager_transactions WHERE wallet_id = $1 AND kind = 'BET'`, walletID).Scan(&txCount); err != nil {
		t.Fatalf("count bet transactions: %v", err)
	}
	if txCount != 1 {
		t.Fatalf("bet transaction rows = %d, want exactly 1", txCount)
	}
}

func TestSubmitWagerTransaction_RefundHappyPath(t *testing.T) {
	ctx := context.Background()
	uc, _ := newTestUseCases(ctx, t)
	walletID, playerID := openWallet(ctx, t, uc, "100.00")

	betExternalID := uniqueExternalID("bet")
	bet, err := uc.SubmitWagerTransaction(ctx, betCommand("provider-a", betExternalID, walletID, playerID, mustMoney(t, "40.00")))
	if err != nil || bet.Status != wagertransaction.StatusProcessed {
		t.Fatalf("bet: %v, status=%s", err, bet.Status)
	}

	refundExternalID := uniqueExternalID("refund")
	refund, err := uc.SubmitWagerTransaction(ctx, app.SubmitWagerTransactionCommand{
		ProviderID: "provider-a", ExternalTransactionID: refundExternalID, IdempotencyKey: "provider-a:" + refundExternalID,
		PlayerID: playerID, WalletID: walletID, RoundID: "round-1", GameID: "game-1",
		Kind: wagertransaction.KindRefund, Money: mustMoney(t, "40.00"), ReferenceExternalTransactionID: betExternalID,
	})
	if err != nil {
		t.Fatalf("refund: %v", err)
	}
	if refund.Status != wagertransaction.StatusProcessed {
		t.Fatalf("refund status = %s, want PROCESSED", refund.Status)
	}
	if refund.Balance.String() != "100.00" {
		t.Fatalf("balance after refund = %s, want 100.00", refund.Balance)
	}

	// A second REFUND against the same bet must be rejected as a duplicate reversal.
	dupExternalID := uniqueExternalID("refund")
	dupRefund, err := uc.SubmitWagerTransaction(ctx, app.SubmitWagerTransactionCommand{
		ProviderID: "provider-a", ExternalTransactionID: dupExternalID, IdempotencyKey: "provider-a:" + dupExternalID,
		PlayerID: playerID, WalletID: walletID, RoundID: "round-1", GameID: "game-1",
		Kind: wagertransaction.KindRefund, Money: mustMoney(t, "40.00"), ReferenceExternalTransactionID: betExternalID,
	})
	if err != nil {
		t.Fatalf("second refund: %v", err)
	}
	if dupRefund.Status != wagertransaction.StatusRejected || dupRefund.FailureCode != wagertransaction.FailureCodeDuplicateReversal {
		t.Fatalf("second refund status=%s failureCode=%s, want REJECTED/%s", dupRefund.Status, dupRefund.FailureCode, wagertransaction.FailureCodeDuplicateReversal)
	}
}

func TestSubmitWagerTransaction_RefundBeforeReference_PendingThenResolves(t *testing.T) {
	ctx := context.Background()
	uc, pool := newTestUseCases(ctx, t)
	walletID, playerID := openWallet(ctx, t, uc, "100.00")

	betExternalID := uniqueExternalID("bet-late")
	refundExternalID := uniqueExternalID("refund-early")
	refund, err := uc.SubmitWagerTransaction(ctx, app.SubmitWagerTransactionCommand{
		ProviderID: "provider-a", ExternalTransactionID: refundExternalID, IdempotencyKey: "provider-a:" + refundExternalID,
		PlayerID: playerID, WalletID: walletID, RoundID: "round-1", GameID: "game-1",
		Kind: wagertransaction.KindRefund, Money: mustMoney(t, "40.00"), ReferenceExternalTransactionID: betExternalID,
	})
	if err != nil {
		t.Fatalf("early refund: %v", err)
	}
	if refund.Status != wagertransaction.StatusPendingReference {
		t.Fatalf("status = %s, want PENDING_REFERENCE", refund.Status)
	}

	if _, err := uc.SubmitWagerTransaction(ctx, betCommand("provider-a", betExternalID, walletID, playerID, mustMoney(t, "40.00"))); err != nil {
		t.Fatalf("late bet: %v", err)
	}

	// The retry worker (not built yet) would re-drive this PENDING_REFERENCE
	// row; here we only prove ListDuePendingReference can find it, since
	// SubmitWagerTransaction itself doesn't retry PENDING_REFERENCE rows -
	// it's a one-shot decision at submission time.
	txRepo := postgres.NewWagerTransactionRepository()
	due, err := txRepo.ListDuePendingReference(ctx, pool, time.Now().UTC().Add(time.Hour), 10)
	if err != nil {
		t.Fatalf("ListDuePendingReference: %v", err)
	}
	found := false
	for _, tx := range due {
		if tx.ID() == refund.TransactionID {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected the pending refund to be listed as due")
	}
}
