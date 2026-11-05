-- See postgres/0021_user_limits.sql. -1 means no cap.
ALTER TABLE users ADD COLUMN device_limit INTEGER NOT NULL DEFAULT -1;
ALTER TABLE users ADD COLUMN user_limit   INTEGER NOT NULL DEFAULT -1;
