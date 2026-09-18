CREATE TABLE wager_transactions (
    id                                 UUID PRIMARY KEY,
    kind                               TEXT NOT NULL,
    provider_id                        TEXT,
    external_transaction_id           TEXT,
    idempotency_key                    TEXT,
    payload_hash                       TEXT,
    wallet_id                          UUID NOT NULL REFERENCES wallets (id),
    player_id                          UUID NOT NULL,
    round_id                           TEXT,
    game_id                            TEXT,
    amount_minor_units                 BIGINT NOT NULL,
    currency                           CHAR(3) NOT NULL,
    reference_external_transaction_id  TEXT,
    resolved_reference_transaction_id  UUID REFERENCES wager_transactions (id),
    status                             TEXT NOT NULL,
    failure_code                       TEXT,
    result_balance_minor_units         BIGINT,
    attempts                           INT NOT NULL DEFAULT 0,
    next_retry_at                      TIMESTAMPTZ,
    created_at                         TIMESTAMPTZ NOT NULL,
    updated_at                         TIMESTAMPTZ NOT NULL,

    CONSTRAINT wager_transactions_kind_valid CHECK (
        kind IN ('OPENING', 'BET', 'WIN', 'LOSS', 'REFUND', 'ROLLBACK')
    ),
    CONSTRAINT wager_transactions_status_valid CHECK (
        status IN ('PENDING', 'PENDING_REFERENCE', 'PROCESSED', 'REJECTED', 'FAILED')
    ),
    CONSTRAINT wager_transactions_currency_format CHECK (currency ~ '^[A-Z]{3}$'),

    -- OPENING never carries external-origin metadata; every external kind requires all of it.
    CONSTRAINT wager_transactions_origin_metadata CHECK (
        (kind = 'OPENING'
            AND provider_id IS NULL AND external_transaction_id IS NULL
            AND idempotency_key IS NULL AND payload_hash IS NULL
            AND round_id IS NULL AND game_id IS NULL)
        OR
        (kind <> 'OPENING'
            AND provider_id IS NOT NULL AND external_transaction_id IS NOT NULL
            AND idempotency_key IS NOT NULL AND payload_hash IS NOT NULL
            AND round_id IS NOT NULL AND game_id IS NOT NULL)
    ),

    -- LOSS is always exactly zero; every other kind is strictly positive.
    CONSTRAINT wager_transactions_amount_by_kind CHECK (
        (kind = 'LOSS' AND amount_minor_units = 0)
        OR (kind <> 'LOSS' AND amount_minor_units > 0)
    ),

    -- REFUND/ROLLBACK must reference the transaction they reverse; BET/LOSS/OPENING never do.
    CONSTRAINT wager_transactions_reference_by_kind CHECK (
        (kind IN ('REFUND', 'ROLLBACK') AND reference_external_transaction_id IS NOT NULL)
        OR (kind IN ('BET', 'LOSS', 'OPENING') AND reference_external_transaction_id IS NULL)
        OR (kind = 'WIN')
    ),

    CONSTRAINT wager_transactions_failure_code_on_terminal_failure CHECK (
        (status IN ('REJECTED', 'FAILED') AND failure_code IS NOT NULL)
        OR (status NOT IN ('REJECTED', 'FAILED'))
    ),

    CONSTRAINT wager_transactions_result_balance_on_processed CHECK (
        (status = 'PROCESSED' AND result_balance_minor_units IS NOT NULL)
        OR (status <> 'PROCESSED')
    )
);

-- One external operation, one row: an HTTP/SQS replay must find this row
-- instead of inserting a duplicate movement.
CREATE UNIQUE INDEX ux_wager_transactions_provider_external
    ON wager_transactions (provider_id, external_transaction_id)
    WHERE kind <> 'OPENING';

-- A given idempotency key always maps to the same operation - the server
-- never silently substitutes a different key for the same content.
CREATE UNIQUE INDEX ux_wager_transactions_provider_idempotency_key
    ON wager_transactions (provider_id, idempotency_key)
    WHERE kind <> 'OPENING';

-- At most one OPENING per wallet: schema-level guard against duplicate initial credit.
CREATE UNIQUE INDEX ux_wager_transactions_opening_per_wallet
    ON wager_transactions (wallet_id)
    WHERE kind = 'OPENING';

CREATE INDEX ix_wager_transactions_wallet ON wager_transactions (wallet_id);

-- Lets the reference-retry worker find due PENDING_REFERENCE rows without a table scan.
CREATE INDEX ix_wager_transactions_pending_reference_retry
    ON wager_transactions (next_retry_at)
    WHERE status = 'PENDING_REFERENCE';
