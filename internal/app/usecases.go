package app

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"backend-challenge-go/internal/domain/events"
	"backend-challenge-go/internal/platform/postgres"
)

// Clock is injected instead of calling time.Now() directly, so tests can
// control "now" deterministically (e.g. to force a PENDING_REFERENCE row's
// backoff to already be due).
type Clock func() time.Time

// UseCases wires the domain packages to the postgres repositories. Every
// exported method here is one atomic, complete operation: it opens its own
// database transaction (or transactions, across optimistic-concurrency
// retries) and either commits everything or nothing.
type UseCases struct {
	pool       *pgxpool.Pool
	walletRepo *postgres.WalletRepository
	ledgerRepo *postgres.LedgerRepository
	txRepo     *postgres.WagerTransactionRepository
	outboxRepo *postgres.OutboxRepository
	inboxRepo  *postgres.InboxRepository
	now        Clock
}

func NewUseCases(
	pool *pgxpool.Pool,
	walletRepo *postgres.WalletRepository,
	ledgerRepo *postgres.LedgerRepository,
	txRepo *postgres.WagerTransactionRepository,
	outboxRepo *postgres.OutboxRepository,
	inboxRepo *postgres.InboxRepository,
	now Clock,
) *UseCases {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &UseCases{
		pool:       pool,
		walletRepo: walletRepo,
		ledgerRepo: ledgerRepo,
		txRepo:     txRepo,
		outboxRepo: outboxRepo,
		inboxRepo:  inboxRepo,
		now:        now,
	}
}

// stageEvent wraps payload in an envelope and appends it to the outbox
// within the caller's in-flight transaction - it never publishes anything
// itself. Only the separate outbox worker (internal/platform/outbox) reads
// these rows and actually sends them, and only after this transaction has
// committed. This is the entire "publish after commit" guarantee: nothing
// outside this transaction can observe the event before commit, because
// nothing outside this transaction can see this row before commit.
func (uc *UseCases) stageEvent(ctx context.Context, q postgres.Querier, payload events.Event, correlationID uuid.UUID, causationID *uuid.UUID, now time.Time) error {
	env, err := events.NewEnvelope(uuid.New(), payload, correlationID, causationID, now)
	if err != nil {
		return fmt.Errorf("app: build event envelope: %w", err)
	}
	if err := uc.outboxRepo.Insert(ctx, q, env, now); err != nil {
		return fmt.Errorf("app: stage outbox event: %w", err)
	}
	return nil
}
