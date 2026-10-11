ALTER TABLE orders DROP CONSTRAINT orders_status_check;
ALTER TABLE orders ADD CONSTRAINT orders_status_check CHECK
    (status IN ('open','accepted','picked up','delivered','dispute','completed','cancelled','expired','not completed'));
ALTER TABLE orders ADD COLUMN courier_id uuid;
ALTER TABLE orders ADD COLUMN version bigint NOT NULL DEFAULT 1 CHECK (version > 0);
ALTER TABLE orders ADD COLUMN cancellation_reason text NOT NULL DEFAULT '';
ALTER TABLE orders ADD CONSTRAINT orders_courier_not_requester CHECK (courier_id <> requester_id);
ALTER TABLE orders ADD CONSTRAINT orders_assigned_status CHECK
    (status NOT IN ('accepted','picked up','delivered','dispute','completed','not completed') OR courier_id IS NOT NULL);
-- Assignment history stays on the order. Only these two states occupy a slot.
CREATE UNIQUE INDEX orders_one_courier_slot ON orders(courier_id)
    WHERE status IN ('accepted','picked up');
CREATE INDEX orders_requester_created ON orders(requester_id,created_at DESC,id);
CREATE INDEX orders_open_created ON orders(created_at DESC,id) WHERE status = 'open';

-- A committed event journal/outbox. Browser consumers read it through authorized
-- HTTP; a future broker publisher can use the stable event ID for deduplication.
CREATE TABLE order_events (
    event_id text PRIMARY KEY,
    order_id text NOT NULL REFERENCES orders(id),
    order_version bigint NOT NULL,
    event_type text NOT NULL,
    schema_version integer NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE(order_id,order_version)
);

-- Cancellation and its refund intent commit together. Credit remains the owner
-- of balances; releasing an escrow is idempotent across worker restarts.
CREATE TABLE order_refunds (
    order_id text PRIMARY KEY REFERENCES orders(id),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
