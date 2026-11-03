-- The token scopes and notification channels were PostgreSQL arrays. The other
-- dialects store JSON text, so the two columns become JSONB as well. The
-- application already encodes and decodes them as JSON; this only removes the
-- array round trip that no other database has.
ALTER TABLE api_tokens ALTER COLUMN scopes DROP DEFAULT;
ALTER TABLE api_tokens ALTER COLUMN scopes TYPE JSONB USING to_jsonb(scopes);
ALTER TABLE api_tokens ALTER COLUMN scopes SET DEFAULT '[]'::jsonb;

ALTER TABLE notifications ALTER COLUMN notificators DROP DEFAULT;
ALTER TABLE notifications ALTER COLUMN notificators TYPE JSONB USING to_jsonb(notificators);
ALTER TABLE notifications ALTER COLUMN notificators SET DEFAULT '[]'::jsonb;
