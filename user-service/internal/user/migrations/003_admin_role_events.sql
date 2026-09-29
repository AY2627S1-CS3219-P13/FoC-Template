-- Keep role history even if an account is later removed. This table has no public writes.
CREATE TABLE admin_role_events (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    actor_kind text NOT NULL CHECK (actor_kind IN ('bootstrap', 'admin', 'operator')),
    actor_user_id uuid,
    actor_label text NOT NULL CHECK (char_length(actor_label) BETWEEN 3 AND 100),
    target_user_id uuid NOT NULL,
    target_email text NOT NULL,
    previous_role text NOT NULL CHECK (previous_role IN ('user', 'admin')),
    new_role text NOT NULL CHECK (new_role IN ('user', 'admin')),
    reason text NOT NULL DEFAULT '' CHECK (char_length(reason) <= 200),
    occurred_at timestamptz NOT NULL DEFAULT now(),
    CHECK (previous_role <> new_role),
    CHECK ((actor_kind = 'operator' AND actor_user_id IS NULL AND char_length(reason) >= 3)
        OR (actor_kind <> 'operator' AND actor_user_id IS NOT NULL))
);
CREATE INDEX admin_role_events_recent ON admin_role_events (occurred_at DESC, id DESC);
