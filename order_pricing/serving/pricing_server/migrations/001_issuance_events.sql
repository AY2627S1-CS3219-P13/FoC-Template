CREATE TABLE IF NOT EXISTS pricing_quotes (
    caller text NOT NULL,
    idempotency_key uuid NOT NULL,
    fingerprint text NOT NULL,
    response jsonb NOT NULL,
    first_received_at timestamptz NOT NULL,
    PRIMARY KEY (caller, idempotency_key)
);

-- Positions are allocated under the transaction-scoped feed lock, held through
-- COMMIT. A sequence alone would expose holes behind an exported cursor.
CREATE TABLE IF NOT EXISTS pricing_events (
    position bigint PRIMARY KEY,
    source text NOT NULL,
    event_id uuid NOT NULL,
    payload jsonb NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (source, event_id)
);
