-- Successful command identity survives later cancellation/expiry and restart.
-- Its insertion is atomic with the status event; failed actions leave no receipt.
CREATE TABLE order_actions (
    order_id text NOT NULL REFERENCES orders(id),
    action text NOT NULL CHECK (action IN ('accept','pick_up','deliver','cancel','confirm','dispute','resolve_completed','resolve_not_completed')),
    actor_id uuid NOT NULL,
    received_at timestamptz NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    order_version bigint NOT NULL,
    PRIMARY KEY (order_id, action, actor_id)
);

ALTER TABLE orders ADD COLUMN delivered_at timestamptz;
ALTER TABLE orders ADD COLUMN completion_requested_at timestamptz;
-- Older delivered rows lack an exact delivery timestamp. Use their delivery
-- status event where available; never restart the five-minute window on boot.
UPDATE orders SET delivered_at=COALESCE(
 (SELECT max(created_at) FROM order_events WHERE order_id=orders.id AND event_type='OrderStatusChanged'),created_at)
 WHERE status IN ('delivered','dispute','completed','not completed');
CREATE INDEX orders_delivery_deadline ON orders(delivered_at) WHERE status='delivered';
CREATE TABLE order_payouts (
 order_id text PRIMARY KEY REFERENCES orders(id),
 courier_id uuid NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 last_attempt_at timestamptz NOT NULL DEFAULT '-infinity'
);
