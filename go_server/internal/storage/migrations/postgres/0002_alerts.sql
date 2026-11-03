CREATE TABLE IF NOT EXISTS geofences (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT NOT NULL DEFAULT '',
    area       TEXT NOT NULL,
    attributes JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS alerts (
    id          BIGSERIAL PRIMARY KEY,
    device_id   BIGINT NOT NULL REFERENCES devices (id) ON DELETE CASCADE,
    position_id BIGINT REFERENCES positions (id) ON DELETE SET NULL,
    type        TEXT NOT NULL,
    alert_time  TIMESTAMPTZ NOT NULL,
    geofence_id BIGINT,
    attributes  JSONB NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX IF NOT EXISTS alerts_device_time_idx ON alerts (device_id, alert_time DESC);

CREATE INDEX IF NOT EXISTS alerts_time_idx ON alerts (alert_time DESC);

CREATE INDEX IF NOT EXISTS alerts_type_time_idx ON alerts (type, alert_time DESC);
