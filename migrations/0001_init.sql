-- Initial schema for payment-gateway-hub.
-- Migrations are applied in file-name order by internal/storage/postgres.Migrate.

CREATE TABLE merchants (
    id             TEXT PRIMARY KEY,
    name           TEXT        NOT NULL,
    api_key_hash   TEXT        NOT NULL UNIQUE,
    webhook_url    TEXT        NOT NULL DEFAULT '',
    webhook_secret TEXT        NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL,
    updated_at     TIMESTAMPTZ NOT NULL
);

-- Secrets are AES-256-GCM encrypted JSON (see internal/platform/secret).
CREATE TABLE gateway_credentials (
    merchant_id TEXT        NOT NULL REFERENCES merchants (id) ON DELETE CASCADE,
    provider    TEXT        NOT NULL,
    environment TEXT        NOT NULL,
    secrets     BYTEA       NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (merchant_id, provider)
);

CREATE TABLE payments (
    id              TEXT PRIMARY KEY,
    merchant_id     TEXT        NOT NULL REFERENCES merchants (id),
    order_id        TEXT        NOT NULL,
    provider        TEXT        NOT NULL,
    provider_ref    TEXT        NOT NULL DEFAULT '',
    method          TEXT        NOT NULL,
    channel         TEXT        NOT NULL,
    amount          BIGINT      NOT NULL CHECK (amount > 0),
    currency        TEXT        NOT NULL,
    refunded_amount BIGINT      NOT NULL DEFAULT 0,
    status          TEXT        NOT NULL,
    failure_reason  TEXT        NOT NULL DEFAULT '',
    description     TEXT        NOT NULL DEFAULT '',
    customer_name   TEXT        NOT NULL DEFAULT '',
    customer_email  TEXT        NOT NULL DEFAULT '',
    customer_phone  TEXT        NOT NULL DEFAULT '',
    action          JSONB       NOT NULL DEFAULT '{}',
    expires_at      TIMESTAMPTZ,
    paid_at         TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL,
    updated_at      TIMESTAMPTZ NOT NULL,
    UNIQUE (merchant_id, order_id)
);

CREATE INDEX payments_merchant_created_idx ON payments (merchant_id, created_at DESC);
