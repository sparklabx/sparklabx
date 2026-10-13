DROP TABLE IF EXISTS usage_snapshots;
DROP TABLE IF EXISTS usage_daily;
ALTER TABLE admins DROP COLUMN IF EXISTS last_seen_at;
