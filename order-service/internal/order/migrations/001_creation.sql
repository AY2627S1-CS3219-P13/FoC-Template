CREATE TABLE creation_attempts (
    requester_id uuid NOT NULL,
    idempotency_key text NOT NULL,
    order_id text NOT NULL UNIQUE,
    request_fingerprint text NOT NULL,
    state text NOT NULL CHECK (state IN ('pending', 'completed', 'release_pending', 'abandoned')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (requester_id, idempotency_key),
    CHECK (length(idempotency_key) BETWEEN 1 AND 128),
    CHECK (length(order_id) BETWEEN 1 AND 128)
);

CREATE TABLE orders (
    id text PRIMARY KEY REFERENCES creation_attempts(order_id),
    requester_id uuid NOT NULL,
    pickup jsonb NOT NULL,
    delivery jsonb NOT NULL,
    items jsonb NOT NULL,
    offered_credits bigint NOT NULL CHECK (offered_credits > 0),
    expires_at timestamptz NOT NULL,
    status text NOT NULL DEFAULT 'open' CHECK (status = 'open'),
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (jsonb_array_length(items) > 0),
    CONSTRAINT orders_expiry_after_creation CHECK (expires_at > created_at)
);

CREATE INDEX orders_requester_created_idx ON orders(requester_id, created_at DESC);
CREATE INDEX creation_attempts_recovery_idx ON creation_attempts(updated_at)
    WHERE state IN ('pending', 'release_pending');
