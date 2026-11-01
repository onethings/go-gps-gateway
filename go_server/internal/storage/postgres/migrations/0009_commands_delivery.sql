-- A command that can be retransmitted needs the two facts the first version of
-- the table did not keep: the serial the downlink frame carried, so the
-- device's answer can be paired with the row it belongs to, and how often the
-- payload reached the socket, so a retried command is distinguishable from one
-- that went out once.
--
-- serial is NULL for the protocols that have no sequence number of their own
-- (GT06, H02, Teltonika); attempts stays 0 until the first successful write.
ALTER TABLE commands
    ADD COLUMN IF NOT EXISTS serial   INTEGER,
    ADD COLUMN IF NOT EXISTS attempts INTEGER NOT NULL DEFAULT 0;

-- The answer path looks one row up by the serial the device echoed. Partial on
-- 'sent' because only a waiting row can be answered.
CREATE INDEX IF NOT EXISTS commands_serial_idx
    ON commands (device_id, serial) WHERE status = 'sent';

-- The reconnect path and the retention sweep both walk rows that are not
-- finished yet, so a partial index keeps them off the history.
CREATE INDEX IF NOT EXISTS commands_undelivered_idx
    ON commands (device_id, id) WHERE status = 'pending';

CREATE INDEX IF NOT EXISTS commands_created_idx ON commands (created_at);
