ALTER TABLE orders ADD COLUMN window_start timestamptz;
ALTER TABLE orders ADD CONSTRAINT orders_delivery_window CHECK(window_start IS NULL OR expires_at=window_start+interval '30 minutes');
DROP INDEX orders_one_courier_slot;
CREATE INDEX orders_courier_work ON orders(courier_id) WHERE status IN ('accepted','picked up');
CREATE FUNCTION enforce_courier_capacity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.status IN ('accepted','picked up') AND (TG_OP='INSERT' OR OLD.status NOT IN ('accepted','picked up') OR OLD.courier_id IS DISTINCT FROM NEW.courier_id) THEN
  PERFORM pg_advisory_xact_lock(hashtextextended(NEW.courier_id::text,321913));
  IF (SELECT count(*) FROM orders WHERE courier_id=NEW.courier_id AND status IN ('accepted','picked up') AND id<>NEW.id)>=3 THEN
   RAISE EXCEPTION 'courier capacity reached' USING ERRCODE='23505',CONSTRAINT='orders_one_courier_slot';
  END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER courier_capacity BEFORE INSERT OR UPDATE OF status,courier_id ON orders FOR EACH ROW EXECUTE FUNCTION enforce_courier_capacity();
CREATE TABLE scheduled_imports (
 transfer_id text PRIMARY KEY,
 fingerprint text NOT NULL,
 order_id text NOT NULL,
 outcome text NOT NULL CHECK(outcome IN ('accepted','rejected')),
 reason text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
