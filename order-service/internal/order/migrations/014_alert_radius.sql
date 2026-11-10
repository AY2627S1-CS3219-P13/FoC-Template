-- Nearby-alert radius options become 0 m (the chosen campus location and the
-- suppliers registered there), 100 m and 250 m. Across the 15 seeded campus
-- locations these cover on average 1, 1.8 and 3.8 locations (7%, 12% and 25%).
-- Existing preferences move to 250 m, the widest new option.
ALTER TABLE nearby_alert_preferences DROP CONSTRAINT nearby_alert_preferences_radius_meters_check;
UPDATE nearby_alert_preferences SET radius_meters = 250, updated_at = clock_timestamp();
ALTER TABLE nearby_alert_preferences ADD CONSTRAINT nearby_alert_preferences_radius_meters_check
    CHECK (radius_meters IN (0, 100, 250));
