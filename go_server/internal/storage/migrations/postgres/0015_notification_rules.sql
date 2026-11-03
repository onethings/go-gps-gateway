-- M4-3 notification rules: a preset can be global (always), carry the
-- filters the Java NotificationManager applies (alarms, geofenceIds in
-- attributes), reference a calendar and a saved command, and priority as an
-- attribute. Existing presets become global so an upgrade does not silently
-- stop delivering what already worked; new presets default to linked-only.
ALTER TABLE notifications
    ADD COLUMN IF NOT EXISTS always BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS command_id BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS calendar_id BIGINT REFERENCES calendars (id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS attributes JSONB NOT NULL DEFAULT '{}'::jsonb;

UPDATE notifications SET always = true;
