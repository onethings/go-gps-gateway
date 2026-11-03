-- Initial SQLite schema. SQLite is the embedded option that replaces H2 from
-- the Java server: no external service, no cgo. JSON values are stored as TEXT
-- and timestamps as UTC DATETIME text.
CREATE TABLE IF NOT EXISTS calendars (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT NOT NULL DEFAULT '',
    data       BLOB,
    attributes TEXT
);

CREATE TABLE IF NOT EXISTS device_groups (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT NOT NULL DEFAULT '',
    group_id   INTEGER REFERENCES device_groups (id) ON DELETE SET NULL,
    attributes TEXT
);
CREATE INDEX IF NOT EXISTS device_groups_parent_idx ON device_groups (group_id);

CREATE TABLE IF NOT EXISTS users (
    id                    INTEGER PRIMARY KEY AUTOINCREMENT,
    email                 TEXT NOT NULL UNIQUE,
    name                  TEXT NOT NULL DEFAULT '',
    admin                 INTEGER NOT NULL DEFAULT 0,
    created_at            DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    disabled              INTEGER NOT NULL DEFAULT 0,
    password_hash         TEXT,
    failed_login_attempts INTEGER NOT NULL DEFAULT 0,
    locked_until          DATETIME,
    temporary             INTEGER NOT NULL DEFAULT 0,
    attributes            TEXT,
    totp_key              TEXT,
    totp_enabled          INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS devices (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    unique_id       TEXT NOT NULL UNIQUE,
    name            TEXT NOT NULL DEFAULT '',
    model           TEXT,
    protocol        TEXT,
    attributes      TEXT,
    last_update     DATETIME,
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    status          TEXT NOT NULL DEFAULT 'unknown',
    group_id        INTEGER REFERENCES device_groups (id) ON DELETE SET NULL,
    phone           TEXT,
    contact         TEXT,
    category        TEXT,
    disabled        INTEGER NOT NULL DEFAULT 0,
    expiration_time DATETIME,
    calendar_id     INTEGER NOT NULL DEFAULT 0,
    position_id     INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS devices_status_idx ON devices (status);
CREATE INDEX IF NOT EXISTS devices_group_idx ON devices (group_id);

CREATE TABLE IF NOT EXISTS positions (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    device_id   INTEGER NOT NULL REFERENCES devices (id) ON DELETE CASCADE,
    protocol    TEXT NOT NULL,
    device_time DATETIME NOT NULL,
    server_time DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    valid       INTEGER NOT NULL DEFAULT 0,
    latitude    REAL,
    longitude   REAL,
    altitude    REAL,
    speed       REAL,
    course      REAL,
    accuracy    REAL,
    attributes  TEXT,
    geofenceids TEXT
);
CREATE INDEX IF NOT EXISTS positions_device_time_idx ON positions (device_id, device_time DESC);
CREATE INDEX IF NOT EXISTS positions_time_idx ON positions (device_time DESC);

CREATE TABLE IF NOT EXISTS events (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    device_id   INTEGER NOT NULL REFERENCES devices (id) ON DELETE CASCADE,
    position_id INTEGER REFERENCES positions (id) ON DELETE SET NULL,
    geofence_id INTEGER,
    type        TEXT NOT NULL,
    event_time  DATETIME NOT NULL,
    attributes  TEXT
);
CREATE INDEX IF NOT EXISTS events_device_time_idx ON events (device_id, event_time DESC);
CREATE INDEX IF NOT EXISTS events_time_idx ON events (event_time DESC);
CREATE INDEX IF NOT EXISTS events_type_time_idx ON events (type, event_time DESC);

CREATE TABLE IF NOT EXISTS geofences (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT NOT NULL DEFAULT '',
    area        TEXT NOT NULL,
    attributes  TEXT,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    calendar_id INTEGER REFERENCES calendars (id) ON DELETE SET NULL,
    description TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS drivers (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT NOT NULL DEFAULT '',
    unique_id  TEXT NOT NULL DEFAULT '',
    attributes TEXT
);
CREATE INDEX IF NOT EXISTS drivers_unique_id_idx ON drivers (unique_id);

CREATE TABLE IF NOT EXISTS maintenances (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT NOT NULL DEFAULT '',
    type       TEXT NOT NULL DEFAULT '',
    start_value REAL NOT NULL DEFAULT 0,
    period     REAL NOT NULL DEFAULT 0,
    attributes TEXT
);

CREATE TABLE IF NOT EXISTS orders (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    unique_id    TEXT NOT NULL DEFAULT '',
    description  TEXT NOT NULL DEFAULT '',
    from_address TEXT NOT NULL DEFAULT '',
    to_address   TEXT NOT NULL DEFAULT '',
    attributes   TEXT
);

CREATE TABLE IF NOT EXISTS reports (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    calendar_id INTEGER REFERENCES calendars (id) ON DELETE SET NULL,
    type        TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    attributes  TEXT
);

CREATE TABLE IF NOT EXISTS statistics (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    capture_time         DATETIME,
    active_users         INTEGER NOT NULL DEFAULT 0,
    active_devices       INTEGER NOT NULL DEFAULT 0,
    requests             INTEGER NOT NULL DEFAULT 0,
    messages_received    INTEGER NOT NULL DEFAULT 0,
    messages_stored      INTEGER NOT NULL DEFAULT 0,
    mail_sent            INTEGER NOT NULL DEFAULT 0,
    sms_sent             INTEGER NOT NULL DEFAULT 0,
    geocoder_requests    INTEGER NOT NULL DEFAULT 0,
    geolocation_requests INTEGER NOT NULL DEFAULT 0,
    protocols            TEXT,
    attributes           TEXT
);
CREATE INDEX IF NOT EXISTS statistics_capture_time_idx ON statistics (capture_time DESC);

CREATE TABLE IF NOT EXISTS attributes (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    description TEXT NOT NULL DEFAULT '',
    attribute   TEXT NOT NULL DEFAULT '',
    expression  TEXT NOT NULL DEFAULT '',
    type        TEXT NOT NULL DEFAULT '',
    priority    INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS permissions (
    owner_type    TEXT NOT NULL,
    owner_id      INTEGER NOT NULL,
    property_type TEXT NOT NULL,
    property_id   INTEGER NOT NULL,
    created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (owner_type, owner_id, property_type, property_id)
);
CREATE INDEX IF NOT EXISTS permissions_property_idx ON permissions (property_type, property_id);

CREATE TABLE IF NOT EXISTS api_tokens (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id    INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    scopes     TEXT,
    expires_at DATETIME,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS api_tokens_user_idx ON api_tokens (user_id);

CREATE TABLE IF NOT EXISTS commands (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    device_id   INTEGER NOT NULL REFERENCES devices (id) ON DELETE CASCADE,
    type        TEXT NOT NULL,
    attributes  TEXT,
    status      TEXT NOT NULL DEFAULT 'pending'
                CHECK (status IN ('pending', 'sent', 'done', 'failed')),
    result_code INTEGER,
    sent_at     DATETIME,
    done_at     DATETIME,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    serial      INTEGER,
    attempts    INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS commands_device_idx ON commands (device_id, id DESC);
CREATE INDEX IF NOT EXISTS commands_pending_idx
    ON commands (device_id, id DESC) WHERE status = 'sent';
CREATE INDEX IF NOT EXISTS commands_serial_idx
    ON commands (device_id, serial) WHERE status = 'sent';
CREATE INDEX IF NOT EXISTS commands_undelivered_idx
    ON commands (device_id, id) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS commands_created_idx ON commands (created_at);

CREATE TABLE IF NOT EXISTS password_resets (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id    INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    expires_at DATETIME NOT NULL,
    used_at    DATETIME,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS password_resets_user_idx ON password_resets (user_id);

CREATE TABLE IF NOT EXISTS notifications (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    type         TEXT NOT NULL,
    notificators TEXT,
    description  TEXT NOT NULL DEFAULT '',
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    always       INTEGER NOT NULL DEFAULT 0,
    command_id   INTEGER NOT NULL DEFAULT 0,
    calendar_id  INTEGER REFERENCES calendars (id) ON DELETE SET NULL,
    attributes   TEXT
);

CREATE TABLE IF NOT EXISTS user_notifications (
    user_id         INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    notification_id INTEGER NOT NULL REFERENCES notifications (id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, notification_id)
);
CREATE INDEX IF NOT EXISTS user_notifications_notification_idx
    ON user_notifications (notification_id);

CREATE TABLE IF NOT EXISTS protocol_settings (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    protocol  TEXT NOT NULL UNIQUE,
    port      INTEGER NOT NULL DEFAULT 0,
    transport TEXT NOT NULL DEFAULT 'tcp'
);

CREATE TABLE IF NOT EXISTS server_settings (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    registration      INTEGER NOT NULL DEFAULT 0,
    readonly          INTEGER NOT NULL DEFAULT 0,
    device_readonly   INTEGER NOT NULL DEFAULT 0,
    limit_commands    INTEGER NOT NULL DEFAULT 0,
    disable_reports   INTEGER NOT NULL DEFAULT 0,
    fixed_email       INTEGER NOT NULL DEFAULT 0,
    force_settings    INTEGER NOT NULL DEFAULT 0,
    announcement      TEXT NOT NULL DEFAULT '',
    map               TEXT NOT NULL DEFAULT '',
    map_url           TEXT NOT NULL DEFAULT '',
    overlay_url       TEXT NOT NULL DEFAULT '',
    bing_key          TEXT NOT NULL DEFAULT '',
    poi_layer         TEXT NOT NULL DEFAULT '',
    coordinate_format TEXT NOT NULL DEFAULT '',
    latitude          REAL NOT NULL DEFAULT 0,
    longitude         REAL NOT NULL DEFAULT 0,
    zoom              INTEGER NOT NULL DEFAULT 0,
    attributes        TEXT
);

CREATE TABLE IF NOT EXISTS actions (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    action_time DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    address     TEXT NOT NULL DEFAULT '',
    user_id     INTEGER NOT NULL DEFAULT 0,
    action_type TEXT NOT NULL,
    object_type TEXT NOT NULL DEFAULT '',
    object_id   INTEGER NOT NULL DEFAULT 0,
    attributes  TEXT
);
CREATE INDEX IF NOT EXISTS actions_time_idx ON actions (action_time);

CREATE TABLE IF NOT EXISTS media (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    device_id  INTEGER NOT NULL REFERENCES devices (id) ON DELETE CASCADE,
    file_name  TEXT NOT NULL UNIQUE,
    type       TEXT NOT NULL DEFAULT 'image'
               CHECK (type IN ('image', 'video', 'audio')),
    size       INTEGER NOT NULL DEFAULT 0,
    media_time DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    attributes TEXT
);
CREATE INDEX IF NOT EXISTS media_device_idx ON media (device_id, media_time DESC);
