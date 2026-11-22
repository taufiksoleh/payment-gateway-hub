-- Merchants may hold several API keys, which makes rotation possible:
-- issue a new key, let the old one expire after a grace period.
CREATE TABLE api_keys (
    id           TEXT PRIMARY KEY,
    merchant_id  TEXT        NOT NULL REFERENCES merchants (id) ON DELETE CASCADE,
    key_hash     TEXT        NOT NULL UNIQUE, -- sha256 of the raw key
    hint         TEXT        NOT NULL DEFAULT '', -- last 4 characters
    created_at   TIMESTAMPTZ NOT NULL,
    expires_at   TIMESTAMPTZ,
    revoked_at   TIMESTAMPTZ,
    last_used_at TIMESTAMPTZ
);

CREATE INDEX api_keys_merchant_idx ON api_keys (merchant_id, created_at DESC);

-- Carry over every existing key so current API keys keep working. Their
-- hints are unknown (only hashes were stored).
INSERT INTO api_keys (id, merchant_id, key_hash, created_at)
SELECT 'key_' || substr(md5(id), 1, 24), id, api_key_hash, created_at
FROM merchants;

ALTER TABLE merchants DROP COLUMN api_key_hash;
