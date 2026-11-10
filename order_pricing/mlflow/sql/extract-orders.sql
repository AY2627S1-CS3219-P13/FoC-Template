-- Read-only export of immediate orders whose outcome is final, in the CSV format
-- `python -m pricing_mlops import-observed --orders` reads.
-- No requester/courier IDs, item names or cancellation reasons leave the database.
-- Run once against Order's database, for example:
--   psql -U <user> -d <db> -v ON_ERROR_STOP=1 -f - < extract-orders.sql > orders.csv
BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY;
\copy (SELECT o.id AS order_id, o.created_at, o.expires_at, o.pickup->>'type' AS pickup_type, o.pickup->>'id' AS pickup_id, CASE WHEN o.pickup->>'type' = 'location' THEN o.pickup->>'id' ELSE o.pickup->>'locationId' END AS pickup_location_id, o.delivery->>'type' AS delivery_type, o.delivery->>'id' AS delivery_id, CASE WHEN o.delivery->>'type' = 'location' THEN o.delivery->>'id' ELSE o.delivery->>'locationId' END AS delivery_location_id, (SELECT sum((i->>'quantity')::numeric) FROM jsonb_array_elements(o.items) i) AS total_item_quantity, o.offered_credits, o.status AS final_status, (SELECT min(a.recorded_at) FROM order_actions a WHERE a.order_id = o.id AND a.action = 'accept') AS accepted_at, (SELECT min(a.recorded_at) FROM order_actions a WHERE a.order_id = o.id AND a.action = 'cancel') AS cancelled_at FROM orders o WHERE o.window_start IS NULL AND o.expires_at < now() - interval '1 hour' ORDER BY o.created_at, o.id) TO STDOUT WITH CSV HEADER
ROLLBACK;
