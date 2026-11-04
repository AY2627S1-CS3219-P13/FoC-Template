ALTER TABLE orders
    ADD COLUMN delivery_photo text NOT NULL DEFAULT '',
    ADD COLUMN dispute_description text NOT NULL DEFAULT '',
    ADD COLUMN dispute_photo text NOT NULL DEFAULT '',
    ADD COLUMN resolution_explanation text NOT NULL DEFAULT '';
