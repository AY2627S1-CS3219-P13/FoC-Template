CREATE TABLE locations (
    id text PRIMARY KEY,
    name text NOT NULL UNIQUE
);

CREATE TABLE suppliers (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    category text NOT NULL CHECK (category IN ('food','printing','retail','services','other')),
    location_id text NOT NULL REFERENCES locations(id),
    description text NOT NULL DEFAULT '' CHECK (char_length(description) <= 500),
    active boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX suppliers_active_name_location
    ON suppliers (lower(name), location_id) WHERE active;
