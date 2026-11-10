-- Nearby alerts allow up to three preferences per courier, each with its own
-- on/off switch and two-minute cooldown. A campus location appears at most once
-- per courier. The existing preference becomes the courier's first.
ALTER TABLE nearby_alert_preferences DROP CONSTRAINT nearby_alert_preferences_pkey;
ALTER TABLE nearby_alert_preferences
    ADD COLUMN position smallint NOT NULL DEFAULT 1 CHECK (position BETWEEN 1 AND 3),
    ADD PRIMARY KEY (user_id, position),
    ADD CONSTRAINT nearby_alert_preferences_user_location UNIQUE (user_id, location_id);
ALTER TABLE nearby_alert_preferences ALTER COLUMN position DROP DEFAULT;

-- The notification names the courier's alert location that matched. Jobs queued
-- before this migration have none and keep the original wording.
ALTER TABLE push_deliveries ADD COLUMN alert_location text NOT NULL DEFAULT '';
