-- A PostgreSQL dump of the Traccar Java schema, cut down to the tables the
-- gateway touches: the device and position tables as the reference changelogs
-- leave them after 6.17, their two reference foreign keys, and a little history.
--
--   psql "$DSN" -f scripts/reference-schema.sql
--
-- compat-e2e.sh loads this and then starts the gateway with -db-schema
-- reference, so the run proves the attach path against a real Java database.

DROP TABLE IF EXISTS tc_positions;
DROP TABLE IF EXISTS tc_devices;
DROP TABLE IF EXISTS tc_groups;
DROP TABLE IF EXISTS tc_calendars;

CREATE TABLE tc_calendars (
    id         SERIAL PRIMARY KEY,
    name       VARCHAR(128) NOT NULL,
    data       TEXT NOT NULL
);

CREATE TABLE tc_groups (
    id       SERIAL PRIMARY KEY,
    name     VARCHAR(128) NOT NULL,
    groupid  INT REFERENCES tc_groups (id) ON DELETE SET NULL
);

CREATE TABLE tc_devices (
    id             SERIAL PRIMARY KEY,
    name           VARCHAR(128) NOT NULL,
    uniqueid       VARCHAR(128) NOT NULL UNIQUE,
    lastupdate     TIMESTAMP,
    positionid     BIGINT,
    groupid        INT,
    attributes     VARCHAR(4000),
    phone          VARCHAR(128),
    model          VARCHAR(128),
    contact        VARCHAR(512),
    category       VARCHAR(128),
    disabled       BOOLEAN DEFAULT false,
    status         CHAR(8),
    expirationtime TIMESTAMP,
    calendarid     INT,
    motionstreak   BOOLEAN DEFAULT false,
    motionlatitude   DOUBLE PRECISION DEFAULT 0,
    motionlongitude  DOUBLE PRECISION DEFAULT 0
);

CREATE TABLE tc_positions (
    id          SERIAL PRIMARY KEY,
    protocol    VARCHAR(128),
    deviceid    INT NOT NULL REFERENCES tc_devices (id) ON DELETE CASCADE,
    servertime  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    devicetime  TIMESTAMP NOT NULL,
    fixtime     TIMESTAMP NOT NULL,
    valid       BOOLEAN NOT NULL,
    latitude    DOUBLE PRECISION NOT NULL,
    longitude   DOUBLE PRECISION NOT NULL,
    altitude    FLOAT NOT NULL,
    speed       FLOAT NOT NULL,
    course      FLOAT NOT NULL,
    address     VARCHAR(512),
    attributes  VARCHAR(4000),
    accuracy    DOUBLE PRECISION NOT NULL DEFAULT 0,
    network     VARCHAR(4000),
    geofenceids VARCHAR(128)
);

ALTER TABLE tc_devices
    ADD CONSTRAINT fk_devices_groupid FOREIGN KEY (groupid) REFERENCES tc_groups (id) ON DELETE SET NULL;
ALTER TABLE tc_devices
    ADD CONSTRAINT fk_devices_calendarid FOREIGN KEY (calendarid) REFERENCES tc_calendars (id) ON DELETE SET NULL;

CREATE INDEX position_deviceid_fixtime ON tc_positions (deviceid, fixtime);
CREATE INDEX idx_devices_uniqueid ON tc_devices (uniqueid);

INSERT INTO tc_groups (id, name) VALUES (1, 'Fleet');

INSERT INTO tc_devices
    (id, name, uniqueid, lastupdate, groupid, attributes, phone, model, contact, category, disabled, status)
VALUES
    (1, 'Truck 1', '862170041234567', '2026-10-31 12:00:35', 1,
     '{"fuel":"diesel","speedLimit":90}', '+37060000001', 'GT06N', 'Disponent', 'truck', false, 'online'),
    (2, 'Van 2', '862170041234568', '2026-10-30 09:15:05', NULL,
     NULL, NULL, 'GT06N', NULL, 'van', true, 'offline');

INSERT INTO tc_positions
    (protocol, deviceid, servertime, devicetime, fixtime, valid, latitude, longitude,
     altitude, speed, course, address, attributes, accuracy, network, geofenceids)
VALUES
    ('gt06', 1, '2026-10-31 12:00:05', '2026-10-31 12:00:00', '2026-10-31 12:00:00',
     true, 54.94535, 24.01762, 100, 27, 180, 'Vilnius', '{"sat":9,"ignition":true}', 5, NULL, '3,4'),
    ('gt06', 1, '2026-10-31 12:00:35', '2026-10-31 12:00:30', '2026-10-31 12:00:30',
     true, 54.94600, 24.01800, 101, 28, 181, 'Vilnius', '{"sat":8}', 6, NULL, NULL),
    ('gt06', 2, '2026-10-30 09:15:05', '2026-10-30 09:15:00', '2026-10-30 09:15:00',
     true, 54.90000, 23.90000, 90, 0, 0, 'Kaunas', NULL, 10, NULL, '');

UPDATE tc_devices SET positionid = (SELECT max(id) FROM tc_positions WHERE deviceid = 1) WHERE id = 1;
