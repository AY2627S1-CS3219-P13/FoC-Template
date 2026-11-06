-- One rate-limit bucket per authenticated user, shared by Order replicas.
CREATE TABLE pricing_quote_limits (
    user_id uuid PRIMARY KEY,
    window_start timestamptz NOT NULL,
    requests bigint NOT NULL CHECK (requests > 0)
);
