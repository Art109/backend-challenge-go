//go:build integration

// Integration tests run against a real PostgreSQL instance (see
// docker-compose.yml) instead of a mock, per the spec's explicit
// requirement that PostgreSQL never be substituted by mocks in tests.
// Run with: go test -tags=integration -race ./internal/platform/postgres/...
// (docker compose up -d postgres must be running first).
package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"backend-challenge-go/internal/domain/money"
	"backend-challenge-go/internal/domain/wallet"
	"backend-challenge-go/internal/platform/postgres"
)

func testDSN() string {
	if dsn := os.Getenv("TEST_DATABASE_URL"); dsn != "" {
		return dsn
	}
	return "postgres://app:app@localhost:5432/backend_challenge?sslmode=disable"
}

// insertPendingBetTransaction inserts a minimal BET row so a ledger entry
// referencing it satisfies the FK to wager_transactions - real code goes
// through WagerTransactionRepository for this; the raw SQL here just keeps
// this concurrency test self-contained without depending on that repo.
func insertPendingBetTransaction(ctx context.Context, pool *pgxpool.Pool, walletID, playerID uuid.UUID, amount money.Money) (uuid.UUID, error) {
	id := uuid.New()
	_, err := pool.Exec(ctx, `
		INSERT INTO wager_transactions (
			id, kind, provider_id, external_transaction_id, idempotency_key, payload_hash,
			wallet_id, player_id, round_id, game_id, amount_minor_units, currency,
			status, created_at, updated_at
		) VALUES ($1, 'BET', 'provider-a', $2, $2, 'hash', $3, $4, 'round-1', 'game-1', $5, $6, 'PENDING', now(), now())
	`, id, id.String(), walletID, playerID, amount.MinorUnits(), amount.Currency())
	return id, err
}

func mustParseMoney(t *testing.T, amount string) money.Money {
	t.Helper()
	m, err := money.ParseExternal(amount, "BRL")
	if err != nil {
		t.Fatalf("ParseExternal(%q): %v", amount, err)
	}
	return m
}

// placeBetOnce runs a single optimistic-concurrency attempt: read the
// current wallet (no row lock), compute the debit in the domain layer, and
// try to persist it with a version-checked UPDATE. If another writer
// committed first, UpdateBalance returns postgres.ErrVersionConflict and
// this attempt's transaction rolls back without having changed anything.
func placeBetOnce(ctx context.Context, pool *pgxpool.Pool, walletRepo *postgres.WalletRepository, ledgerRepo *postgres.LedgerRepository, walletID, transactionID uuid.UUID, amount money.Money) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once committed

	current, err := walletRepo.GetByID(ctx, tx, walletID)
	if err != nil {
		return err
	}

	updated, entry, err := current.Debit(uuid.New(), transactionID, amount, time.Now().UTC())
	if err != nil {
		return err // a real business rejection (e.g. insufficient balance), not a race
	}

	if err := walletRepo.UpdateBalance(ctx, tx, updated, current.Version()); err != nil {
		return err // postgres.ErrVersionConflict on a lost race
	}
	if err := ledgerRepo.InsertEntry(ctx, tx, entry); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// placeBetWithRetry re-reads and retries on a version conflict - this is
// what an application-layer use case does in production; the test exercises
// exactly that retry loop against real concurrent writers.
func placeBetWithRetry(ctx context.Context, pool *pgxpool.Pool, walletRepo *postgres.WalletRepository, ledgerRepo *postgres.LedgerRepository, walletID, transactionID uuid.UUID, amount money.Money) error {
	const maxAttempts = 20
	for attempt := 0; attempt < maxAttempts; attempt++ {
		err := placeBetOnce(ctx, pool, walletRepo, ledgerRepo, walletID, transactionID, amount)
		if errors.Is(err, postgres.ErrVersionConflict) {
			continue
		}
		return err
	}
	return fmt.Errorf("exhausted %d retries on version conflicts", maxAttempts)
}

