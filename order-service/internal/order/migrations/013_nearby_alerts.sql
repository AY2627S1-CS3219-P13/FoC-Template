-- Nearby errand alerts: one preference per courier, their push devices, and the
-- delivery jobs the matching consumer writes for the push sender.
CREATE TABLE nearby_alert_preferences (
    user_id uuid PRIMARY KEY,
    enabled boolean NOT NULL,
    location_id text NOT NULL CHECK (length(location_id) BETWEEN 1 AND 128),
    radius_meters integer NOT NULL CHECK (radius_meters IN (200, 500, 1000)),
    last_alerted_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

-- The endpoint is a secret capability URL: never logged or returned.
CREATE TABLE push_subscriptions (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id uuid NOT NULL,
    endpoint text NOT NULL UNIQUE CHECK (length(endpoint) BETWEEN 1 AND 1024),
    p256dh bytea NOT NULL CHECK (length(p256dh) = 65),
    auth bytea NOT NULL CHECK (length(auth) = 16),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX push_subscriptions_user ON push_subscriptions(user_id, created_at);

CREATE TABLE push_deliveries (
    event_id text NOT NULL REFERENCES order_events(event_id),
    subscription_id bigint NOT NULL REFERENCES push_subscriptions(id) ON DELETE CASCADE,
    order_id text NOT NULL REFERENCES orders(id),
    distance_meters integer NOT NULL CHECK (distance_meters >= 0),
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'sent', 'failed', 'skipped')),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    last_error text CHECK (last_error IN ('push_rejected', 'push_unavailable', 'push_unauthorized')),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (event_id, subscription_id)
);
CREATE INDEX push_deliveries_due ON push_deliveries(next_attempt_at) WHERE status = 'pending';
CREATE INDEX push_deliveries_created ON push_deliveries(created_at) WHERE status <> 'pending';

-- Migration 009 allows only the three original consumers; 009 stays unedited.
ALTER TABLE workflow_receipts DROP CONSTRAINT workflow_receipts_consumer_name_check,
    ADD CONSTRAINT workflow_receipts_consumer_name_check CHECK (consumer_name IN
        ('workflow-timers', 'workflow-settlement', 'workflow-publication', 'workflow-nearby-alerts'));

-- Existing orders never alert: the new consumer starts after every current event.
INSERT INTO workflow_receipts(consumer_name, event_id)
SELECT 'workflow-nearby-alerts', event_id FROM order_events WHERE domain_type IS NOT NULL;
