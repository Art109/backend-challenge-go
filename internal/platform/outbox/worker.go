// Package outbox runs the background worker that publishes staged domain
// events: the other half of the transactional outbox pattern, separate
// from internal/platform/postgres.OutboxRepository (which only reads/writes
// the table) and from internal/app (which only stages rows inside its own
// transactions, never publishes).
package outbox

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"backend-challenge-go/internal/platform/postgres"
	"backend-challenge-go/internal/platform/sqs"
)

// Worker polls for due, unpublished outbox rows and publishes them. Running
// several instances of it concurrently (multiple processes, or several
// goroutines within one) is safe and intended - see OutboxRepository.ClaimBatch
// for how they avoid duplicating or blocking on each other's work.
type Worker struct {
	pool      *pgxpool.Pool
	repo      *postgres.OutboxRepository
	publisher *sqs.Publisher
	interval  time.Duration
	batchSize int
}

func NewWorker(pool *pgxpool.Pool, repo *postgres.OutboxRepository, publisher *sqs.Publisher) *Worker {
	return &Worker{
		pool:      pool,
		repo:      repo,
		publisher: publisher,
		interval:  2 * time.Second,
		batchSize: 25,
	}
}

// Run polls until ctx is cancelled. Each tick drains every currently-due
// row (looping publishBatch until it claims nothing), rather than
// publishing only one batch per tick, so a backlog doesn't fall further
// behind while waiting for the next tick.
func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.drain(ctx)
		}
	}
}

func (w *Worker) drain(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		claimed, err := w.publishBatch(ctx)
		if err != nil {
			slog.Error("outbox_publish_batch_failed", "error", err)
			return
		}
		if claimed == 0 {
			return
		}
	}
}

// publishBatch claims up to batchSize due rows, attempts to publish each,
// and commits the whole batch's outcome (published or rescheduled with
// backoff) in one transaction - the same transaction that holds the
// FOR UPDATE SKIP LOCKED claim, so the claim and its outcome are atomic.
func (w *Worker) publishBatch(ctx context.Context) (int, error) {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	rows, err := w.repo.ClaimBatch(ctx, tx, time.Now().UTC(), w.batchSize)
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, tx.Commit(ctx)
	}

	for _, row := range rows {
		err := w.publisher.Publish(ctx, row.AggregateID.String(), row.EventID.String(), string(row.Payload))
		if err != nil {
			attempts := row.Attempts + 1
			slog.Warn("outbox_publish_failed", "eventId", row.EventID, "attempts", attempts, "error", err)
			if err := w.repo.ScheduleRetry(ctx, tx, row.EventID, attempts, time.Now().UTC().Add(backoff(attempts))); err != nil {
				return 0, err
			}
			continue
		}
		if err := w.repo.MarkPublished(ctx, tx, row.EventID, time.Now().UTC()); err != nil {
			return 0, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(rows), nil
}

func backoff(attempts int) time.Duration {
	d := time.Second
	for i := 0; i < attempts; i++ {
		d *= 2
	}
	if d > 5*time.Minute {
		d = 5 * time.Minute
	}
	return d
}
