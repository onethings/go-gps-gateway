-- One generic table for every object permission. The Java server keeps one
-- table per pair (tc_user_device, tc_user_group, tc_group_geofence, ...), which
-- is the same shape repeated a dozen times; here the pair names are data, so a
-- new resource does not need a migration. The API contract is unchanged: a
-- permission is still a two key object such as {"userId":3,"deviceId":7}.
CREATE TABLE IF NOT EXISTS permissions (
    owner_type    TEXT NOT NULL,
    owner_id      BIGINT NOT NULL,
    property_type TEXT NOT NULL,
    property_id   BIGINT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (owner_type, owner_id, property_type, property_id)
);

-- The primary key covers "what does this owner have"; this index covers the
-- reverse question, "who may reach this object".
CREATE INDEX IF NOT EXISTS permissions_property_idx ON permissions (property_type, property_id);

-- user_devices becomes an ordinary pair so that device grants, group grants and
-- resource grants are read and written by one code path.
INSERT INTO permissions (owner_type, owner_id, property_type, property_id)
SELECT 'user', user_id, 'device', device_id FROM user_devices
ON CONFLICT DO NOTHING;

DROP TABLE user_devices;

-- A share link is handed out by creating a password-less account that owns the
-- shared object. temporary marks it, so a share account cannot be shared again
-- and an operator can tell one apart in the account list.
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS temporary BOOLEAN NOT NULL DEFAULT FALSE;
