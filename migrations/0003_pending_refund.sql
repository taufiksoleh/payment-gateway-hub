-- Amount reserved by refunds in flight at the provider. Reserved before the
-- provider call so concurrent refunds cannot refund the same money twice.
ALTER TABLE payments
    ADD COLUMN pending_refund_amount BIGINT NOT NULL DEFAULT 0
        CHECK (pending_refund_amount >= 0);
