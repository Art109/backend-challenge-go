// Package referenceretry runs the background worker that re-evaluates
// REFUND/ROLLBACK transactions parked in PENDING_REFERENCE - the durable
// retry-with-backoff the spec requires for a reversal whose reference
// hasn't arrived yet, including after the whole application restarts (the
// work is driven entirely from the database, not in-memory state).
package referenceretry

import (
	"context"
	"log/slog"
	"time"

	"backend-challenge-go/internal/app"
)

type Worker struct {
	uc        *app.UseCases
	interval  time.Duration
	batchSize int
}

func NewWorker(uc *app.UseCases) *Worker {
	return &Worker{uc: uc, interval: 5 * time.Second, batchSize: 50}
}

func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.tick(ctx)
		}
	}
}

func (w *Worker) tick(ctx context.Context) {
	ids, err := w.uc.DuePendingReferenceIDs(ctx, time.Now().UTC(), w.batchSize)
	if err != nil {
		slog.Error("reference_retry_list_failed", "error", err)
		return
	}
	for _, id := range ids {
		if err := w.uc.RetryPendingReference(ctx, id); err != nil {
			slog.Warn("reference_retry_failed", "transactionId", id, "error", err)
		}
	}
}
