package wagertransaction

import (
	"time"

	"github.com/google/uuid"

	"backend-challenge-go/internal/domain/money"
)

// MarkProcessed completes a PENDING or PENDING_REFERENCE transaction. For
// REFUND/ROLLBACK, resolvedReferenceID is the internal id the external
// reference resolved to (uuid.Nil for kinds that don't reference anything).
// resultBalance is snapshotted so a later idempotent replay returns exactly
// what the original caller saw, regardless of subsequent wallet activity.
func (t WagerTransaction) MarkProcessed(resolvedReferenceID uuid.UUID, resultBalance money.Money, now time.Time) (WagerTransaction, error) {
	if err := t.ensureNotTerminal(); err != nil {
		return WagerTransaction{}, err
	}
	updated := t
	updated.status = StatusProcessed
	updated.resolvedReferenceID = resolvedReferenceID
	updated.resultBalance = &resultBalance
	updated.updatedAt = now
	return updated, nil
}

// MarkPendingReference records that this transaction is waiting on a
// REFUND/ROLLBACK reference that hasn't arrived yet. Only valid as the
// first transition out of PENDING - once already PENDING_REFERENCE, use
// IncrementAttempt for subsequent retries instead.
func (t WagerTransaction) MarkPendingReference(now time.Time) (WagerTransaction, error) {
	if t.status != StatusPending {
		return WagerTransaction{}, ErrInvalidTransition
	}
	updated := t
	updated.status = StatusPendingReference
	updated.updatedAt = now
	return updated, nil
}

// IncrementAttempt bumps the retry counter for a transaction stuck in
// PENDING_REFERENCE and schedules the next backoff attempt. It does not
// change status: exhausting retries is a separate, explicit call to
// MarkRejected.
func (t WagerTransaction) IncrementAttempt(nextRetryAt time.Time, now time.Time) (WagerTransaction, error) {
	if t.status != StatusPendingReference {
		return WagerTransaction{}, ErrInvalidTransition
	}
	updated := t
	updated.attempts = t.attempts + 1
	updated.nextRetryAt = &nextRetryAt
	updated.updatedAt = now
	return updated, nil
}

// MarkRejected terminates a PENDING or PENDING_REFERENCE transaction with a
// definitive business rejection (e.g. insufficient balance, reference
// exhausted its retry budget).
func (t WagerTransaction) MarkRejected(failureCode string, now time.Time) (WagerTransaction, error) {
	if failureCode == "" {
		return WagerTransaction{}, ErrMissingFailureCode
	}
	if err := t.ensureNotTerminal(); err != nil {
		return WagerTransaction{}, err
	}
	updated := t
	updated.status = StatusRejected
	updated.failureCode = failureCode
	updated.updatedAt = now
	return updated, nil
}

// MarkFailed terminates a PENDING transaction with a permanent
// infrastructure failure recorded for audit purposes (distinct from a
// business rejection).
func (t WagerTransaction) MarkFailed(failureCode string, now time.Time) (WagerTransaction, error) {
	if failureCode == "" {
		return WagerTransaction{}, ErrMissingFailureCode
	}
	if err := t.ensureNotTerminal(); err != nil {
		return WagerTransaction{}, err
	}
	updated := t
	updated.status = StatusFailed
	updated.failureCode = failureCode
	updated.updatedAt = now
	return updated, nil
}
