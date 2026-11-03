-- M3-4 administrative resources: protocol settings, the server settings row
-- and the audit trail, plus the free form user attributes the web interface
-- stores on an account (terms acceptance, per-user notification overrides).

CREATE TABLE IF NOT EXISTS protocol_settings (
    id        BIGSERIAL PRIMARY KEY,
    protocol  TEXT NOT NULL UNIQUE,
    port      INTEGER NOT NULL DEFAULT 0,
    transport TEXT NOT NULL DEFAULT 'tcp'
);

CREATE TABLE IF NOT EXISTS server_settings (
    id                BIGSERIAL PRIMARY KEY,
    registration      BOOLEAN NOT NULL DEFAULT false,
    readonly          BOOLEAN NOT NULL DEFAULT false,
    device_readonly   BOOLEAN NOT NULL DEFAULT false,
    limit_commands    BOOLEAN NOT NULL DEFAULT false,
    disable_reports   BOOLEAN NOT NULL DEFAULT false,
    fixed_email       BOOLEAN NOT NULL DEFAULT false,
    force_settings    BOOLEAN NOT NULL DEFAULT false,
    announcement      TEXT NOT NULL DEFAULT '',
    map               TEXT NOT NULL DEFAULT '',
    map_url           TEXT NOT NULL DEFAULT '',
    overlay_url       TEXT NOT NULL DEFAULT '',
    bing_key          TEXT NOT NULL DEFAULT '',
    poi_layer         TEXT NOT NULL DEFAULT '',
    coordinate_format TEXT NOT NULL DEFAULT '',
    latitude          DOUBLE PRECISION NOT NULL DEFAULT 0,
    longitude         DOUBLE PRECISION NOT NULL DEFAULT 0,
    zoom              INTEGER NOT NULL DEFAULT 0,
    attributes        JSONB NOT NULL DEFAULT '{}'::jsonb
);

INSERT INTO server_settings (id) SELECT 1 WHERE NOT EXISTS (SELECT 1 FROM server_settings);

CREATE TABLE IF NOT EXISTS actions (
    id          BIGSERIAL PRIMARY KEY,
    action_time TIMESTAMPTZ NOT NULL DEFAULT now(),
    address     TEXT NOT NULL DEFAULT '',
    user_id     BIGINT NOT NULL DEFAULT 0,
    action_type TEXT NOT NULL,
    object_type TEXT NOT NULL DEFAULT '',
    object_id   BIGINT NOT NULL DEFAULT 0,
    attributes  JSONB NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX IF NOT EXISTS actions_time_idx ON actions (action_time);

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS attributes JSONB NOT NULL DEFAULT '{}'::jsonb;
