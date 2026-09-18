package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"backend-challenge-go/internal/domain/events"
)

type OutboxRepository struct{}

func NewOutboxRepository() *OutboxRepository {
	return &OutboxRepository{}
}

// Insert appends an outbox row in the same transaction as the domain
// change it announces - this is the mechanic that makes "publish only
// after commit" true: the row exists once the transaction commits, and
// nothing before that point has told anyone outside the transaction.
//
// The stored payload is the *entire* envelope (eventId, eventType,
// aggregateId, correlationId, causationId, occurredAt, version, data), not
// just the inner data - so the publisher worker can send it to SQS
// unmodified, byte for byte, on every attempt including retries.
func (r *OutboxRepository) Insert(ctx context.Context, q Querier, env events.Envelope, occurredAt time.Time) error {
	payload, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("postgres: marshal outbox payload: %w", err)
	}
	_, err = q.Exec(ctx, `
		INSERT INTO outbox_events (event_id, aggregate_id, event_type, correlation_id, causation_id, payload, occurred_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, env.EventID, env.AggregateID, env.EventType, env.CorrelationID, nullCausationID(env.CausationID), payload, occurredAt)
	if err != nil {
		return fmt.Errorf("postgres: insert outbox event: %w", err)
	}
	return nil
}

// OutboxRow is a row read back by the publisher worker: enough to publish
// (event type, aggregate, raw payload) plus the retry bookkeeping fields.
type OutboxRow struct {
	EventID       uuid.UUID
	AggregateID   uuid.UUID
	EventType     string
	CorrelationID uuid.UUID
	CausationID   *uuid.UUID
	Payload       []byte
	OccurredAt    time.Time
	Attempts      int
}

// ClaimBatch locks up to limit unpublished, due rows with
// SELECT ... FOR UPDATE SKIP LOCKED: concurrent publisher workers each get
// a disjoint batch instead of blocking on or duplicating each other's work,
// and a crashed worker's lock is released automatically when its
// connection/transaction ends, so its rows become claimable again with no
// extra bookkeeping. Call within a transaction and commit only after the
// publish attempt (success -> MarkPublished, failure -> ScheduleRetry) -
// holding the row lock for that whole span is what makes "recover abandoned
// work" automatic instead of something a background sweeper has to detect.
func (r *OutboxRepository) ClaimBatch(ctx context.Context, tx pgx.Tx, now time.Time, limit int) ([]OutboxRow, error) {
	rows, err := tx.Query(ctx, `
		SELECT event_id, aggregate_id, event_type, correlation_id, causation_id, payload, occurred_at, attempts
		FROM outbox_events
		WHERE published_at IS NULL AND next_attempt_at <= $1
		ORDER BY next_attempt_at
		LIMIT $2
		FOR UPDATE SKIP LOCKED
	`, now, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: claim outbox batch: %w", err)
	}
	defer rows.Close()

	var out []OutboxRow
	for rows.Next() {
		var row OutboxRow
		var causationID uuid.NullUUID
		if err := rows.Scan(&row.EventID, &row.AggregateID, &row.EventType, &row.CorrelationID, &causationID, &row.Payload, &row.OccurredAt, &row.Attempts); err != nil {
			return nil, fmt.Errorf("postgres: scan outbox row: %w", err)
		}
		if causationID.Valid {
			id := causationID.UUID
			row.CausationID = &id
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: iterate outbox batch: %w", err)
	}
	return out, nil
}

// MarkPublished stamps an event as durably published. Preserving eventId
// across retries (never generating a new one) is what lets a republish be
// recognized as the same logical event downstream.
func (r *OutboxRepository) MarkPublished(ctx context.Context, q Querier, eventID uuid.UUID, publishedAt time.Time) error {
	_, err := q.Exec(ctx, `UPDATE outbox_events SET published_at = $1 WHERE event_id = $2`, publishedAt, eventID)
	if err != nil {
		return fmt.Errorf("postgres: mark outbox event published: %w", err)
	}
	return nil
}

// ScheduleRetry records a failed publish attempt and schedules the next one
// with the backoff the caller computed.
func (r *OutboxRepository) ScheduleRetry(ctx context.Context, q Querier, eventID uuid.UUID, attempts int, nextAttemptAt time.Time) error {
	_, err := q.Exec(ctx, `
		UPDATE outbox_events SET attempts = $1, next_attempt_at = $2 WHERE event_id = $3
	`, attempts, nextAttemptAt, eventID)
	if err != nil {
		return fmt.Errorf("postgres: schedule outbox retry: %w", err)
	}
	return nil
}

func nullCausationID(id *uuid.UUID) any {
	if id == nil {
		return nil
	}
	return *id
}
