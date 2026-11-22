-- Merchants list their webhooks newest first (GET /v1/webhooks).
CREATE INDEX webhook_outbox_merchant_created_idx ON webhook_outbox (merchant_id, created_at DESC, id DESC);
