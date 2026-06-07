package database

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	_ "github.com/lib/pq"
	"github.com/rs/zerolog/log"

	"github.com/sparklabx/sparklabx/backend/internal/config"
)

var (
	db   *sql.DB
	once sync.Once
)

// pingWithRetry retries Ping until success or the total budget runs out.
// On a fresh `helm install`, the postgres pod usually becomes
// DNS-resolvable a few seconds after the backend starts. Without retry,
// the first Ping fails with `lookup postgres: no such host`, log.Fatal
// triggers, k8s restarts the container, and the user sees 2-3
// CrashLoopBackOff cycles before the deploy settles. The same loop also
// absorbs short network blips and postgres failovers during normal
// operation, so it isn't startup-specific.
func pingWithRetry(d *sql.DB) error {
	const (
		maxAttempts = 30               // ~2 min total at the chosen backoff
		baseDelay   = 1 * time.Second  // first wait
		maxDelay    = 10 * time.Second // cap each individual wait
	)
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := d.PingContext(ctx)
		cancel()
		if err == nil {
			if attempt > 1 {
				log.Info().Int("attempts", attempt).Msg("database became reachable")
			}
			return nil
		}
		lastErr = err
		delay := time.Duration(attempt) * baseDelay
		if delay > maxDelay {
			delay = maxDelay
		}
		log.Warn().Err(err).Int("attempt", attempt).Int("max", maxAttempts).
			Dur("retry_in", delay).Msg("waiting for database")
		time.Sleep(delay)
	}
	return fmt.Errorf("database unreachable after %d attempts: %w", maxAttempts, lastErr)
}

func Init(cfg *config.Config) error {
	var initErr error
	once.Do(func() {
		var err error
		db, err = sql.Open("postgres", cfg.DatabaseURL)
		if err != nil {
			initErr = fmt.Errorf("failed to open database: %w", err)
			return
		}

		if err = pingWithRetry(db); err != nil {
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
