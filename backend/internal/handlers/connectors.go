package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"

	"github.com/sparklabx/sparklabx/backend/internal/database"
)

// Generic data-connector layer (see docs/connectors-design.md). Replaces the
// Trino-specific wiring with a registry + a per-connector auth resolver, so any
// connector authenticates via the user's SSO identity regardless of login method.

const connectorTokenTTL = 5 * time.Minute

// ConnectorType is a supported kind of data source (built-in, code-level).
type ConnectorType struct {
	ID            string
	Label         string
	Icon          string
	DriverClass   string
	DriverPackage string   // Maven coord the kernel needs (connect-dialog nudge)
	MetaStrategy  string   // "trino-show" | "none" (no catalog browser yet)
	DefaultAuth   string   // "app-jwt" | "idp-passthrough" | "broker-mapped"
	AuthOptions   []string // selectable auth strategies in the Add dialog
}

// Browsable reports whether this type has a catalog browser (sidebar tree).
func (t ConnectorType) Browsable() bool { return t.MetaStrategy != "" && t.MetaStrategy != "none" }

// NeedsCredentials reports whether the Add dialog must collect a username/password
// (broker-mapped sources store them encrypted; SSO/app-jwt sources don't).
func (t ConnectorType) NeedsCredentials() bool { return t.DefaultAuth == "broker-mapped" }

var connectorTypes = map[string]ConnectorType{
	"trino": {
		ID: "trino", Label: "Trino", Icon: "trino",
		DriverClass:   "io.trino.jdbc.TrinoDriver",
		DriverPackage: "io.trino:trino-jdbc:481",
		MetaStrategy:  "trino-show", DefaultAuth: "app-jwt",
		AuthOptions: []string{"app-jwt", "idp-passthrough", "broker-mapped"},
	},
	"postgres": {
		ID: "postgres", Label: "PostgreSQL", Icon: "postgres",
		DriverClass:   "org.postgresql.Driver",
		DriverPackage: "org.postgresql:postgresql:42.7.4",
		MetaStrategy:  "jdbc-information-schema", DefaultAuth: "broker-mapped",
		AuthOptions: []string{"broker-mapped"},
	},
	"mysql": {
		ID: "mysql", Label: "MySQL", Icon: "mysql",
		DriverClass:   "com.mysql.cj.jdbc.Driver",
		DriverPackage: "com.mysql:mysql-connector-j:9.1.0",
		MetaStrategy:  "jdbc-information-schema", DefaultAuth: "broker-mapped",
		AuthOptions: []string{"broker-mapped"},
	},
	"athena": {
		ID: "athena", Label: "AWS Athena", Icon: "athena",
		DriverClass:   "com.amazon.athena.jdbc.AthenaDriver",
		DriverPackage: "com.amazonaws:athena-jdbc:2025.15.1",
		// No catalog browser: there's no Go Athena driver for the
		// information_schema path, so the sidebar shows a query() hint.
		MetaStrategy: "none", DefaultAuth: "broker-mapped",
		// broker-mapped username/password → the Athena JDBC driver reads them
		// as User=AccessKeyId / Password=SecretAccessKey. (IAM-role / DefaultChain
		// auth — leave creds blank, put CredentialsProvider=DefaultChain in the URL.)
		AuthOptions: []string{"broker-mapped"},
	},
	// bigquery / snowflake / … land here as each connector is added.
}

// ConnectorInstance is a configured, enabled connection (per-deployment).
type ConnectorInstance struct {
	ID          string
	Type        string
	Label       string
	URL         string
	Auth        string
	Username    string // broker-mapped only
	PasswordEnc string // broker-mapped only (AES-GCM)
	OwnerID     string // "" = shared (org-wide); else the admin who owns it (personal)
}

// Shared reports whether the connector is org-wide (vs personal to one admin).
func (i ConnectorInstance) Shared() bool { return i.OwnerID == "" }

func (i ConnectorInstance) metaStrategy() string { return connectorTypes[i.Type].MetaStrategy }
func (i ConnectorInstance) icon() string         { return connectorTypes[i.Type].Icon }

// connectorInstances builds the connectors VISIBLE to userID: shared rows
// (owner_id = ”) + that user's personal rows. The TRINO_URL env connector is
// seeded into this table once at startup, so it's just another row here.
// Queried per call (small table) so the set reflects runtime adds/deletes.
func (h *AuthHandler) connectorInstances(userID string) []ConnectorInstance {
	var out []ConnectorInstance
	rows, err := database.GetDB().Query(
		`SELECT id, type, label, url, auth, username, password_enc, owner_id
		 FROM connectors WHERE owner_id = $1 ORDER BY created_at`, userID)
	if err != nil {
		log.Warn().Err(err).Msg("list connectors failed")
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var c ConnectorInstance
		if err := rows.Scan(&c.ID, &c.Type, &c.Label, &c.URL, &c.Auth, &c.Username, &c.PasswordEnc, &c.OwnerID); err != nil {
			continue
		}
		out = append(out, c)
	}
	return out
}

