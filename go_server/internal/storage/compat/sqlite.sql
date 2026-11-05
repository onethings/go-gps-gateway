-- Adopt a Traccar Java database in place, the SQLite way: the reference tables
-- are copied into the Go shape, because SQLite cannot change a column's type or
-- drop a NOT NULL constraint. Columns the reference has and the Go schema does
-- not (fixtime, address, network) are kept so historical rows lose nothing.
--
-- The reference tables are renamed in dependency order and dropped child first:
-- renaming tc_devices makes SQLite rewrite the foreign key that tc_positions
-- holds, so dropping the parent before the child would cascade the rows away.

ALTER TABLE tc_positions RENAME TO tc_positions_reference;
ALTER TABLE tc_devices RENAME TO tc_devices_reference;

CREATE TABLE devices (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    unique_id       TEXT NOT NULL UNIQUE,
    name            TEXT NOT NULL DEFAULT '',
    model           TEXT,
    protocol        TEXT,
    attributes      TEXT NOT NULL DEFAULT '{}',
    last_update     DATETIME,
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    status          TEXT NOT NULL DEFAULT 'unknown',
    group_id        INTEGER,
    phone           TEXT,
    contact         TEXT,
    category        TEXT,
    disabled        INTEGER NOT NULL DEFAULT 0,
    expiration_time DATETIME,
    calendar_id     INTEGER NOT NULL DEFAULT 0,
    position_id     INTEGER NOT NULL DEFAULT 0
);

INSERT INTO devices (
    id, unique_id, name, model, attributes, last_update, created_at, status,
    group_id, phone, contact, category, disabled, expiration_time, calendar_id,
    position_id
)
SELECT id, uniqueid, name, model,
       COALESCE(NULLIF(trim(attributes), ''), '{}'),
       lastupdate, CURRENT_TIMESTAMP, COALESCE(NULLIF(trim(status), ''), 'unknown'),
       groupid, phone, contact, category, COALESCE(disabled, 0), expirationtime,
       COALESCE(calendarid, 0), COALESCE(positionid, 0)
FROM tc_devices_reference;

CREATE TABLE positions (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    device_id   INTEGER NOT NULL REFERENCES devices (id) ON DELETE CASCADE,
    protocol    TEXT NOT NULL DEFAULT '',
    device_time DATETIME NOT NULL,
    server_time DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    valid       INTEGER NOT NULL DEFAULT 0,
    latitude    REAL,
    longitude   REAL,
    altitude    REAL,
    speed       REAL,
    course      REAL,
    accuracy    REAL,
    geofenceids TEXT NOT NULL DEFAULT '[]',
    attributes  TEXT NOT NULL DEFAULT '{}',
    fixtime     DATETIME,
    address     TEXT,
    network     TEXT
);

INSERT INTO positions (
    id, device_id, protocol, device_time, server_time, valid, latitude,
    longitude, altitude, speed, course, accuracy, geofenceids, attributes,
    fixtime, address, network
)
SELECT id, deviceid, COALESCE(protocol, ''), devicetime, servertime, valid,
       latitude, longitude, altitude, speed, course, COALESCE(accuracy, 0),
       CASE
           WHEN trim(COALESCE(geofenceids, '')) = '' THEN '[]'
           WHEN trim(geofenceids) LIKE '[%' THEN trim(geofenceids)
           ELSE '[' || trim(geofenceids) || ']'
       END,
       COALESCE(NULLIF(trim(attributes), ''), '{}'),
       fixtime, address, network
FROM tc_positions_reference;

DROP TABLE tc_positions_reference;
DROP TABLE tc_devices_reference;

CREATE INDEX IF NOT EXISTS devices_status_idx ON devices (status);
CREATE INDEX IF NOT EXISTS devices_group_idx ON devices (group_id);
CREATE INDEX IF NOT EXISTS positions_device_time_idx ON positions (device_id, device_time DESC);
CREATE INDEX IF NOT EXISTS positions_time_idx ON positions (device_time DESC);
