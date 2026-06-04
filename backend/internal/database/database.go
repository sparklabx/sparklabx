package database

import (
	"database/sql"
	"fmt"
	"sync"

	_ "github.com/lib/pq"
	"github.com/rs/zerolog/log"

	"github.com/sparklabx/sparklabx/backend/internal/config"
)

var (
	db   *sql.DB
	once sync.Once
)

func Init(cfg *config.Config) error {
	var initErr error
	once.Do(func() {
		var err error
		db, err = sql.Open("postgres", cfg.DatabaseURL)
		if err != nil {
			initErr = fmt.Errorf("failed to open database: %w", err)
			return
		}

		if err = db.Ping(); err != nil {
			initErr = fmt.Errorf("failed to ping database: %w", err)
			return
		}

		db.SetMaxOpenConns(25)
		db.SetMaxIdleConns(5)

		log.Info().Msg("database connection established")
	})
	return initErr
}

func GetDB() *sql.DB {
	return db
}

func Close() {
	if db != nil {
		if err := db.Close(); err != nil {
			log.Error().Err(err).Msg("error closing database connection")
		}
		log.Info().Msg("database connection closed")
	}
}
