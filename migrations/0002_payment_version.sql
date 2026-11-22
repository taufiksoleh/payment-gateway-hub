-- Optimistic locking: every UPDATE must match the version it read and bumps it.
ALTER TABLE payments ADD COLUMN version BIGINT NOT NULL DEFAULT 1;
