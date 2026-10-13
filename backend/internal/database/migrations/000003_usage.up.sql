-- Usage metrics for the admin Usage page.
--   last_seen_at     last authenticated request (online = within 5 min)
--   usage_daily      one row per user per (UTC) day, accumulated every minute
--   usage_snapshots  cluster-wide concurrency, one row per minute, pruned after 30 days
ALTER TABLE admins ADD COLUMN IF NOT EXISTS last_seen_at TIMESTAMPTZ;

CREATE TABLE IF NOT EXISTS usage_daily (
    user_id          UUID NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
    day              DATE NOT NULL,
    active_minutes   INTEGER NOT NULL DEFAULT 0,
    kernel_minutes   INTEGER NOT NULL DEFAULT 0,
    cpu_core_seconds DOUBLE PRECISION NOT NULL DEFAULT 0,
    mem_gb_seconds   DOUBLE PRECISION NOT NULL DEFAULT 0,
    cells_run        INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (user_id, day)
);
CREATE INDEX IF NOT EXISTS idx_usage_daily_day ON usage_daily(day);

CREATE TABLE IF NOT EXISTS usage_snapshots (
    ts              TIMESTAMPTZ PRIMARY KEY,
    online_users    INTEGER NOT NULL,
    running_kernels INTEGER NOT NULL,
    cpu_cores       DOUBLE PRECISION NOT NULL,
    mem_bytes       BIGINT NOT NULL
);
