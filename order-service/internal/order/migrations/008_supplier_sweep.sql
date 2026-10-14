ALTER TABLE orders ADD COLUMN supplier_checked_at timestamptz NOT NULL DEFAULT '-infinity';
CREATE INDEX orders_supplier_sweep ON orders(supplier_checked_at,id) WHERE status IN ('open','accepted','picked up');
