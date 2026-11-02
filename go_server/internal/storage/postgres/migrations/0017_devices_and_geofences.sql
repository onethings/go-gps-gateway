-- Device fields the Java model has and the REST resource accepts, plus the
-- geofence ids the geofence stage writes onto a position.

ALTER TABLE devices
    ADD COLUMN IF NOT EXISTS phone           TEXT,
    ADD COLUMN IF NOT EXISTS contact         TEXT,
    ADD COLUMN IF NOT EXISTS category        TEXT,
    ADD COLUMN IF NOT EXISTS disabled        BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS expiration_time TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS calendar_id     BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS position_id     BIGINT NOT NULL DEFAULT 0;

-- JSON encoded id list, like the attributes column, so the driver can scan it
-- with database/sql.
ALTER TABLE positions
    ADD COLUMN IF NOT EXISTS geofenceids JSONB NOT NULL DEFAULT '[]'::jsonb;
