-- Adopt a Traccar Java database in place. The two reference tables keep their
-- rows and take the names and columns the Go schema uses, so every Go query
-- and the migrations that follow run against them unchanged. Runs on the first
-- start with database.schema=reference; the guard in compat.go makes it a
-- no-op afterwards.

ALTER TABLE tc_devices RENAME TO devices;

-- Columns the reference added over its lifetime; the older a database is, the
-- more of these it lacks.
ALTER TABLE devices ADD COLUMN IF NOT EXISTS status         TEXT;
ALTER TABLE devices ADD COLUMN IF NOT EXISTS expirationtime TIMESTAMP;
ALTER TABLE devices ADD COLUMN IF NOT EXISTS calendarid     INT;

ALTER TABLE devices RENAME COLUMN uniqueid       TO unique_id;
ALTER TABLE devices RENAME COLUMN lastupdate     TO last_update;
ALTER TABLE devices RENAME COLUMN groupid        TO group_id;
ALTER TABLE devices RENAME COLUMN positionid     TO position_id;
ALTER TABLE devices RENAME COLUMN expirationtime TO expiration_time;
ALTER TABLE devices RENAME COLUMN calendarid     TO calendar_id;

-- These two reference foreign keys point at tc_groups and tc_calendars, which
-- the Go schema does not use; the zero id the Go columns default to would
-- violate them.
ALTER TABLE devices DROP CONSTRAINT IF EXISTS fk_devices_groupid;
ALTER TABLE devices DROP CONSTRAINT IF EXISTS fk_devices_calendarid;

ALTER TABLE devices ADD COLUMN IF NOT EXISTS protocol   TEXT;
ALTER TABLE devices ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT now();

-- The gateway registers a device with the identifier only, so every NOT NULL
-- column it leaves out needs the reference schema's missing default.
ALTER TABLE devices ALTER COLUMN name SET DEFAULT '';

UPDATE devices SET status = 'unknown' WHERE status IS NULL OR btrim(status) = '';
ALTER TABLE devices ALTER COLUMN status TYPE TEXT;
ALTER TABLE devices ALTER COLUMN status SET DEFAULT 'unknown';
ALTER TABLE devices ALTER COLUMN status SET NOT NULL;

UPDATE devices SET disabled = false WHERE disabled IS NULL;
ALTER TABLE devices ALTER COLUMN disabled SET DEFAULT false;
ALTER TABLE devices ALTER COLUMN disabled SET NOT NULL;

UPDATE devices SET calendar_id = 0 WHERE calendar_id IS NULL;
ALTER TABLE devices ALTER COLUMN calendar_id SET DEFAULT 0;
ALTER TABLE devices ALTER COLUMN calendar_id SET NOT NULL;

UPDATE devices SET position_id = 0 WHERE position_id IS NULL;
ALTER TABLE devices ALTER COLUMN position_id SET DEFAULT 0;
ALTER TABLE devices ALTER COLUMN position_id SET NOT NULL;

UPDATE devices SET attributes = '{}' WHERE attributes IS NULL OR btrim(attributes) = '';
ALTER TABLE devices ALTER COLUMN attributes TYPE JSONB USING attributes::jsonb;
ALTER TABLE devices ALTER COLUMN attributes SET DEFAULT '{}'::jsonb;
ALTER TABLE devices ALTER COLUMN attributes SET NOT NULL;

ALTER TABLE tc_positions RENAME TO positions;

ALTER TABLE positions ADD COLUMN IF NOT EXISTS geofenceids VARCHAR(128);

ALTER TABLE positions RENAME COLUMN deviceid   TO device_id;
ALTER TABLE positions RENAME COLUMN devicetime TO device_time;
ALTER TABLE positions RENAME COLUMN servertime TO server_time;

UPDATE positions SET protocol = '' WHERE protocol IS NULL;
ALTER TABLE positions ALTER COLUMN protocol SET DEFAULT '';
ALTER TABLE positions ALTER COLUMN protocol SET NOT NULL;

-- The reference column is a comma separated list; the Go schema stores a JSON
-- array. Both shapes are accepted, so a database written by either server
-- converts.
UPDATE positions SET geofenceids = NULL WHERE btrim(geofenceids) = '';
ALTER TABLE positions ALTER COLUMN geofenceids TYPE JSONB
    USING CASE
        WHEN geofenceids IS NULL THEN '[]'::jsonb
        WHEN btrim(geofenceids) LIKE '[%' THEN btrim(geofenceids)::jsonb
        ELSE to_jsonb(string_to_array(btrim(geofenceids), ',')::bigint[])
    END;
ALTER TABLE positions ALTER COLUMN geofenceids SET DEFAULT '[]'::jsonb;
ALTER TABLE positions ALTER COLUMN geofenceids SET NOT NULL;

UPDATE positions SET attributes = '{}' WHERE attributes IS NULL OR btrim(attributes) = '';
ALTER TABLE positions ALTER COLUMN attributes TYPE JSONB USING attributes::jsonb;
ALTER TABLE positions ALTER COLUMN attributes SET DEFAULT '{}'::jsonb;
ALTER TABLE positions ALTER COLUMN attributes SET NOT NULL;

-- The gateway has no separate fix time: the device time is it. Filling the
-- reference column keeps the reports that read fixtime correct.
CREATE OR REPLACE FUNCTION positions_fixtime_from_device_time() RETURNS trigger
AS 'BEGIN IF NEW.fixtime IS NULL THEN NEW.fixtime := NEW.device_time; END IF; RETURN NEW; END'
LANGUAGE plpgsql;

CREATE TRIGGER positions_fixtime_from_device_time BEFORE INSERT ON positions
FOR EACH ROW EXECUTE PROCEDURE positions_fixtime_from_device_time();
