CREATE TABLE IF NOT EXISTS users (
    id         BIGSERIAL PRIMARY KEY,
    email      TEXT NOT NULL UNIQUE,
    name       TEXT NOT NULL DEFAULT '',
    admin      BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS user_devices (
    user_id   BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    device_id BIGINT NOT NULL REFERENCES devices (id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, device_id)
);

CREATE INDEX IF NOT EXISTS user_devices_device_idx ON user_devices (device_id);

CREATE TABLE IF NOT EXISTS api_tokens (
    id         BIGSERIAL PRIMARY KEY,
    user_id    BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    scopes     TEXT[] NOT NULL DEFAULT '{}',
    expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS api_tokens_user_idx ON api_tokens (user_id);
