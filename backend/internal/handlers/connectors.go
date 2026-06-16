package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
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
	ID           string
	Label        string
	Icon         string
	DriverClass  string
	MetaStrategy string // "trino-show" | "jdbc-information-schema" | "api"
	DefaultAuth  string // "app-jwt" | "idp-passthrough" | ...
}

var connectorTypes = map[string]ConnectorType{
	"trino": {
		ID: "trino", Label: "Trino", Icon: "trino",
		DriverClass:  "io.trino.jdbc.TrinoDriver",
		MetaStrategy: "trino-show", DefaultAuth: "app-jwt",
	},
	// postgres / mysql / bigquery / … land here as each connector is added.
}

// ConnectorInstance is a configured, enabled connection (per-deployment).
type ConnectorInstance struct {
	ID    string
	Type  string
	Label string
	URL   string
	Auth  string
}

func (i ConnectorInstance) metaStrategy() string { return connectorTypes[i.Type].MetaStrategy }
func (i ConnectorInstance) icon() string         { return connectorTypes[i.Type].Icon }

// connectorInstances builds the active connectors from config. For now the only
// source is TRINO_URL (back-compat); a CONNECTORS JSON list can extend this later.
func (h *AuthHandler) connectorInstances() []ConnectorInstance {
	var out []ConnectorInstance
	if h.cfg.KernelTrinoURL != "" {
		auth := h.cfg.TrinoAuth
		if auth == "" {
			auth = connectorTypes["trino"].DefaultAuth
		}
		out = append(out, ConnectorInstance{ID: "trino", Type: "trino", Label: "Trino", URL: h.cfg.KernelTrinoURL, Auth: auth})
	}
	return out
}

// ConnectorsKernelManifest is the JSON injected into kernels (SPARKLABX_CONNECTORS)
// so the generic data helpers can build a reader per connector: {id, driver, url}.
// Credentials are fetched per query from /connectors/:id/credentials, not here.
func (h *AuthHandler) ConnectorsKernelManifest() string {
	type entry struct {
		ID     string `json:"id"`
		Driver string `json:"driver"`
		URL    string `json:"url"`
	}
	var list []entry
	for _, inst := range h.connectorInstances() {
		list = append(list, entry{ID: inst.ID, Driver: connectorTypes[inst.Type].DriverClass, URL: inst.URL})
	}
	if len(list) == 0 {
		return ""
	}
	b, _ := json.Marshal(list)
	return string(b)
}

func (h *AuthHandler) connectorByID(id string) (ConnectorInstance, bool) {
	for _, inst := range h.connectorInstances() {
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
	insts := h.connectorInstances()
	out := make([]gin.H, 0, len(insts))
	for _, inst := range insts {
		out = append(out, gin.H{
			"id": inst.ID, "label": inst.Label, "icon": inst.icon(),
			"kind": inst.Type, "auth": inst.Auth,
		})
	}
	c.JSON(http.StatusOK, gin.H{"connectors": out})
}

// ConnectorCredentials returns a fresh bearer credential for a connector,
// resolved as the calling user. Called by the kernel (RequireKernelToken) per
// query; generalizes /kernel/oidc-token across connectors + auth strategies.
func (h *AuthHandler) ConnectorCredentials(c *gin.Context) {
	inst, ok := h.connectorByID(c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "unknown connector"})
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

// ConnectorMetadata lists catalogs / schemas / tables for the sidebar browser,
// as the logged-in user. RequireAdmin. Lazy by ?catalog=&schema=.
func (h *AuthHandler) ConnectorMetadata(c *gin.Context) {
	inst, ok := h.connectorByID(c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "unknown connector"})
		return
	}
	adminID := c.GetString("admin_id")
	token, _, ssoExpired, principal := h.resolveConnectorBearer(inst, adminID)
	if token == "" {
		c.JSON(http.StatusOK, gin.H{
			"enabled": true, "items": []string{},
			"sso_expired": ssoExpired, "needs_sso": !ssoExpired,
		})
		return
	}
	if principal == "" {
		principal = c.GetString("admin_username")
	}

	items, level, err := h.connectorMetadata(inst, token, principal, c.Query("catalog"), c.Query("schema"))
	if err != nil {
		log.Warn().Err(err).Str("connector", inst.ID).Msg("connector metadata query failed")
		c.JSON(http.StatusBadGateway, gin.H{"error": "metadata query failed"})
		return
	}
	if items == nil {
		items = []string{}
	}
	c.JSON(http.StatusOK, gin.H{"enabled": true, "level": level, "items": items})
}

// connectorMetadata dispatches to the right metadata adapter for the connector's
// strategy and returns the items + the level name.
func (h *AuthHandler) connectorMetadata(inst ConnectorInstance, token, user, catalog, schema string) ([]string, string, error) {
	switch inst.metaStrategy() {
	case "trino-show":
		base, insecure, ok := trinoHTTPBaseFrom(inst.URL)
		if !ok {
			return nil, "", fmt.Errorf("invalid trino url")
		}
		var stmt, level string
		switch {
		case catalog == "":
			stmt, level = "SHOW CATALOGS", "catalog"
		case schema == "":
			stmt, level = "SHOW SCHEMAS FROM "+quoteTrinoIdent(catalog), "schema"
		default:
			stmt, level = "SHOW TABLES FROM "+quoteTrinoIdent(catalog)+"."+quoteTrinoIdent(schema), "table"
		}
		items, err := trinoShow(base, insecure, token, user, stmt)
		return items, level, err
	default:
		return nil, "", fmt.Errorf("unsupported metadata strategy %q", inst.metaStrategy())
	}
}
