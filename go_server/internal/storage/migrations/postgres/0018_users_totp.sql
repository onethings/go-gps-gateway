-- Two factor authentication. The key stays NULL until the user generates one
-- and enabled stays false until a code proves the authenticator app holds it,
-- so an abandoned setup cannot lock the account out.
ALTER TABLE users ADD COLUMN IF NOT EXISTS totp_key TEXT;
ALTER TABLE users ADD COLUMN IF NOT EXISTS totp_enabled BOOLEAN NOT NULL DEFAULT false;
