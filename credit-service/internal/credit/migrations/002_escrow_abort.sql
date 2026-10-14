CREATE TABLE escrow_aborts (order_id text PRIMARY KEY CHECK (length(order_id) BETWEEN 1 AND 128), created_at timestamptz NOT NULL DEFAULT now());
