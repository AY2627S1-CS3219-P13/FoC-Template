ALTER TABLE order_events
    ADD COLUMN domain_type text,
    ADD COLUMN domain_schema_version integer,
    ADD COLUMN domain_payload jsonb,
    ADD CONSTRAINT order_domain_presence CHECK (
        (domain_type IS NULL AND domain_schema_version IS NULL AND domain_payload IS NULL)
        OR (domain_type IS NOT NULL AND domain_schema_version IS NOT NULL
            AND domain_schema_version > 0 AND domain_payload IS NOT NULL
            AND jsonb_typeof(domain_payload)='object'));
CREATE INDEX order_domain_events ON order_events(order_id,order_version) WHERE domain_type IS NOT NULL;

CREATE TABLE workflow_receipts (
    consumer_name text NOT NULL CHECK (consumer_name IN ('workflow-timers','workflow-settlement','workflow-publication')),
    event_id text NOT NULL REFERENCES order_events(event_id),
    processed_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (consumer_name,event_id)
);
CREATE TABLE workflow_retries (
    consumer_name text NOT NULL,
    event_id text NOT NULL REFERENCES order_events(event_id),
    attempts integer NOT NULL CHECK (attempts > 0),
    next_attempt_at timestamptz NOT NULL,
    error_category text NOT NULL CHECK (error_category IN ('invalid_domain_event','effect_failed')),
    PRIMARY KEY (consumer_name,event_id)
);
-- Tombstones survive removal of a schedule and fence older events/bootstrap.
CREATE TABLE workflow_projections (
    order_id text PRIMARY KEY REFERENCES orders(id),
    order_version bigint NOT NULL CHECK (order_version > 0),
    bootstrap_version bigint
);
CREATE TABLE workflow_timers (
    order_id text NOT NULL REFERENCES orders(id),
    timer_kind text NOT NULL CHECK (timer_kind IN ('expiry','completion')),
    due_at timestamptz NOT NULL,
    source_event_id text REFERENCES order_events(event_id),
    source_order_version bigint NOT NULL,
    attempts integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT '-infinity',
    error_category text CHECK (error_category IN ('timer_failed')),
    PRIMARY KEY (order_id,timer_kind)
);
CREATE INDEX workflow_timers_due ON workflow_timers(due_at,next_attempt_at);
ALTER TABLE order_refunds ADD COLUMN attempts integer NOT NULL DEFAULT 0,
    ADD COLUMN error_category text;
ALTER TABLE order_payouts ADD COLUMN attempts integer NOT NULL DEFAULT 0,
    ADD COLUMN error_category text;
