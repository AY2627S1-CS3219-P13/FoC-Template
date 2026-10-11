ALTER TABLE order_refunds ADD COLUMN last_attempt_at timestamptz NOT NULL DEFAULT '-infinity';
CREATE INDEX order_refunds_retry_order ON order_refunds(last_attempt_at,created_at);
