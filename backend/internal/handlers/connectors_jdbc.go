package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	_ "github.com/go-sql-driver/mysql"

	"github.com/sparklabx/sparklabx/backend/internal/database"
)

// JDBC catalog browser for SQL databases (Postgres, MySQL). The backend connects
// with the connector's stored broker-mapped credentials and reads
// information_schema. Two levels: schemas (no selector) then tables (?catalog=
// carries the chosen schema). System schemas are hidden. Browsing uses a plain
// connection (no TLS unless the URL sets it) suitable for internal databases;
// notebooks still query via the full JDBC URL through Spark.

const jdbcBrowseTimeout = 8 * time.Second

// jdbcMetadata drills down by path: [] → schemas, [schema] → tables,
// [schema, table] → columns ("name (type)").
func (h *AuthHandler) jdbcMetadata(inst ConnectorInstance, path []string) (items []string, level string, err error) {
	password := ""
	if inst.PasswordEnc != "" && h.iam != nil {
		if pw, derr := h.iam.DecryptSecret(inst.PasswordEnc); derr == nil {
			password = pw
		} else {
			return nil, "", fmt.Errorf("decrypt connector credential: %w", derr)
		}
	}
	driver, dsn, err := jdbcDriverDSN(inst, password)
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

	// $1.. (lib/pq) vs ? (go-sql-driver) placeholders differ by driver.
	ph := func(n int) string {
		if inst.Type == "postgres" {
			return fmt.Sprintf("$%d", n)
		}
		return "?"
	}

	var query string
	var args []any
	var withType bool
	switch len(path) {
	case 0:
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
	case 1:
		level = "table"
		query = `SELECT table_name FROM information_schema.tables WHERE table_schema = ` + ph(1) + ` ORDER BY 1`
		args = []any{path[0]}
	default:
		level, withType = "column", true
		query = `SELECT column_name, data_type FROM information_schema.columns
		         WHERE table_schema = ` + ph(1) + ` AND table_name = ` + ph(2) + ` ORDER BY ordinal_position`
		args = []any{path[0], path[1]}
	}

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", fmt.Errorf("%s metadata query: %w", inst.Type, err)
	}
	defer rows.Close()
	for rows.Next() {
		if withType {
			var name, typ string
			if err := rows.Scan(&name, &typ); err != nil {
				return nil, "", err
			}
			items = append(items, name+" ("+typ+")")
		} else {
			var name string
			if err := rows.Scan(&name); err != nil {
				return nil, "", err
			}
			items = append(items, name)
		}
	}
	return items, level, rows.Err()
}

// TestConnector tries a connection with the supplied (unsaved) config and
// reports ok/error, so the Add/Edit dialog can verify before saving. On edit
// with a blank password the stored one is used. RequireAdmin.
func (h *AuthHandler) TestConnector(c *gin.Context) {
	var req createConnectorReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	typ, ok := connectorTypes[req.Type]
	if !ok {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": "unknown connector type"})
		return
	}
	if req.Auth == "" {
		req.Auth = typ.DefaultAuth
	}
	inst := ConnectorInstance{ID: req.ID, Type: req.Type, URL: strings.TrimSpace(req.URL), Auth: req.Auth, Username: req.Username}

	switch typ.MetaStrategy {
	case "jdbc-information-schema":
		pw := req.Password
		if pw == "" && req.ID != "" && h.iam != nil {
			// Edit with blank password → test with the stored credential.
			var enc string
			_ = database.GetDB().QueryRow(
				`SELECT password_enc FROM connectors WHERE id = $1 AND (owner_id = '' OR owner_id = $2)`,
				req.ID, c.GetString("admin_id")).Scan(&enc)
			if enc != "" {
				if p, err := h.iam.DecryptSecret(enc); err == nil {
					pw = p
				}
			}
		}
		driver, dsn, err := jdbcDriverDSN(inst, pw)
		if err != nil {
			c.JSON(http.StatusOK, gin.H{"ok": false, "error": err.Error()})
			return
		}
		db, err := sql.Open(driver, dsn)
		if err != nil {
			c.JSON(http.StatusOK, gin.H{"ok": false, "error": err.Error()})
			return
		}
		defer db.Close()
		ctx, cancel := context.WithTimeout(context.Background(), jdbcBrowseTimeout)
		defer cancel()
		if err := db.PingContext(ctx); err != nil {
			c.JSON(http.StatusOK, gin.H{"ok": false, "error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "message": "Connected"})

	case "trino-show":
		token, _, ssoExpired, principal := h.resolveConnectorBearer(inst, c.GetString("admin_id"))
		if token == "" {
			c.JSON(http.StatusOK, gin.H{"ok": false, "error": "sign in with SSO to test this connector", "sso_expired": ssoExpired})
			return
		}
		base, insecure, ok := trinoHTTPBaseFrom(inst.URL)
		if !ok {
			c.JSON(http.StatusOK, gin.H{"ok": false, "error": "invalid trino url"})
			return
		}
		if _, err := trinoShow(base, insecure, token, principal, "SHOW CATALOGS"); err != nil {
			c.JSON(http.StatusOK, gin.H{"ok": false, "error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "message": "Connected"})

	default:
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": "no connection test for this connector type"})
	}
}

// jdbcDriverDSN converts the connector's JDBC URL + the given plaintext password
// into a Go database/sql driver name and DSN.
func jdbcDriverDSN(inst ConnectorInstance, password string) (driver, dsn string, err error) {
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
