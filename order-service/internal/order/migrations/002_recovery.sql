-- Preserve the exact financial request before the first remote side effect.
ALTER TABLE creation_attempts ADD COLUMN offered_credits bigint CHECK (offered_credits > 0);
ALTER TABLE creation_attempts DROP CONSTRAINT creation_attempts_state_check;
ALTER TABLE creation_attempts ADD CONSTRAINT creation_attempts_state_check
    CHECK (state IN ('pending', 'reserving', 'completed', 'release_pending', 'abandoned'));
DROP INDEX creation_attempts_recovery_idx;
CREATE INDEX creation_attempts_recovery_idx ON creation_attempts(updated_at)
    WHERE state IN ('pending', 'reserving', 'release_pending');

-- now() is the transaction start, which may precede slow dependency calls.
ALTER TABLE orders ALTER COLUMN created_at SET DEFAULT clock_timestamp();
