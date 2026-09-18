//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"backend-challenge-go/internal/domain/events"
	"backend-challenge-go/internal/domain/wagertransaction"
	"backend-challenge-go/internal/domain/wallet"
	"backend-challenge-go/internal/platform/postgres"
)

func openTestWallet(ctx context.Context, t *testing.T, pool *pgxpool.Pool, balance string) uuid.UUID {
	t.Helper()
	repo := postgres.NewWalletRepository()
	w, err := wallet.Open(uuid.New(), uuid.New(), mustParseMoney(t, balance), time.Now().UTC())
	if err != nil {
		t.Fatalf("wallet.Open: %v", err)
	}
	if err := repo.Insert(ctx, pool, w); err != nil {
		t.Fatalf("insert wallet: %v", err)
	}
	return w.ID()
}

func TestWagerTransactionRepository_InsertGetIdempotency(t *testing.T) {
	ctx := context.Background()
	dsn := testDSN()
	if err := postgres.RunMigrations(dsn); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	pool, err := postgres.NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer pool.Close()

	repo := postgres.NewWagerTransactionRepository()
	walletID := openTestWallet(ctx, t, pool, "100.00")
	playerID := uuid.New()
	externalID := "ext-" + uuid.NewString()

	tx, err := wagertransaction.NewExternal(wagertransaction.NewExternalParams{
		ID: uuid.New(), ProviderID: "provider-a", ExternalTransactionID: externalID,
		IdempotencyKey: "provider-a:" + externalID, PayloadHash: "hash1",
		WalletID: walletID, PlayerID: playerID, RoundID: "round-1", GameID: "game-1",
		Kind: wagertransaction.KindBet, Money: mustParseMoney(t, "25.00"), Now: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("NewExternal: %v", err)
	}

	if err := repo.Insert(ctx, pool, tx); err != nil {
		t.Fatalf("insert: %v", err)
	}

	got, err := repo.GetByID(ctx, pool, tx.ID())
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Status() != wagertransaction.StatusPending {
		t.Errorf("Status() = %s, want PENDING", got.Status())
	}

	byExternal, found, err := repo.GetByProviderAndExternalID(ctx, pool, "provider-a", externalID)
	if err != nil || !found {
		t.Fatalf("GetByProviderAndExternalID: found=%v err=%v", found, err)
	}
	if byExternal.ID() != tx.ID() {
		t.Errorf("GetByProviderAndExternalID returned wrong row")
	}

	// A second attempt to insert the *same* operation (same provider+external
	// id, different idempotency key) must be rejected: the spec forbids
	// reapplying a financial operation identified by (providerId,
	// externalTransactionId) under a different key.
	dup, err := wagertransaction.NewExternal(wagertransaction.NewExternalParams{
		ID: uuid.New(), ProviderID: "provider-a", ExternalTransactionID: externalID,
		IdempotencyKey: "provider-a:different-key", PayloadHash: "hash1",
		WalletID: walletID, PlayerID: playerID, RoundID: "round-1", GameID: "game-1",
		Kind: wagertransaction.KindBet, Money: mustParseMoney(t, "25.00"), Now: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("NewExternal (dup): %v", err)
	}
	if err := repo.Insert(ctx, pool, dup); !errors.Is(err, postgres.ErrIdempotencyConflict) {
		t.Fatalf("expected ErrIdempotencyConflict, got %v", err)
	}
}

func TestWagerTransactionRepository_StatusCAS(t *testing.T) {
	ctx := context.Background()
	dsn := testDSN()
	if err := postgres.RunMigrations(dsn); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	pool, err := postgres.NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer pool.Close()

	repo := postgres.NewWagerTransactionRepository()
	walletID := openTestWallet(ctx, t, pool, "100.00")
	externalID := "ext-cas-" + uuid.NewString()

	tx, err := wagertransaction.NewExternal(wagertransaction.NewExternalParams{
		ID: uuid.New(), ProviderID: "provider-a", ExternalTransactionID: externalID,
		IdempotencyKey: "provider-a:" + externalID, PayloadHash: "hash1",
		WalletID: walletID, PlayerID: uuid.New(), RoundID: "round-1", GameID: "game-1",
		Kind: wagertransaction.KindBet, Money: mustParseMoney(t, "25.00"), Now: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("NewExternal: %v", err)
	}
	if err := repo.Insert(ctx, pool, tx); err != nil {
		t.Fatalf("insert: %v", err)
	}

	processed, err := tx.MarkProcessed(uuid.Nil, mustParseMoney(t, "75.00"), time.Now().UTC())
	if err != nil {
		t.Fatalf("MarkProcessed: %v", err)
	}
	if err := repo.UpdateStatus(ctx, pool, processed, wagertransaction.StatusPending); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}

	// Retrying the same CAS with the now-stale expected previous status
	// must fail: this is the exact mechanism a second worker racing to
	// finish the same transaction would hit.
	if err := repo.UpdateStatus(ctx, pool, processed, wagertransaction.StatusPending); !errors.Is(err, postgres.ErrStatusConflict) {
		t.Fatalf("expected ErrStatusConflict on stale CAS, got %v", err)
	}

	reread, err := repo.GetByID(ctx, pool, tx.ID())
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if reread.Status() != wagertransaction.StatusProcessed {
		t.Errorf("Status() = %s, want PROCESSED", reread.Status())
	}
	balance, ok := reread.ResultBalance()
	if !ok || balance.String() != "75.00" {
		t.Errorf("ResultBalance() = %s, %v; want 75.00, true", balance, ok)
	}
}

func TestInboxRepository_DedupsRedelivery(t *testing.T) {
	ctx := context.Background()
	dsn := testDSN()
	if err := postgres.RunMigrations(dsn); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	pool, err := postgres.NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer pool.Close()

	repo := postgres.NewInboxRepository()
	messageID := uuid.NewString()

	if err := repo.Record(ctx, pool, "wager-consumer", messageID, "hash-abc", time.Now().UTC()); err != nil {
		t.Fatalf("first Record: %v", err)
	}
	if err := repo.Record(ctx, pool, "wager-consumer", messageID, "hash-abc", time.Now().UTC()); !errors.Is(err, postgres.ErrDuplicateMessage) {
		t.Fatalf("redelivery: expected ErrDuplicateMessage, got %v", err)
	}

	hash, found, err := repo.PayloadHash(ctx, pool, "wager-consumer", messageID)
	if err != nil || !found || hash != "hash-abc" {
		t.Fatalf("PayloadHash = %q, %v, %v; want hash-abc, true, nil", hash, found, err)
	}

	if err := repo.MarkCompleted(ctx, pool, "wager-consumer", messageID, time.Now().UTC()); err != nil {
		t.Fatalf("MarkCompleted: %v", err)
	}
}

// TestOutboxRepository_TwoPublishersDoNotDoubleClaim is the mandatory
// scenario: two publisher workers contend for the same outbox rows, and
// each row must be claimed and published exactly once.
func TestOutboxRepository_TwoPublishersDoNotDoubleClaim(t *testing.T) {
	ctx := context.Background()
	dsn := testDSN()
	if err := postgres.RunMigrations(dsn); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	pool, err := postgres.NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer pool.Close()

	repo := postgres.NewOutboxRepository()

	const eventCount = 10
	aggregateID := uuid.New()
	correlationID := uuid.New()
	ourEventIDs := make(map[uuid.UUID]bool, eventCount)
	for i := 0; i < eventCount; i++ {
		env, err := events.NewEnvelope(uuid.New(), events.WagerTransactionProcessedData{
			TransactionID: aggregateID,
			Kind:          wagertransaction.KindBet,
			Money:         mustParseMoney(t, "1.00"),
		}, correlationID, nil, time.Now().UTC())
		if err != nil {
			t.Fatalf("NewEnvelope: %v", err)
		}
		if err := repo.Insert(ctx, pool, env, time.Now().UTC()); err != nil {
			t.Fatalf("insert outbox event %d: %v", i, err)
		}
		ourEventIDs[env.EventID] = true
	}

	// A big enough batch size that any worker could, if the locking were
	// broken, grab the same rows another worker already claimed - and large
	// enough to also sweep up any unpublished rows left behind by an
	// earlier, unrelated test run sharing this long-lived database.
	const batchSize = 200

	claim := func() ([]uuid.UUID, error) {
		tx, err := pool.Begin(ctx)
		if err != nil {
			return nil, err
		}
		defer tx.Rollback(ctx) //nolint:errcheck

		batch, err := repo.ClaimBatch(ctx, tx, time.Now().UTC(), batchSize)
		if err != nil {
			return nil, err
		}
		ids := make([]uuid.UUID, 0, len(batch))
		for _, row := range batch {
			ids = append(ids, row.EventID)
			if err := repo.MarkPublished(ctx, tx, row.EventID, time.Now().UTC()); err != nil {
				return nil, err
			}
		}
		return ids, tx.Commit(ctx)
	}

	var mu sync.Mutex
	claimed := map[uuid.UUID]int{}
	var wg sync.WaitGroup
	for w := 0; w < 3; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 5; i++ {
				ids, err := claim()
				if err != nil {
					t.Errorf("claim: %v", err)
					return
				}
				mu.Lock()
				for _, id := range ids {
					claimed[id]++
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	// Only assert about the events *this test* inserted - the shared
	// database may also hold unpublished rows from other tests/runs, which
	// this same claim loop legitimately sweeps up too.
	for id := range ourEventIDs {
		if claimed[id] != 1 {
			t.Errorf("our event %s claimed %d times, want exactly 1", id, claimed[id])
		}
	}
}
