CREATE TABLE IF NOT EXISTS notifications (
    id           BIGSERIAL PRIMARY KEY,
    type         TEXT NOT NULL,
    notificators TEXT[] NOT NULL DEFAULT '{}',
    description  TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS user_notifications (
    user_id         BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    notification_id BIGINT NOT NULL REFERENCES notifications (id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, notification_id)
);

CREATE INDEX IF NOT EXISTS user_notifications_notification_idx
    ON user_notifications (notification_id);
