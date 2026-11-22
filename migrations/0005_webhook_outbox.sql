-- Transactional outbox for merchant webhooks (see internal/outbox).
-- Rows are inserted in the same transaction as the payment change.
CREATE TABLE webhook_outbox (
    id              TEXT PRIMARY KEY, -- event id, also inside payload
    merchant_id     TEXT        NOT NULL REFERENCES merchants (id) ON DELETE CASCADE,
    payment_id      TEXT        NOT NULL REFERENCES payments (id) ON DELETE CASCADE,
    type            TEXT        NOT NULL,
    payload         BYTEA       NOT NULL, -- exact body bytes; BYTEA keeps them byte-for-byte
    attempts        INT         NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL,
    locked_until    TIMESTAMPTZ,          -- claimed by a dispatcher until then
    last_error      TEXT        NOT NULL DEFAULT '',
    delivered_at    TIMESTAMPTZ,
    dead_at         TIMESTAMPTZ,          -- gave up after too many attempts
    created_at      TIMESTAMPTZ NOT NULL
);

-- The dispatcher's query: undelivered, not dead, due.
CREATE INDEX webhook_outbox_due_idx ON webhook_outbox (next_attempt_at)
    WHERE delivered_at IS NULL AND dead_at IS NULL;
