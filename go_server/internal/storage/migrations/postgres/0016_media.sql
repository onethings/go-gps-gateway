-- Media files a device uploaded. The bytes stay on disk; the row is the index
-- the REST resource and the event rule read.
CREATE TABLE IF NOT EXISTS media (
    id         BIGSERIAL PRIMARY KEY,
    device_id  BIGINT NOT NULL REFERENCES devices (id) ON DELETE CASCADE,
    file_name  TEXT NOT NULL UNIQUE,
    type       TEXT NOT NULL DEFAULT 'image'
               CHECK (type IN ('image', 'video', 'audio')),
    size       BIGINT NOT NULL DEFAULT 0,
    media_time TIMESTAMPTZ NOT NULL DEFAULT now(),
    attributes JSONB NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX IF NOT EXISTS media_device_idx ON media (device_id, media_time DESC);
