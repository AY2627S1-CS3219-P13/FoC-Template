ALTER TABLE suppliers
    ADD COLUMN opening_hours text NOT NULL DEFAULT '' CHECK (char_length(opening_hours) <= 200),
    ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now(),
    ADD COLUMN deleted_at timestamptz,
    ADD CONSTRAINT deleted_supplier_inactive CHECK (deleted_at IS NULL OR NOT active);

CREATE INDEX suppliers_catalogue_name ON suppliers (lower(name), id) WHERE deleted_at IS NULL;
CREATE INDEX suppliers_catalogue_category ON suppliers (category) WHERE deleted_at IS NULL;
CREATE INDEX suppliers_catalogue_location ON suppliers (location_id) WHERE deleted_at IS NULL;
