CREATE TABLE schedules (
 id text PRIMARY KEY,
 requester_id uuid NOT NULL,
 idempotency_key text NOT NULL,
 fingerprint text NOT NULL,
 request jsonb NOT NULL,
 order_data jsonb,
 state text NOT NULL CHECK(state IN ('pending','reserving','scheduled','transfer_pending','transferred','cancelled','expired','abandoned')),
 courier_id uuid,
 transfer_id text,
 generation bigint NOT NULL DEFAULT 0,
 refund_pending boolean NOT NULL DEFAULT false,
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(requester_id,idempotency_key),
 CHECK(courier_id IS NULL OR courier_id<>requester_id)
);
CREATE INDEX schedules_work ON schedules(updated_at,id) WHERE state<>'transferred';
