-- Serialize publication positions, not creation transactions. A sequence alone
-- would let a cursor skip an earlier allocation whose transaction commits late.
CREATE TABLE order_publication_head (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    position bigint NOT NULL CHECK (position >= 0)
);
INSERT INTO order_publication_head(position) VALUES (0);

CREATE TABLE order_publications (
    position bigint PRIMARY KEY CHECK (position > 0),
    event_id text NOT NULL UNIQUE REFERENCES order_events(event_id),
    published_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX order_creation_events ON order_events(created_at,event_id)
    WHERE event_type='OrderCreated';
