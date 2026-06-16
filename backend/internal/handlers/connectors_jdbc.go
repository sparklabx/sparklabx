package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// JDBC catalog browser for SQL databases (Postgres, MySQL). The backend connects
// with the connector's stored broker-mapped credentials and reads
// information_schema. Two levels: schemas (no selector) then tables (?catalog=
// carries the chosen schema). System schemas are hidden. Browsing uses a plain
// connection (no TLS unless the URL sets it) suitable for internal databases;
// notebooks still query via the full JDBC URL through Spark.

const jdbcBrowseTimeout = 8 * time.Second

// jdbcMetadata returns schemas (schemaSel == "") or the tables of schemaSel.
func (h *AuthHandler) jdbcMetadata(inst ConnectorInstance, schemaSel string) (items []string, level string, err error) {
	driver, dsn, err := h.jdbcDriverDSN(inst)
	if err != nil {
		return nil, "", err
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, "", fmt.Errorf("open %s: %w", inst.Type, err)
	}
	defer db.Close()
	db.SetMaxOpenConns(2)

	ctx, cancel := context.WithTimeout(context.Background(), jdbcBrowseTimeout)
	defer cancel()

	var query string
	var args []any
	if schemaSel == "" {
		level = "schema"
		switch inst.Type {
		case "postgres":
			query = `SELECT schema_name FROM information_schema.schemata
			         WHERE schema_name NOT IN ('pg_catalog','information_schema','pg_toast')
			           AND schema_name NOT LIKE 'pg_temp_%' AND schema_name NOT LIKE 'pg_toast_temp_%'
			         ORDER BY 1`
		case "mysql":
			query = `SELECT schema_name FROM information_schema.schemata
			         WHERE schema_name NOT IN ('information_schema','performance_schema','mysql','sys')
			         ORDER BY 1`
		}
	} else {
		level = "table"
		// $1 (lib/pq) vs ? (go-sql-driver) placeholder differs by driver.
		ph := "?"
		if inst.Type == "postgres" {
			ph = "$1"
		}
		query = `SELECT table_name FROM information_schema.tables WHERE table_schema = ` + ph + ` ORDER BY 1`
		args = []any{schemaSel}
	}

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", fmt.Errorf("%s metadata query: %w", inst.Type, err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, "", err
		}
		items = append(items, name)
	}
	return items, level, rows.Err()
}

// jdbcDriverDSN converts the connector's JDBC URL + stored creds into a Go
// database/sql driver name and DSN.
func (h *AuthHandler) jdbcDriverDSN(inst ConnectorInstance) (driver, dsn string, err error) {
	password := ""
	if inst.PasswordEnc != "" && h.iam != nil {
		if pw, derr := h.iam.DecryptSecret(inst.PasswordEnc); derr == nil {
			password = pw
		} else {
			return "", "", fmt.Errorf("decrypt connector credential: %w", derr)
		}
	}
	// jdbc:postgresql://host:port/db?params → parse host/db (scheme after "jdbc:").
	u, perr := url.Parse(strings.TrimPrefix(inst.URL, "jdbc:"))
	if perr != nil {
		return "", "", fmt.Errorf("parse connector url: %w", perr)
	}
	dbName := strings.TrimPrefix(u.Path, "/")

	switch inst.Type {
	case "postgres":
		// lib/pq URL DSN. Honour an explicit sslmode, else disable (internal DBs).
		sslmode := u.Query().Get("sslmode")
		if sslmode == "" {
			sslmode = "disable"
		}
		dsn = fmt.Sprintf("postgres://%s:%s@%s/%s?sslmode=%s",
			url.QueryEscape(inst.Username), url.QueryEscape(password), u.Host, dbName, url.QueryEscape(sslmode))
		return "postgres", dsn, nil
	case "mysql":
		// go-sql-driver DSN: user:pass@tcp(host:port)/db
		dsn = fmt.Sprintf("%s:%s@tcp(%s)/%s", inst.Username, password, u.Host, dbName)
		return "mysql", dsn, nil
	}
	return "", "", fmt.Errorf("unsupported jdbc type %q", inst.Type)
}
