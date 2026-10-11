-- Admission is not authorization or a queued command. No credentials or command
-- body are stored, and abandoned admissions can never be executed by a worker.
-- No order FK: admitting a request must not wait for the order's row lock.
CREATE TABLE order_admissions (
    ticket bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    order_id text NOT NULL CHECK (length(order_id) BETWEEN 1 AND 128),
    received_at timestamptz NOT NULL,
    valid_until timestamptz NOT NULL DEFAULT clock_timestamp()+interval '30 seconds'
);
CREATE INDEX order_admissions_order ON order_admissions(order_id,ticket);
CREATE INDEX order_admissions_expiry ON order_admissions(valid_until);
