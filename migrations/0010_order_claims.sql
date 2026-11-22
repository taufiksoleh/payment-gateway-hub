-- Short-lived claims so concurrent creates for one order call the provider
-- only once. A claim past expires_at belongs to a request that gave up and
-- may be taken over.
CREATE TABLE order_claims (
    merchant_id TEXT        NOT NULL,
    order_id    TEXT        NOT NULL,
    expires_at  TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (merchant_id, order_id)
);
