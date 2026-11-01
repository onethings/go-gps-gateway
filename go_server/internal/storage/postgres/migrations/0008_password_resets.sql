-- Password self-service: consecutive failed logins lock an account for a
-- while, and a forgotten password is reset with a single-use emailed code.
-- The counters are on users because a login already reads that row.
ALTER TABLE users ADD COLUMN IF NOT EXISTS failed_login_attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN IF NOT EXISTS locked_until TIMESTAMPTZ;

-- Only the hash of a reset code is stored. used_at marks a code that was
-- already traded for a password, and created_at drives the resend throttle.
CREATE TABLE IF NOT EXISTS password_resets (
    id         BIGSERIAL PRIMARY KEY,
    user_id    BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    expires_at TIMESTAMPTZ NOT NULL,
    used_at    TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS password_resets_user_idx ON password_resets (user_id);
