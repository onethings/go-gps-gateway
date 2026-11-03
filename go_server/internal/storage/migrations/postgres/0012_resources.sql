-- M2-1 resource tables. Every table maps one model type and one REST
-- resource the Java server exposes; the columns are the model fields, the
-- free form settings stay in attributes.
CREATE TABLE IF NOT EXISTS calendars (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT NOT NULL DEFAULT '',
    data       BYTEA,
    attributes JSONB NOT NULL DEFAULT '{}'::jsonb
);

CREATE TABLE IF NOT EXISTS groups (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT NOT NULL DEFAULT '',
    group_id   BIGINT REFERENCES groups (id) ON DELETE SET NULL,
    attributes JSONB NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX IF NOT EXISTS groups_parent_idx ON groups (group_id);

-- Device permissions are granted on groups, so a device points into the tree.
ALTER TABLE devices
    ADD COLUMN IF NOT EXISTS group_id BIGINT REFERENCES groups (id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS devices_group_idx ON devices (group_id);

-- Geofences gain the two Java columns the engine never needed: the calendar
-- that limits when the rule fires and the free text description.
ALTER TABLE geofences
    ADD COLUMN IF NOT EXISTS calendar_id BIGINT REFERENCES calendars (id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS description TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS drivers (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT NOT NULL DEFAULT '',
    unique_id  TEXT NOT NULL DEFAULT '',
    attributes JSONB NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX IF NOT EXISTS drivers_unique_id_idx ON drivers (unique_id);

CREATE TABLE IF NOT EXISTS maintenances (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT NOT NULL DEFAULT '',
    type       TEXT NOT NULL DEFAULT '',
    start      DOUBLE PRECISION NOT NULL DEFAULT 0,
    period     DOUBLE PRECISION NOT NULL DEFAULT 0,
    attributes JSONB NOT NULL DEFAULT '{}'::jsonb
);

CREATE TABLE IF NOT EXISTS orders (
    id           BIGSERIAL PRIMARY KEY,
    unique_id    TEXT NOT NULL DEFAULT '',
    description  TEXT NOT NULL DEFAULT '',
    from_address TEXT NOT NULL DEFAULT '',
    to_address   TEXT NOT NULL DEFAULT '',
    attributes   JSONB NOT NULL DEFAULT '{}'::jsonb
);

CREATE TABLE IF NOT EXISTS reports (
    id          BIGSERIAL PRIMARY KEY,
    calendar_id BIGINT REFERENCES calendars (id) ON DELETE SET NULL,
    type        TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    attributes  JSONB NOT NULL DEFAULT '{}'::jsonb
);

CREATE TABLE IF NOT EXISTS statistics (
    id                   BIGSERIAL PRIMARY KEY,
    capture_time         TIMESTAMPTZ,
    active_users         INTEGER NOT NULL DEFAULT 0,
    active_devices       INTEGER NOT NULL DEFAULT 0,
    requests             INTEGER NOT NULL DEFAULT 0,
    messages_received    INTEGER NOT NULL DEFAULT 0,
    messages_stored      INTEGER NOT NULL DEFAULT 0,
    mail_sent            INTEGER NOT NULL DEFAULT 0,
    sms_sent             INTEGER NOT NULL DEFAULT 0,
    geocoder_requests    INTEGER NOT NULL DEFAULT 0,
    geolocation_requests INTEGER NOT NULL DEFAULT 0,
    protocols            JSONB NOT NULL DEFAULT '{}'::jsonb,
    attributes           JSONB NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX IF NOT EXISTS statistics_capture_time_idx ON statistics (capture_time DESC);

-- Computed attributes carry their settings in dedicated columns, so the table
-- has no attributes column of its own.
CREATE TABLE IF NOT EXISTS attributes (
    id          BIGSERIAL PRIMARY KEY,
    description TEXT NOT NULL DEFAULT '',
    attribute   TEXT NOT NULL DEFAULT '',
    expression  TEXT NOT NULL DEFAULT '',
    type        TEXT NOT NULL DEFAULT '',
    priority    INTEGER NOT NULL DEFAULT 0
);
