-- Password login. The column stays NULL for accounts that only use tokens,
-- so an existing row needs no change and cannot be unlocked by a blank login.
ALTER TABLE users ADD COLUMN IF NOT EXISTS password_hash TEXT;
