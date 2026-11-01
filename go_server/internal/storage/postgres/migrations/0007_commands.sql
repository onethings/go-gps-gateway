-- Every command the REST layer sends becomes one row, so a support engineer can
-- tell a command that was never accepted from one the device executed. A row
-- is written before the payload reaches the socket and never deleted, which is
-- what makes the history useful: status 'failed' with a NULL result_code means
-- the send never got out, and 'failed' with a code means the device refused.
CREATE TABLE IF NOT EXISTS commands (
    id          BIGSERIAL PRIMARY KEY,
    device_id   BIGINT NOT NULL REFERENCES devices (id) ON DELETE CASCADE,
    type        TEXT NOT NULL,
    attributes  JSONB NOT NULL DEFAULT '{}'::jsonb,
    status      TEXT NOT NULL DEFAULT 'pending'
                CHECK (status IN ('pending', 'sent', 'done', 'failed')),
    result_code INTEGER,
    sent_at     TIMESTAMPTZ,
    done_at     TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS commands_device_idx ON commands (device_id, id DESC);

-- The acknowledgement path looks up the newest 'sent' row of one device, so
-- the partial index keeps that lookup independent of the history size.
CREATE INDEX IF NOT EXISTS commands_pending_idx
    ON commands (device_id, id DESC) WHERE status = 'sent';
