CREATE TABLE schedule_events (
 event_id text PRIMARY KEY,
 schedule_id text NOT NULL REFERENCES schedules(id),
 event_type text NOT NULL CHECK(event_type='ScheduledOrderAcceptanceRequested'),
 schema_version integer NOT NULL DEFAULT 1 CHECK(schema_version=1),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE schedule_receipts (
 consumer_name text NOT NULL CHECK(consumer_name='workflow-schedule-acceptance'),
 event_id text NOT NULL REFERENCES schedule_events(event_id),
 outcome text NOT NULL CHECK(outcome IN ('accepted','rejected')),
 processed_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(consumer_name,event_id)
);
CREATE TABLE schedule_retries (
 event_id text PRIMARY KEY REFERENCES schedule_events(event_id),
 attempts integer NOT NULL CHECK(attempts>0),
 next_attempt_at timestamptz NOT NULL,
 error_category text NOT NULL CHECK(error_category='import_failed')
);
-- Resume acceptance attempts carried over from the independent runtime.
INSERT INTO schedule_events(event_id,schedule_id,event_type)
 SELECT transfer_id,id,'ScheduledOrderAcceptanceRequested' FROM schedules WHERE state='transfer_pending';