// ConnectorsKernelManifest is the JSON injected into kernels (SPARKLABX_CONNECTORS)
// so the generic data helpers can build a reader per connector: {id, driver, url}.
// Credentials are fetched per query from /connectors/:id/credentials, not here.
func (h *AuthHandler) ConnectorsKernelManifest(userID string) string {
	type entry struct {
		ID     string `json:"id"`
		Kind   string `json:"kind"`
		Driver string `json:"driver"`
		URL    string `json:"url"`
	}
	var list []entry
	for _, inst := range h.connectorInstances(userID) {
		list = append(list, entry{ID: inst.ID, Kind: inst.Type, Driver: connectorTypes[inst.Type].DriverClass, URL: inst.URL})
	}
	if len(list) == 0 {
		return ""
	}
	b, _ := json.Marshal(list)
	return string(b)
}

// connectorByID returns the connector with id IF it is visible to userID
// (shared or owned by them).
func (h *AuthHandler) connectorByID(userID, id string) (ConnectorInstance, bool) {
	for _, inst := range h.connectorInstances(userID) {
		if inst.ID == id {
			return inst, true
		}
	}
	return ConnectorInstance{}, false
}

// adminIdentity looks up the username/email for an admin id (used to stamp the
// principal into app-minted connector tokens).
func (h *AuthHandler) adminIdentity(adminID string) (username, email string) {
	var u, e sql.NullString
	_ = database.GetDB().QueryRow(`SELECT username, email FROM admins WHERE id = $1`, adminID).Scan(&u, &e)
	return u.String, e.String
}

// resolveConnectorBearer produces the bearer token a connector accepts for this
// user, per the instance's auth strategy. Returns ssoExpired=true when the user
// IS an SSO user whose session can no longer be refreshed (idp-passthrough).
func (h *AuthHandler) resolveConnectorBearer(inst ConnectorInstance, adminID string) (token string, expiresIn int, ssoExpired bool, principal string) {
	switch inst.Auth {
	case "app-jwt":
		if h.keys == nil {
			return "", 0, false, ""
		}
		uname, email := h.adminIdentity(adminID)
		t, err := h.keys.Mint(adminID, uname, email, connectorTokenTTL)
		if err != nil {
			log.Error().Err(err).Msg("mint connector token failed")
			return "", 0, false, ""
		}
		return t, int(connectorTokenTTL.Seconds()), false, uname
	default: // "idp-passthrough"
		t, exp, ssoExp, _ := h.ValidOIDCAccessToken(adminID)
		return t, exp, ssoExp, jwtPreferredUsername(t)
	}
}

// ConnectorJWKS serves the app's public signing key so connectors can validate
// app-minted (app-jwt) tokens. Public, no auth.
func (h *AuthHandler) ConnectorJWKS(c *gin.Context) {
	if h.keys == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "connector signing not configured"})
		return
	}
	c.JSON(http.StatusOK, h.keys.JWKS())
}

// ListConnectors returns the active connectors for the notebook UI (sidebar
// picker, connect dialog). RequireAdmin.
func (h *AuthHandler) ListConnectors(c *gin.Context) {
	adminID := c.GetString("admin_id")
	insts := h.connectorInstances(adminID)
	out := make([]gin.H, 0, len(insts))
	for _, inst := range insts {
		// All connectors are personal (owned by the caller), so always editable/deletable.
		out = append(out, gin.H{
			"id": inst.ID, "label": inst.Label, "icon": inst.icon(),
			"kind": inst.Type, "auth": inst.Auth,
			"browsable": connectorTypes[inst.Type].Browsable(),
			"deletable": true,
		})
	}
	c.JSON(http.StatusOK, gin.H{"connectors": out})
}

