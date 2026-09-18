-- Transactional outbox: a row is written in the SAME db transaction as the
-- domain change it announces, and only published afterwards by a separate
-- worker (internal/platform/outbox). This is what guarantees "no event
-- before commit" without a distributed transaction.
CREATE TABLE outbox_events (
    event_id        UUID PRIMARY KEY,
    aggregate_id     UUID NOT NULL,
    event_type        TEXT NOT NULL,
    correlation_id     UUID NOT NULL,
    causation_id        UUID,
    payload              JSONB NOT NULL,
    occurred_at          TIMESTAMPTZ NOT NULL,
    attempts             INT NOT NULL DEFAULT 0,
    next_attempt_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at           TIMESTAMPTZ
);

-- Lets N publisher workers each grab a batch of due, unpublished rows
-- (via SELECT ... FOR UPDATE SKIP LOCKED at query time) without scanning
-- published rows or colliding with each other.
CREATE INDEX ix_outbox_events_pending
    ON outbox_events (next_attempt_at)
    WHERE published_at IS NULL;
