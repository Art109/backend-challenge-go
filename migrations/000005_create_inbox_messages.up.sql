-- SQS message deduplication. (consumerName, messageId) is unique per spec:
-- the same envelope messageId, redelivered to the same consumer, is
-- recognized instead of reprocessed.
CREATE TABLE inbox_messages (
    consumer_name TEXT NOT NULL,
    message_id    TEXT NOT NULL,
    payload_hash  TEXT NOT NULL,
    received_at   TIMESTAMPTZ NOT NULL,
    completed_at  TIMESTAMPTZ,

    PRIMARY KEY (consumer_name, message_id)
);
