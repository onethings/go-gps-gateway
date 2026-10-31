CREATE TABLE IF NOT EXISTS devices (
    id            BIGSERIAL PRIMARY KEY,
    unique_id     TEXT NOT NULL UNIQUE,
    name          TEXT NOT NULL DEFAULT '',
    model         TEXT,
    protocol      TEXT,
    attributes    JSONB NOT NULL DEFAULT '{}'::jsonb,
    last_update   TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS positions (
    id            BIGSERIAL PRIMARY KEY,
    device_id     BIGINT NOT NULL REFERENCES devices (id) ON DELETE CASCADE,
    protocol      TEXT NOT NULL,
    device_time   TIMESTAMPTZ NOT NULL,
    server_time   TIMESTAMPTZ NOT NULL DEFAULT now(),
    valid         BOOLEAN NOT NULL DEFAULT false,
    latitude      DOUBLE PRECISION,
    longitude     DOUBLE PRECISION,
    altitude      DOUBLE PRECISION,
    speed         DOUBLE PRECISION,
    course        DOUBLE PRECISION,
    accuracy      DOUBLE PRECISION,
    attributes    JSONB NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX IF NOT EXISTS positions_device_time_idx ON positions (device_id, device_time DESC);

CREATE INDEX IF NOT EXISTS positions_time_idx ON positions (device_time DESC);

CREATE INDEX IF NOT EXISTS positions_attributes_idx ON positions USING GIN (attributes);
