-- The refunds behind pending_refund_amount, each with the id sent to the
-- provider, so the reconciler can resend them safely.
ALTER TABLE payments ADD COLUMN pending_refunds JSONB NOT NULL DEFAULT '[]';

CREATE INDEX payments_pending_refunds_idx ON payments (updated_at) WHERE pending_refund_amount > 0;
