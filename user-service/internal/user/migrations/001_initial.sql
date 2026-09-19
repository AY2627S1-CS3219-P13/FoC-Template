CREATE TABLE users (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email text NOT NULL UNIQUE,
    display_name text NOT NULL CHECK (char_length(display_name) BETWEEN 1 AND 50),
    password_hash text NOT NULL,
    verified_at timestamptz,
    is_admin boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- Pending accounts do not reserve a display name indefinitely.
CREATE UNIQUE INDEX users_active_display_name ON users (lower(display_name))
    WHERE verified_at IS NOT NULL;

CREATE TABLE verification_challenges (
    user_id uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    code_hash bytea NOT NULL,
    registration_hash bytea NOT NULL,
    expires_at timestamptz NOT NULL,
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 5),
    sent_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE sessions (
    token_hash bytea PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sessions_user_id ON sessions(user_id);
CREATE INDEX sessions_expires_at ON sessions(expires_at);

-- Shared by replicas and retained across service restarts.
CREATE TABLE rate_limits (
    key text PRIMARY KEY,
    requests integer NOT NULL,
    expires_at timestamptz NOT NULL
);
CREATE INDEX rate_limits_expires_at ON rate_limits(expires_at);
