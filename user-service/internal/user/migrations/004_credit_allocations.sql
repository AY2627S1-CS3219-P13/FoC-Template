-- New verifications enqueue an allocation in the same transaction as activation.
-- Deliberately do not backfill previously verified users in this development rollout.
CREATE TABLE credit_allocations (
    user_id uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    attempts integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    lease_until timestamptz
);
CREATE INDEX credit_allocations_pending ON credit_allocations (next_attempt_at)
    WHERE completed_at IS NULL;
