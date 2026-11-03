-- GROUPS and START are reserved words in MySQL 8. The other dialects share the
-- same SQL, so the two identifiers are renamed instead of quoted per dialect.
ALTER TABLE groups RENAME TO device_groups;
ALTER INDEX IF EXISTS groups_parent_idx RENAME TO device_groups_parent_idx;
ALTER TABLE maintenances RENAME COLUMN start TO start_value;
