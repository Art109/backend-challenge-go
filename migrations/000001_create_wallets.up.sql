CREATE TABLE wallets (
    id                  UUID PRIMARY KEY,
    player_id           UUID NOT NULL,
    currency            CHAR(3) NOT NULL,
    balance_minor_units BIGINT NOT NULL,
    version             BIGINT NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL,
    updated_at          TIMESTAMPTZ NOT NULL,

    CONSTRAINT wallets_balance_non_negative CHECK (balance_minor_units >= 0),
    CONSTRAINT wallets_version_positive CHECK (version >= 1),
    CONSTRAINT wallets_currency_format CHECK (currency ~ '^[A-Z]{3}$')
);

-- The pair (playerId, currency) identifies a single wallet.
CREATE UNIQUE INDEX ux_wallets_player_currency ON wallets (player_id, currency);
