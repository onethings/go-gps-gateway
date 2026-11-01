-- The session manager records what the gateway knows between frames: whether a
-- device holds a connection. 'unknown' is the value a device starts with, which
-- is what the Java server stores for a device that has never connected.
ALTER TABLE devices
    ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'unknown';

CREATE INDEX IF NOT EXISTS devices_status_idx ON devices (status);

-- events is what the Java server keeps in tc_events: one row per thing that
-- happened, linked to the position that caused it when there was one. The
-- alerts table stays for now and is folded into this one by the next migration.
CREATE TABLE IF NOT EXISTS events (
    id          BIGSERIAL PRIMARY KEY,
    device_id   BIGINT NOT NULL REFERENCES devices (id) ON DELETE CASCADE,
    position_id BIGINT REFERENCES positions (id) ON DELETE SET NULL,
    geofence_id BIGINT,
    type        TEXT NOT NULL,
    event_time  TIMESTAMPTZ NOT NULL,
    attributes  JSONB NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX IF NOT EXISTS events_device_time_idx ON events (device_id, event_time DESC);

CREATE INDEX IF NOT EXISTS events_time_idx ON events (event_time DESC);

CREATE INDEX IF NOT EXISTS events_type_time_idx ON events (type, event_time DESC);
