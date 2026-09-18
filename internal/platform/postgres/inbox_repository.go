package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrDuplicateMessage means (consumerName, messageId) was already recorded
// - the caller must not reprocess this message's business effect, only
// confirm it's already handled and let the SQS delivery be acknowledged.
var ErrDuplicateMessage = errors.New("postgres: message already recorded in inbox")

type InboxRepository struct{}

func NewInboxRepository() *InboxRepository {
	return &InboxRepository{}
}

// Record inserts the inbox row for a freshly-seen message, in the same
// transaction as the domain change and outbox rows it causes. Returns
// ErrDuplicateMessage if this (consumerName, messageId) was already seen -
// this is the primary SQS dedup mechanism, checked before any domain logic
// runs so a redelivery never reaches the use case at all.
func (r *InboxRepository) Record(ctx context.Context, q Querier, consumerName, messageID, payloadHash string, receivedAt time.Time) error {
	_, err := q.Exec(ctx, `
		INSERT INTO inbox_messages (consumer_name, message_id, payload_hash, received_at)
		VALUES ($1, $2, $3, $4)
	`, consumerName, messageID, payloadHash, receivedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrDuplicateMessage
		}
		return fmt.Errorf("postgres: record inbox message: %w", err)
	}
	return nil
}

// MarkCompleted stamps the durable-completion timestamp once the message's
// handling has committed. Called in the same transaction as Record when the
// operation completes synchronously, or in a later transaction when a
// PENDING_REFERENCE resolution finishes asynchronously.
func (r *InboxRepository) MarkCompleted(ctx context.Context, q Querier, consumerName, messageID string, completedAt time.Time) error {
	_, err := q.Exec(ctx, `
		UPDATE inbox_messages SET completed_at = $1
		WHERE consumer_name = $2 AND message_id = $3
	`, completedAt, consumerName, messageID)
	if err != nil {
		return fmt.Errorf("postgres: mark inbox message completed: %w", err)
	}
	return nil
}

// PayloadHash returns the hash recorded for a (consumerName, messageId)
// pair, and whether a row exists at all - used to verify a redelivered
// message's content matches what was originally recorded.
func (r *InboxRepository) PayloadHash(ctx context.Context, q Querier, consumerName, messageID string) (string, bool, error) {
	var hash string
	err := q.QueryRow(ctx, `
		SELECT payload_hash FROM inbox_messages WHERE consumer_name = $1 AND message_id = $2
	`, consumerName, messageID).Scan(&hash)
	if err != nil {
		if isNoRows(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("postgres: read inbox payload hash: %w", err)
	}
	return hash, true, nil
}
