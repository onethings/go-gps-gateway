-- Initial MySQL schema. It mirrors the logical schema the PostgreSQL
-- migrations build, with MySQL types: JSON columns keep the same text, and
-- DATETIME(6) replaces TIMESTAMPTZ because the store writes UTC.
CREATE TABLE IF NOT EXISTS calendars (
    id         BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    name       VARCHAR(255) NOT NULL DEFAULT '',
    data       LONGBLOB,
    attributes JSON
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS device_groups (
    id         BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    name       VARCHAR(255) NOT NULL DEFAULT '',
    group_id   BIGINT,
    attributes JSON,
    CONSTRAINT fk_device_groups_parent FOREIGN KEY (group_id) REFERENCES device_groups (id) ON DELETE SET NULL
,
    INDEX device_groups_parent_idx (group_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS users (
    id                    BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    email                 VARCHAR(255) NOT NULL,
    name                  VARCHAR(255) NOT NULL DEFAULT '',
    admin                 BOOLEAN NOT NULL DEFAULT FALSE,
    created_at            DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    disabled              BOOLEAN NOT NULL DEFAULT FALSE,
    password_hash         TEXT,
    failed_login_attempts INTEGER NOT NULL DEFAULT 0,
    locked_until          DATETIME(6),
    temporary             BOOLEAN NOT NULL DEFAULT FALSE,
    attributes            JSON,
    totp_key              TEXT,
    totp_enabled          BOOLEAN NOT NULL DEFAULT FALSE,
    UNIQUE KEY users_email_unique (email)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS devices (
    id              BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    unique_id       VARCHAR(128) NOT NULL,
    name            VARCHAR(255) NOT NULL DEFAULT '',
    model           VARCHAR(128),
    protocol        VARCHAR(128),
    attributes      JSON,
    last_update     DATETIME(6),
    created_at      DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    status          VARCHAR(32) NOT NULL DEFAULT 'unknown',
    group_id        BIGINT,
    phone           VARCHAR(128),
    contact         VARCHAR(255),
    category        VARCHAR(128),
    disabled        BOOLEAN NOT NULL DEFAULT FALSE,
    expiration_time DATETIME(6),
    calendar_id     BIGINT NOT NULL DEFAULT 0,
    position_id     BIGINT NOT NULL DEFAULT 0,
    UNIQUE KEY devices_unique_id_unique (unique_id),
    CONSTRAINT fk_devices_group FOREIGN KEY (group_id) REFERENCES device_groups (id) ON DELETE SET NULL
,
    INDEX devices_status_idx (status),
    INDEX devices_group_idx (group_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS positions (
    id          BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    device_id   BIGINT NOT NULL,
    protocol    VARCHAR(128) NOT NULL,
    device_time DATETIME(6) NOT NULL,
    server_time DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    valid       BOOLEAN NOT NULL DEFAULT FALSE,
    latitude    DOUBLE,
    longitude   DOUBLE,
    altitude    DOUBLE,
    speed       DOUBLE,
    course      DOUBLE,
    accuracy    DOUBLE,
    attributes  JSON,
    geofenceids JSON,
    CONSTRAINT fk_positions_device FOREIGN KEY (device_id) REFERENCES devices (id) ON DELETE CASCADE
,
    INDEX positions_device_time_idx (device_id, device_time DESC),
    INDEX positions_time_idx (device_time DESC)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS events (
    id          BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    device_id   BIGINT NOT NULL,
    position_id BIGINT,
    geofence_id BIGINT,
    type        VARCHAR(128) NOT NULL,
    event_time  DATETIME(6) NOT NULL,
    attributes  JSON,
    CONSTRAINT fk_events_device FOREIGN KEY (device_id) REFERENCES devices (id) ON DELETE CASCADE,
    CONSTRAINT fk_events_position FOREIGN KEY (position_id) REFERENCES positions (id) ON DELETE SET NULL
,
    INDEX events_device_time_idx (device_id, event_time DESC),
    INDEX events_time_idx (event_time DESC),
    INDEX events_type_time_idx (type, event_time DESC)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS geofences (
    id          BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    name        VARCHAR(255) NOT NULL DEFAULT '',
    area        LONGTEXT NOT NULL,
    attributes  JSON,
    created_at  DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    calendar_id BIGINT,
    description VARCHAR(4000) NOT NULL DEFAULT '',
    CONSTRAINT fk_geofences_calendar FOREIGN KEY (calendar_id) REFERENCES calendars (id) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS drivers (
    id         BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    name       VARCHAR(255) NOT NULL DEFAULT '',
    unique_id  VARCHAR(128) NOT NULL DEFAULT '',
    attributes JSON
,
    INDEX drivers_unique_id_idx (unique_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS maintenances (
    id         BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    name       VARCHAR(255) NOT NULL DEFAULT '',
    type       VARCHAR(128) NOT NULL DEFAULT '',
    start_value DOUBLE NOT NULL DEFAULT 0,
    period     DOUBLE NOT NULL DEFAULT 0,
    attributes JSON
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS orders (
    id           BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    unique_id    VARCHAR(128) NOT NULL DEFAULT '',
    description  VARCHAR(4000) NOT NULL DEFAULT '',
    from_address VARCHAR(4000) NOT NULL DEFAULT '',
    to_address   VARCHAR(4000) NOT NULL DEFAULT '',
    attributes   JSON
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS reports (
    id          BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    calendar_id BIGINT,
    type        VARCHAR(128) NOT NULL DEFAULT '',
    description VARCHAR(4000) NOT NULL DEFAULT '',
    attributes  JSON,
    CONSTRAINT fk_reports_calendar FOREIGN KEY (calendar_id) REFERENCES calendars (id) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS statistics (
    id                   BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    capture_time         DATETIME(6),
    active_users         INTEGER NOT NULL DEFAULT 0,
    active_devices       INTEGER NOT NULL DEFAULT 0,
    requests             INTEGER NOT NULL DEFAULT 0,
    messages_received    INTEGER NOT NULL DEFAULT 0,
    messages_stored      INTEGER NOT NULL DEFAULT 0,
    mail_sent            INTEGER NOT NULL DEFAULT 0,
    sms_sent             INTEGER NOT NULL DEFAULT 0,
    geocoder_requests    INTEGER NOT NULL DEFAULT 0,
    geolocation_requests INTEGER NOT NULL DEFAULT 0,
    protocols            JSON,
    attributes           JSON
,
    INDEX statistics_capture_time_idx (capture_time DESC)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS attributes (
    id          BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    description VARCHAR(4000) NOT NULL DEFAULT '',
    attribute   VARCHAR(128) NOT NULL DEFAULT '',
    expression  VARCHAR(4000) NOT NULL DEFAULT '',
    type        VARCHAR(128) NOT NULL DEFAULT '',
    priority    INTEGER NOT NULL DEFAULT 0
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS permissions (
    owner_type    VARCHAR(32) NOT NULL,
    owner_id      BIGINT NOT NULL,
    property_type VARCHAR(32) NOT NULL,
    property_id   BIGINT NOT NULL,
    created_at    DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (owner_type, owner_id, property_type, property_id)
,
    INDEX permissions_property_idx (property_type, property_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS api_tokens (
    id         BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    user_id    BIGINT NOT NULL,
    token_hash VARCHAR(128) NOT NULL,
    scopes     JSON,
    expires_at DATETIME(6),
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    UNIQUE KEY api_tokens_hash_unique (token_hash),
    CONSTRAINT fk_api_tokens_user FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE
,
    INDEX api_tokens_user_idx (user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS commands (
    id          BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    device_id   BIGINT NOT NULL,
    type        VARCHAR(128) NOT NULL,
    attributes  JSON,
    status      VARCHAR(16) NOT NULL DEFAULT 'pending',
    result_code INTEGER,
    sent_at     DATETIME(6),
    done_at     DATETIME(6),
    created_at  DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    serial      INTEGER,
    attempts    INTEGER NOT NULL DEFAULT 0,
    CONSTRAINT fk_commands_device FOREIGN KEY (device_id) REFERENCES devices (id) ON DELETE CASCADE,
    CONSTRAINT commands_status_check CHECK (status IN ('pending', 'sent', 'done', 'failed'))
,
    INDEX commands_device_idx (device_id, id DESC),
    INDEX commands_pending_idx (device_id, id DESC),
    INDEX commands_serial_idx (device_id, serial),
    INDEX commands_created_idx (created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS password_resets (
    id         BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    user_id    BIGINT NOT NULL,
    token_hash VARCHAR(128) NOT NULL,
    expires_at DATETIME(6) NOT NULL,
    used_at    DATETIME(6),
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    UNIQUE KEY password_resets_hash_unique (token_hash),
    CONSTRAINT fk_password_resets_user FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE
,
    INDEX password_resets_user_idx (user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS notifications (
    id           BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    type         VARCHAR(128) NOT NULL,
    notificators JSON,
    description  VARCHAR(4000) NOT NULL DEFAULT '',
    created_at   DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    always       BOOLEAN NOT NULL DEFAULT FALSE,
    command_id   BIGINT NOT NULL DEFAULT 0,
    calendar_id  BIGINT,
    attributes   JSON,
    CONSTRAINT fk_notifications_calendar FOREIGN KEY (calendar_id) REFERENCES calendars (id) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS user_notifications (
    user_id         BIGINT NOT NULL,
    notification_id BIGINT NOT NULL,
    PRIMARY KEY (user_id, notification_id),
    CONSTRAINT fk_user_notifications_user FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE,
    CONSTRAINT fk_user_notifications_notification FOREIGN KEY (notification_id) REFERENCES notifications (id) ON DELETE CASCADE
,
    INDEX user_notifications_notification_idx (notification_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS protocol_settings (
    id        BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    protocol  VARCHAR(128) NOT NULL,
    port      INTEGER NOT NULL DEFAULT 0,
    transport VARCHAR(16) NOT NULL DEFAULT 'tcp',
    UNIQUE KEY protocol_settings_protocol_unique (protocol)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS server_settings (
    id                BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    registration      BOOLEAN NOT NULL DEFAULT FALSE,
    readonly          BOOLEAN NOT NULL DEFAULT FALSE,
    device_readonly   BOOLEAN NOT NULL DEFAULT FALSE,
    limit_commands    BOOLEAN NOT NULL DEFAULT FALSE,
    disable_reports   BOOLEAN NOT NULL DEFAULT FALSE,
    fixed_email       BOOLEAN NOT NULL DEFAULT FALSE,
    force_settings    BOOLEAN NOT NULL DEFAULT FALSE,
    announcement      VARCHAR(4000) NOT NULL DEFAULT '',
    map               VARCHAR(128) NOT NULL DEFAULT '',
    map_url           VARCHAR(512) NOT NULL DEFAULT '',
    overlay_url       VARCHAR(512) NOT NULL DEFAULT '',
    bing_key          VARCHAR(128) NOT NULL DEFAULT '',
    poi_layer         VARCHAR(512) NOT NULL DEFAULT '',
    coordinate_format VARCHAR(128) NOT NULL DEFAULT '',
    latitude          DOUBLE NOT NULL DEFAULT 0,
    longitude         DOUBLE NOT NULL DEFAULT 0,
    zoom              INTEGER NOT NULL DEFAULT 0,
    attributes        JSON
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS actions (
    id          BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    action_time DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    address     VARCHAR(255) NOT NULL DEFAULT '',
    user_id     BIGINT NOT NULL DEFAULT 0,
    action_type VARCHAR(128) NOT NULL,
    object_type VARCHAR(128) NOT NULL DEFAULT '',
    object_id   BIGINT NOT NULL DEFAULT 0,
    attributes  JSON
,
    INDEX actions_time_idx (action_time)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS media (
    id         BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    device_id  BIGINT NOT NULL,
    file_name  VARCHAR(255) NOT NULL,
    type       VARCHAR(16) NOT NULL DEFAULT 'image',
    size       BIGINT NOT NULL DEFAULT 0,
    media_time DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    attributes JSON,
    UNIQUE KEY media_file_name_unique (file_name),
    CONSTRAINT fk_media_device FOREIGN KEY (device_id) REFERENCES devices (id) ON DELETE CASCADE,
    CONSTRAINT media_type_check CHECK (type IN ('image', 'video', 'audio'))
,
    INDEX media_device_idx (device_id, media_time DESC)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
