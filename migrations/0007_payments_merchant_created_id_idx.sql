-- GET /v1/payments pages by (created_at, id) newest first; include id so the
-- keyset comparison and tie-break are served by the index.
CREATE INDEX payments_merchant_created_id_idx ON payments (merchant_id, created_at DESC, id DESC);
DROP INDEX payments_merchant_created_idx;
