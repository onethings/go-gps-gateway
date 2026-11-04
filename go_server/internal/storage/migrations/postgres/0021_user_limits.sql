-- The Java server caps the devices a user may hold and the accounts a user may
-- create. -1 means no cap, which is what every existing Go account behaved
-- like, so nothing changes for an installation that upgrades.
ALTER TABLE users ADD COLUMN IF NOT EXISTS device_limit INTEGER NOT NULL DEFAULT -1;
ALTER TABLE users ADD COLUMN IF NOT EXISTS user_limit   INTEGER NOT NULL DEFAULT -1;
