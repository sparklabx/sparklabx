-- Baseline schema. Statements are idempotent (IF NOT EXISTS / guarded DO blocks)
-- so this migration applies cleanly on a fresh DB and is a no-op on a DB that
-- already had the pre-golang-migrate hand-rolled schema. Future migrations
-- (000002+) can be plain forward migrations.

CREATE TABLE IF NOT EXISTS admins (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    username VARCHAR(255) UNIQUE NOT NULL,
    email VARCHAR(255) UNIQUE NOT NULL,
    password_hash VARCHAR(255) NOT NULL,
    role VARCHAR(20) DEFAULT 'admin',
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);
ALTER TABLE admins ADD COLUMN IF NOT EXISTS role VARCHAR(20) DEFAULT 'admin';
UPDATE admins SET role = 'superadmin' WHERE username = (SELECT username FROM admins ORDER BY created_at ASC LIMIT 1) AND role = 'admin';
-- Encrypted MinIO IAM secret (per-user account; scoped to users/<slug>/* + public/*).
ALTER TABLE admins ADD COLUMN IF NOT EXISTS minio_secret_enc TEXT NOT NULL DEFAULT '';
-- Encrypted OIDC tokens from SSO login (kernel token-passthrough to e.g. Trino).
ALTER TABLE admins ADD COLUMN IF NOT EXISTS oidc_access_token_enc TEXT NOT NULL DEFAULT '';
ALTER TABLE admins ADD COLUMN IF NOT EXISTS oidc_refresh_token_enc TEXT NOT NULL DEFAULT '';
ALTER TABLE admins ADD COLUMN IF NOT EXISTS oidc_token_expires_at TIMESTAMPTZ;

CREATE TABLE IF NOT EXISTS notebooks (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name VARCHAR(255) NOT NULL DEFAULT 'Untitled',
    description TEXT DEFAULT '',
    language VARCHAR(50) NOT NULL DEFAULT 'python',
    owner_id UUID NOT NULL,
    owner_type VARCHAR(20) NOT NULL DEFAULT 'admin',
    is_public BOOLEAN NOT NULL DEFAULT false,
    cluster_config JSONB DEFAULT '{}'::jsonb,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_notebooks_owner ON notebooks(owner_id, owner_type);

CREATE TABLE IF NOT EXISTS notebook_cells (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    notebook_id UUID NOT NULL REFERENCES notebooks(id) ON DELETE CASCADE,
    type VARCHAR(20) NOT NULL DEFAULT 'code',
    source TEXT DEFAULT '',
    cell_order INTEGER NOT NULL DEFAULT 0,
    execution_count INTEGER,
    last_output JSONB,
    last_execution_time_ms INTEGER,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_notebook_cells_notebook ON notebook_cells(notebook_id, cell_order);

CREATE TABLE IF NOT EXISTS allowed_email_rules (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    rule_type VARCHAR(20) NOT NULL,
    value VARCHAR(255) NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    added_by VARCHAR(255) NOT NULL DEFAULT '',
    note TEXT DEFAULT '',
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    CONSTRAINT allowed_email_rules_type_check CHECK (rule_type IN ('domain', 'exact_email')),
    CONSTRAINT allowed_email_rules_unique UNIQUE (rule_type, value)
);
CREATE INDEX IF NOT EXISTS idx_allowed_email_rules_lookup ON allowed_email_rules(enabled, rule_type, value);

-- UI-managed data connectors (Trino, Postgres, MySQL). password_enc is AES-GCM
-- for broker-mapped sources; empty for app-jwt/idp-passthrough.
CREATE TABLE IF NOT EXISTS connectors (
    id VARCHAR(64) PRIMARY KEY,
    type VARCHAR(32) NOT NULL,
    label VARCHAR(128) NOT NULL,
    url TEXT NOT NULL,
    auth VARCHAR(32) NOT NULL,
    username VARCHAR(255) NOT NULL DEFAULT '',
    password_enc TEXT NOT NULL DEFAULT '',
    added_by VARCHAR(255) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ DEFAULT NOW()
);
ALTER TABLE connectors ADD COLUMN IF NOT EXISTS owner_id VARCHAR(64) NOT NULL DEFAULT '';
-- Connectors are personal: id unique PER OWNER (two users can each have "trino"),
-- not globally. Swap the global PK on id for UNIQUE(owner_id, id).
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'connectors_pkey') THEN
        ALTER TABLE connectors DROP CONSTRAINT connectors_pkey;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'connectors_owner_id_id_key') THEN
        ALTER TABLE connectors ADD CONSTRAINT connectors_owner_id_id_key UNIQUE (owner_id, id);
    END IF;
END $$;

-- App-managed secrets (e.g. the connector signing key) — survive restarts, no volume.
CREATE TABLE IF NOT EXISTS app_secrets (key VARCHAR(64) PRIMARY KEY, value TEXT NOT NULL);

-- K8s per-user pod tracking.
CREATE TABLE IF NOT EXISTS user_kernel_pods (
    user_id        TEXT PRIMARY KEY,
    pod_name       TEXT NOT NULL,
    pod_namespace  TEXT NOT NULL,
    pod_url        TEXT NOT NULL DEFAULT '',
    status         TEXT NOT NULL DEFAULT 'pending',
    phase_message  TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    last_used_at   TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_user_kernel_pods_idle ON user_kernel_pods(status, last_used_at);
-- Per-pod resources chosen at connect time (issue #41). Empty → cluster defaults.
ALTER TABLE user_kernel_pods ADD COLUMN IF NOT EXISTS cpu_request TEXT NOT NULL DEFAULT '';
ALTER TABLE user_kernel_pods ADD COLUMN IF NOT EXISTS mem_request TEXT NOT NULL DEFAULT '';
ALTER TABLE user_kernel_pods ADD COLUMN IF NOT EXISTS cpu_limit TEXT NOT NULL DEFAULT '';
ALTER TABLE user_kernel_pods ADD COLUMN IF NOT EXISTS mem_limit TEXT NOT NULL DEFAULT '';

-- Notebook→kernel cache (UNLOGGED, regenerable).
CREATE UNLOGGED TABLE IF NOT EXISTS notebook_kernels (
    notebook_id  TEXT NOT NULL,
    user_id      TEXT NOT NULL,
    kernel_id    TEXT NOT NULL,
    last_used_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    PRIMARY KEY (notebook_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_notebook_kernels_user ON notebook_kernels(user_id);

-- Backfill: rewrite legacy OAuth usernames (full email) to an email-local-part
-- slug; PL/pgSQL handles UNIQUE collisions with a short random suffix.
DO $$
DECLARE
    r RECORD;
    new_slug TEXT;
BEGIN
    FOR r IN SELECT id, email FROM admins WHERE username LIKE '%@%' LOOP
        new_slug := LOWER(REGEXP_REPLACE(
            SPLIT_PART(SPLIT_PART(r.email, '+', 1), '@', 1),
            '[^a-z0-9._-]+', '-', 'g'
        ));
        new_slug := TRIM(BOTH '.-_' FROM new_slug);
        IF new_slug = '' THEN new_slug := 'user'; END IF;
        BEGIN
            UPDATE admins SET username = new_slug WHERE id = r.id;
        EXCEPTION WHEN unique_violation THEN
            UPDATE admins
            SET username = new_slug || '-' || SUBSTR(MD5(RANDOM()::TEXT), 1, 4)
            WHERE id = r.id;
        END;
    END LOOP;
END $$;
