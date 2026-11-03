-- The alerts table is folded into events. An alert was already an event in
-- everything but name: a rule verdict attached to a device and a time. events
-- adds what alerts could not do - a position_id that is filled in, because the
-- event is written in the same transaction as the position it belongs to.
--
-- Copying before dropping keeps the history of an installation that already ran
-- the alert rules.
INSERT INTO events (device_id, position_id, geofence_id, type, event_time, attributes)
SELECT device_id, position_id, geofence_id, type, alert_time, attributes FROM alerts;

DROP TABLE IF EXISTS alerts;
