-- One durable claim per database, including installations that already have an admin.
CREATE TABLE admin_bootstrap (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    consumed_at timestamptz,
    claimed_by uuid REFERENCES users(id) ON DELETE SET NULL
);

INSERT INTO admin_bootstrap(singleton, consumed_at)
SELECT true, CASE WHEN EXISTS (SELECT 1 FROM users WHERE is_admin) THEN now() ELSE NULL END;