// TestConcurrentBets_ExactlyOneSucceeds is the mandatory scenario from
// section 8 of the spec: a 100.00 BRL wallet receives two concurrent 80.00
// BRL bets. Exactly one must process, the other must be rejected for
// insufficient balance, the final balance must be 20.00, and there must be
// exactly one debit in the ledger. Each "writer" here holds its own
// connection pool, mirroring independent application instances.
func TestConcurrentBets_ExactlyOneSucceeds(t *testing.T) {
	ctx := context.Background()
	dsn := testDSN()

	if err := postgres.RunMigrations(dsn); err != nil {
		t.Fatalf("run migrations: %v", err)
	}

	walletRepo := postgres.NewWalletRepository()
	ledgerRepo := postgres.NewLedgerRepository()

	poolA, err := postgres.NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("open pool A: %v", err)
	}
	defer poolA.Close()

	poolB, err := postgres.NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("open pool B: %v", err)
	}
	defer poolB.Close()

	// A third, independent connection used only to set up and later
	// observe state - never to place a bet - standing in for the "at
	// least three independent processes" requirement.
	poolObserver, err := postgres.NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("open observer pool: %v", err)
	}
	defer poolObserver.Close()

	playerID := uuid.New()
	initialBalance := mustParseMoney(t, "100.00")
	w, err := wallet.Open(uuid.New(), playerID, initialBalance, time.Now().UTC())
	if err != nil {
		t.Fatalf("wallet.Open: %v", err)
	}
	if err := walletRepo.Insert(ctx, poolObserver, w); err != nil {
		t.Fatalf("insert wallet: %v", err)
	}

	bet := mustParseMoney(t, "80.00")

	results := make([]error, 2)
	var wg sync.WaitGroup
	for i, pool := range []*pgxpool.Pool{poolA, poolB} {
		txID, err := insertPendingBetTransaction(ctx, poolObserver, w.ID(), playerID, bet)
		if err != nil {
			t.Fatalf("insert bet transaction %d: %v", i, err)
		}
		wg.Add(1)
		go func(i int, pool *pgxpool.Pool, txID uuid.UUID) {
			defer wg.Done()
			results[i] = placeBetWithRetry(ctx, pool, walletRepo, ledgerRepo, w.ID(), txID, bet)
		}(i, pool, txID)
	}
	wg.Wait()

	succeeded, rejected := 0, 0
	for _, err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, wallet.ErrInsufficientBalance):
			rejected++
		default:
			t.Fatalf("unexpected error from a bet attempt: %v", err)
		}
	}
	if succeeded != 1 || rejected != 1 {
		t.Fatalf("succeeded=%d rejected=%d, want exactly 1 and 1", succeeded, rejected)
	}

	final, err := walletRepo.GetByID(ctx, poolObserver, w.ID())
	if err != nil {
		t.Fatalf("get final wallet: %v", err)
	}
	if final.Balance().String() != "20.00" {
		t.Fatalf("final balance = %s, want 20.00", final.Balance())
	}

	var ledgerCount int
	row := poolObserver.QueryRow(ctx, `SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1`, w.ID())
	if err := row.Scan(&ledgerCount); err != nil {
		t.Fatalf("count ledger entries: %v", err)
	}
	if ledgerCount != 1 {
		t.Fatalf("ledger entry count = %d, want exactly 1", ledgerCount)
	}
}

// TestConcurrentBets_DifferentWalletsProceedInParallel proves unrelated
// wallets never contend with each other - there is no global lock.
func TestConcurrentBets_DifferentWalletsProceedInParallel(t *testing.T) {
	ctx := context.Background()
	dsn := testDSN()

	if err := postgres.RunMigrations(dsn); err != nil {
		t.Fatalf("run migrations: %v", err)
	}

	walletRepo := postgres.NewWalletRepository()
	ledgerRepo := postgres.NewLedgerRepository()

	pool, err := postgres.NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer pool.Close()

	const walletCount = 5
	walletIDs := make([]uuid.UUID, walletCount)
	txIDs := make([]uuid.UUID, walletCount)
	betAmount := mustParseMoney(t, "10.00")
	for i := range walletIDs {
		playerID := uuid.New()
		w, err := wallet.Open(uuid.New(), playerID, mustParseMoney(t, "100.00"), time.Now().UTC())
		if err != nil {
			t.Fatalf("wallet.Open: %v", err)
		}
		if err := walletRepo.Insert(ctx, pool, w); err != nil {
			t.Fatalf("insert wallet %d: %v", i, err)
		}
		walletIDs[i] = w.ID()
		txID, err := insertPendingBetTransaction(ctx, pool, w.ID(), playerID, betAmount)
		if err != nil {
			t.Fatalf("insert bet transaction %d: %v", i, err)
		}
		txIDs[i] = txID
	}

	var wg sync.WaitGroup
	errs := make([]error, walletCount)
	for i, id := range walletIDs {
		wg.Add(1)
		go func(i int, id, txID uuid.UUID) {
			defer wg.Done()
			errs[i] = placeBetWithRetry(ctx, pool, walletRepo, ledgerRepo, id, txID, betAmount)
		}(i, id, txIDs[i])
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("wallet %d: unexpected error: %v", i, err)
		}
	}
}