// ConnectorCredentials returns a fresh bearer credential for a connector,
// resolved as the calling user. Called by the kernel (RequireKernelToken) per
// query; generalizes /kernel/oidc-token across connectors + auth strategies.
func (h *AuthHandler) ConnectorCredentials(c *gin.Context) {
	inst, ok := h.connectorByID(c.GetString("admin_id"), c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "unknown connector"})
		return
	}
	// broker-mapped: a shared username/password stored encrypted on the connector
	// (not the user's identity). The kernel helper applies it as JDBC user/password.
	if inst.Auth == "broker-mapped" {
		password := ""
		if inst.PasswordEnc != "" && h.iam != nil {
			if pw, err := h.iam.DecryptSecret(inst.PasswordEnc); err == nil {
				password = pw
			} else {
				log.Error().Err(err).Str("connector", inst.ID).Msg("decrypt connector password failed")
			}
		}
		c.JSON(http.StatusOK, gin.H{
			"scheme":      "user-password",
			"username":    inst.Username,
			"password":    password,
			"expires_in":  0,
			"sso_expired": false,
		})
		return
	}

	token, exp, ssoExpired, _ := h.resolveConnectorBearer(inst, c.GetString("admin_id"))
	c.JSON(http.StatusOK, gin.H{
		"scheme":       "bearer",
		"access_token": token,
		"expires_in":   exp,
		"sso_expired":  ssoExpired,
	})
}

// metaPath reads the drill-down from ?catalog=&schema=&table=, returning the
// non-empty prefix (catalog browser navigates one level deeper per request).
func metaPath(c *gin.Context) []string {
	var p []string
	for _, k := range []string{"catalog", "schema", "table"} {
		v := c.Query(k)
		if v == "" {
			break
		}
		p = append(p, v)
	}
	return p
}

// ConnectorMetadata lists catalogs / schemas / tables / columns for the sidebar
// browser. RequireAdmin. Lazy by ?catalog=&schema=&table=.
func (h *AuthHandler) ConnectorMetadata(c *gin.Context) {
	inst, ok := h.connectorByID(c.GetString("admin_id"), c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "unknown connector"})
		return
	}
	path := metaPath(c)
	switch inst.metaStrategy() {
	case "trino-show":
		// Browsed via Basic (broker-mapped) or an app-jwt/IdP bearer per the user.
		authHeader, user, ssoExpired, ok := h.trinoBrowseAuth(inst, c.GetString("admin_id"), c.GetString("admin_username"))
		if !ok {
			c.JSON(http.StatusOK, gin.H{
				"enabled": true, "items": []string{},
				"sso_expired": ssoExpired, "needs_sso": !ssoExpired,
			})
			return
		}
		items, level, err := h.connectorMetadata(inst, authHeader, user, path)
		if err != nil {
			log.Warn().Err(err).Str("connector", inst.ID).Msg("connector metadata query failed")
			c.JSON(http.StatusBadGateway, gin.H{"error": "metadata query failed"})
			return
		}
		if items == nil {
			items = []string{}
		}
		c.JSON(http.StatusOK, gin.H{"enabled": true, "level": level, "items": items})

	case "jdbc-information-schema":
		// Browsed via the connector's shared credentials. Three levels:
		// schemas → tables → columns.
		items, level, err := h.jdbcMetadata(inst, path)
		if err != nil {
			log.Warn().Err(err).Str("connector", inst.ID).Msg("jdbc metadata query failed")
			c.JSON(http.StatusBadGateway, gin.H{"error": "metadata query failed"})
			return
		}
		if items == nil {
			items = []string{}
		}
		c.JSON(http.StatusOK, gin.H{"enabled": true, "level": level, "items": items})

	default:
		// No catalog browser for this type — the FE shows a "use query()" hint.
		c.JSON(http.StatusOK, gin.H{"enabled": true, "browsable": false, "items": []string{}})
	}
}

// connectorMetadata dispatches to the right metadata adapter for the connector's
// strategy. path is the drill-down so far (catalog, schema, table); the returned
// level names what the items are (catalog|schema|table|column).
func (h *AuthHandler) connectorMetadata(inst ConnectorInstance, authHeader, user string, path []string) ([]string, string, error) {
	switch inst.metaStrategy() {
	case "trino-show":
		base, insecure, ok := trinoHTTPBaseFrom(inst.URL)
		if !ok {
			return nil, "", fmt.Errorf("invalid trino url")
		}
		q := func(parts ...string) string {
			qs := make([]string, len(parts))
			for i, p := range parts {
				qs[i] = quoteTrinoIdent(p)
			}
			return strings.Join(qs, ".")
		}
		var stmt, level string
		switch len(path) {
		case 0:
			stmt, level = "SHOW CATALOGS", "catalog"
		case 1:
			stmt, level = "SHOW SCHEMAS FROM "+q(path[0]), "schema"
		case 2:
			stmt, level = "SHOW TABLES FROM "+q(path[0], path[1]), "table"
		default:
			stmt, level = "SHOW COLUMNS FROM "+q(path[0], path[1], path[2]), "column"
		}
		items, err := trinoShow(base, insecure, authHeader, user, stmt)
		return items, level, err
	default:
		return nil, "", fmt.Errorf("unsupported metadata strategy %q", inst.metaStrategy())
	}
}
