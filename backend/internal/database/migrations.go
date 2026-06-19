package database

import (
	"embed"
	"errors"

	"github.com/golang-migrate/migrate/v4"
	migratepgx "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/rs/zerolog/log"
	"golang.org/x/crypto/bcrypt"

	"github.com/sparklabx/sparklabx/backend/internal/config"
)

// Versioned schema migrations, applied by golang-migrate. The runner is separate
// from the application logic; add a new pair of NNNNNN_name.{up,down}.sql files
// to evolve the schema. The baseline (000001) is idempotent so it adopts an
// existing pre-migrate DB without a manual baseline step.
//
//go:embed migrations/*.sql
var migrationsFS embed.FS

// MigrateAndSeed runs schema migrations then seeds the initial admin user.
func MigrateAndSeed(cfg *config.Config) error {
	db := GetDB()

	src, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return err
	}
	driver, err := migratepgx.WithInstance(db, &migratepgx.Config{})
	if err != nil {
		return err
	}
	m, err := migrate.NewWithInstance("iofs", src, "pgx", driver)
	if err != nil {
		return err
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		log.Error().Err(err).Msg("schema migration failed")
		return err
	}
	if v, dirty, verr := m.Version(); verr == nil {
		log.Info().Uint("version", v).Bool("dirty", dirty).Msg("database migrations completed")
	} else {
		log.Info().Msg("database migrations completed")
	}

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
