CREATE TABLE wallet_ledger_entries (
    id                         UUID PRIMARY KEY,
    wallet_id                  UUID NOT NULL REFERENCES wallets (id),
    transaction_id             UUID NOT NULL REFERENCES wager_transactions (id),
    direction                  TEXT NOT NULL,
    amount_minor_units         BIGINT NOT NULL,
    currency                   CHAR(3) NOT NULL,
    balance_before_minor_units BIGINT NOT NULL,
    balance_after_minor_units  BIGINT NOT NULL,
    created_at                 TIMESTAMPTZ NOT NULL,

    CONSTRAINT wallet_ledger_entries_direction_valid CHECK (direction IN ('DEBIT', 'CREDIT')),
    CONSTRAINT wallet_ledger_entries_amount_positive CHECK (amount_minor_units > 0),
    CONSTRAINT wallet_ledger_entries_balance_after_non_negative CHECK (balance_after_minor_units >= 0),
    CONSTRAINT wallet_ledger_entries_balance_invariant CHECK (
        (direction = 'DEBIT' AND balance_after_minor_units = balance_before_minor_units - amount_minor_units)
        OR
        (direction = 'CREDIT' AND balance_after_minor_units = balance_before_minor_units + amount_minor_units)
    )
);

-- Exactly one ledger entry per (wallet, transaction): the schema-level half
-- of "no duplicate movement" (the other half is the app's idempotency check).
CREATE UNIQUE INDEX ux_wallet_ledger_entries_wallet_transaction
    ON wallet_ledger_entries (wallet_id, transaction_id);

CREATE INDEX ix_wallet_ledger_entries_wallet_created
    ON wallet_ledger_entries (wallet_id, created_at, id);
