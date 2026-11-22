-- Dead webhooks are counted for monitoring and listed for replay; keep both
-- cheap when the table is large.
CREATE INDEX webhook_outbox_dead_idx ON webhook_outbox (merchant_id, created_at DESC)
    WHERE dead_at IS NOT NULL;
