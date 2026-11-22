-- Requests sent with an Idempotency-Key header (see internal/idempotency).
CREATE TABLE idempotency_keys (
    merchant_id   TEXT        NOT NULL REFERENCES merchants (id) ON DELETE CASCADE,
    key           TEXT        NOT NULL,
    request_hash  TEXT        NOT NULL,
    token         TEXT        NOT NULL,
    status_code   INT         NOT NULL DEFAULT 0, -- 0 = still in progress
    response_body BYTEA,
    created_at    TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (merchant_id, key)
);

-- For purging expired keys.
CREATE INDEX idempotency_keys_created_idx ON idempotency_keys (created_at);
