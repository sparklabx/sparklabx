package database

import (
	"github.com/rs/zerolog/log"
	"golang.org/x/crypto/bcrypt"

	"github.com/sparklabx/sparklabx/backend/internal/config"
)

// MigrateAndSeed runs schema migrations and seeds initial data.
// Notebook-lite scope: admins + notebooks + storage + kernel infra.
// Exam-specific tables (sessions, candidates, submissions, grading, provision_logs)
// are intentionally absent — see main branch if you need them.
func MigrateAndSeed(cfg *config.Config) error {
	db := GetDB()

	migrations := []string{
		`CREATE TABLE IF NOT EXISTS admins (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			username VARCHAR(255) UNIQUE NOT NULL,
			email VARCHAR(255) UNIQUE NOT NULL,
			password_hash VARCHAR(255) NOT NULL,
			role VARCHAR(20) DEFAULT 'admin',
			created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
		)`,
		`ALTER TABLE admins ADD COLUMN IF NOT EXISTS role VARCHAR(20) DEFAULT 'admin'`,
		`UPDATE admins SET role = 'superadmin' WHERE username = (SELECT username FROM admins ORDER BY created_at ASC LIMIT 1) AND role = 'admin'`,
		// Encrypted MinIO IAM secret. Each admin gets a per-user MinIO account so
		// the kernel pod can only access their own users/<slug>/* prefix +
		// public/*. Empty until first login post-IAM-rollout — backfilled lazily.
		`ALTER TABLE admins ADD COLUMN IF NOT EXISTS minio_secret_enc TEXT NOT NULL DEFAULT ''`,
		// Encrypted OIDC tokens from SSO login, retained so the kernel can
		// authenticate to external services (e.g. Trino) as the logged-in user
		// via token passthrough. Empty unless the user logged in via OIDC SSO.
		`ALTER TABLE admins ADD COLUMN IF NOT EXISTS oidc_access_token_enc TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE admins ADD COLUMN IF NOT EXISTS oidc_refresh_token_enc TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE admins ADD COLUMN IF NOT EXISTS oidc_token_expires_at TIMESTAMPTZ`,

		`CREATE TABLE IF NOT EXISTS notebooks (
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
		)`,
		`CREATE INDEX IF NOT EXISTS idx_notebooks_owner ON notebooks(owner_id, owner_type)`,

		`CREATE TABLE IF NOT EXISTS notebook_cells (
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
		)`,
		`CREATE INDEX IF NOT EXISTS idx_notebook_cells_notebook ON notebook_cells(notebook_id, cell_order)`,

		`CREATE TABLE IF NOT EXISTS allowed_email_rules (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			rule_type VARCHAR(20) NOT NULL,
			value VARCHAR(255) NOT NULL,
			enabled BOOLEAN NOT NULL DEFAULT TRUE,
			added_by VARCHAR(255) NOT NULL DEFAULT '',
			note TEXT DEFAULT '',
			created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
			CONSTRAINT allowed_email_rules_type_check CHECK (rule_type IN ('domain', 'exact_email')),
			CONSTRAINT allowed_email_rules_unique UNIQUE (rule_type, value)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_allowed_email_rules_lookup ON allowed_email_rules(enabled, rule_type, value)`,

		// Admin-managed data connectors (Trino, Postgres, MySQL, …). The TRINO_URL
		// env still seeds a non-deletable "trino" connector; these are the ones
		// added from the UI. password_enc is AES-GCM (services.MinIOIAM key) for
		// broker-mapped (user/password) sources; empty for app-jwt/idp-passthrough.
		`CREATE TABLE IF NOT EXISTS connectors (
			id VARCHAR(64) PRIMARY KEY,
			type VARCHAR(32) NOT NULL,
			label VARCHAR(128) NOT NULL,
			url TEXT NOT NULL,
			auth VARCHAR(32) NOT NULL,
			username VARCHAR(255) NOT NULL DEFAULT '',
			password_enc TEXT NOT NULL DEFAULT '',
			added_by VARCHAR(255) NOT NULL DEFAULT '',
			created_at TIMESTAMPTZ DEFAULT NOW()
		)`,
		// Connector scope: owner_id = '' is a shared (org-wide) source; a non-empty
		// admin id makes it personal (visible only to that user). Existing rows
		// default to shared.
		`ALTER TABLE connectors ADD COLUMN IF NOT EXISTS owner_id VARCHAR(64) NOT NULL DEFAULT ''`,

		// K8s per-user pod tracking
		`CREATE TABLE IF NOT EXISTS user_kernel_pods (
			user_id        TEXT PRIMARY KEY,
			pod_name       TEXT NOT NULL,
			pod_namespace  TEXT NOT NULL,
			pod_url        TEXT NOT NULL DEFAULT '',
			status         TEXT NOT NULL DEFAULT 'pending',
			phase_message  TEXT NOT NULL DEFAULT '',
			created_at     TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
			last_used_at   TIMESTAMP WITH TIME ZONE DEFAULT NOW()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_user_kernel_pods_idle ON user_kernel_pods(status, last_used_at)`,
		// Per-pod resources chosen at connect time (issue #41, k8s_per_user
		// presets). Empty string on legacy rows → gateway falls back to cluster
		// defaults, so no backfill is needed.
		`ALTER TABLE user_kernel_pods ADD COLUMN IF NOT EXISTS cpu_request TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE user_kernel_pods ADD COLUMN IF NOT EXISTS mem_request TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE user_kernel_pods ADD COLUMN IF NOT EXISTS cpu_limit TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE user_kernel_pods ADD COLUMN IF NOT EXISTS mem_limit TEXT NOT NULL DEFAULT ''`,

		// Notebook→kernel cache (UNLOGGED, regenerable)
		`CREATE UNLOGGED TABLE IF NOT EXISTS notebook_kernels (
			notebook_id  TEXT NOT NULL,
			user_id      TEXT NOT NULL,
			kernel_id    TEXT NOT NULL,
			last_used_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
			PRIMARY KEY (notebook_id, user_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_notebook_kernels_user ON notebook_kernels(user_id)`,

		// Backfill: OAuth admins originally got username = full email. Storage
		// now uses username as a path segment (users/<username>/...), and "@" in
		// S3 keys triggers SigV4 SignatureDoesNotMatch under some clients. Rewrite
		// to a clean slug from the email local-part. PL/pgSQL handles UNIQUE
		// collisions by appending a short random suffix.
		`DO $$
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
		END $$`,
	}

	for _, m := range migrations {
		if _, err := db.Exec(m); err != nil {
			log.Error().Err(err).Str("migration", m[:60]).Msg("migration failed")
			return err
		}
	}
	log.Info().Msg("database migrations completed")

	// Seed admin user
	if cfg.SeedAdminUsername != "" && cfg.SeedAdminPassword != "" {
		var exists bool
		err := db.QueryRow("SELECT EXISTS(SELECT 1 FROM admins WHERE username = $1)", cfg.SeedAdminUsername).Scan(&exists)
		if err != nil {
			return err
		}
		if !exists {
			hash, err := bcrypt.GenerateFromPassword([]byte(cfg.SeedAdminPassword), bcrypt.DefaultCost)
			if err != nil {
				return err
			}
			_, err = db.Exec(
				"INSERT INTO admins (id, username, email, password_hash, role) VALUES (gen_random_uuid(), $1, $2, $3, 'superadmin')",
				cfg.SeedAdminUsername, cfg.SeedAdminEmail, string(hash),
			)
			if err != nil {
				return err
			}
			log.Info().Str("username", cfg.SeedAdminUsername).Msg("seed admin user created")
		}
	}

	return nil
}
