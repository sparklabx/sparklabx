package handlers

import (
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rs/zerolog/log"

	"github.com/sparklabx/sparklabx/backend/internal/database"
)

// Admin-managed connector CRUD (see docs/connectors-design.md). Global, shared
// data sources; only superadmins add/remove them (mirrors allowed_email_rules).
// The TRINO_URL env seed is always present and not deletable here.

var connectorIDRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// ConnectorTypes lists the connector kinds the Add dialog can offer. RequireAdmin.
func (h *AuthHandler) ConnectorTypes(c *gin.Context) {
	out := make([]gin.H, 0, len(connectorTypes))
	for _, t := range connectorTypes {
		out = append(out, gin.H{
			"id": t.ID, "label": t.Label, "icon": t.Icon,
			"browsable":         t.Browsable(),
			"needs_credentials": t.NeedsCredentials(),
			"auth_options":      t.AuthOptions,
			"default_auth":      t.DefaultAuth,
			"driver_package":    t.DriverPackage,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["label"].(string) < out[j]["label"].(string) })
	c.JSON(http.StatusOK, gin.H{"types": out})
}

type createConnectorReq struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Label    string `json:"label"`
	URL      string `json:"url"`
	Auth     string `json:"auth"`
	Username string `json:"username"`
	Password string `json:"password"`
	Scope    string `json:"scope"` // "shared" (org-wide, superadmin only) | "personal" (default)
}

// CreateConnector adds a connector. Any admin may add a personal source; only a
// superadmin may add a shared (org-wide) one.
func (h *AuthHandler) CreateConnector(c *gin.Context) {
	var req createConnectorReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	req.ID = strings.TrimSpace(req.ID)
	req.Label = strings.TrimSpace(req.Label)
	req.URL = strings.TrimSpace(req.URL)

	typ, ok := connectorTypes[req.Type]
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown connector type"})
		return
	}
	if !connectorIDRe.MatchString(req.ID) || len(req.ID) > 64 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id must match ^[a-z][a-z0-9_]*$ (≤64 chars)"})
		return
	}
	if req.Label == "" || req.URL == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "label and url are required"})
		return
	}
	if req.Auth == "" {
		req.Auth = typ.DefaultAuth
	}
	if !contains(typ.AuthOptions, req.Auth) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported auth for this connector type"})
		return
	}

	username, passwordEnc := "", ""
	if req.Auth == "broker-mapped" {
		if h.iam == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "credential storage is unavailable (encryption not configured) — cannot add a username/password connector"})
			return
		}
		if strings.TrimSpace(req.Username) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "username is required for this connector type"})
			return
		}
		username = req.Username
		if req.Password != "" {
			enc, err := h.iam.EncryptSecret(req.Password)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to encrypt credential"})
				return
			}
			passwordEnc = enc
		}
	}

	// Connectors are personal — owned by their creator and visible only to them.
	adminID := c.GetString("admin_id")

	_, err := database.GetDB().Exec(
		`INSERT INTO connectors (id, type, label, url, auth, username, password_enc, added_by, owner_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		req.ID, req.Type, req.Label, req.URL, req.Auth, username, passwordEnc, adminID, adminID,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			c.JSON(http.StatusConflict, gin.H{"error": "you already have a data source with this name"})
			return
		}
		log.Error().Err(err).Msg("create connector failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create connector"})
		return
	}
	log.Info().Str("id", req.ID).Str("type", req.Type).Msg("connector created")
	c.JSON(http.StatusCreated, gin.H{"id": req.ID})
}

// GetConnector returns one connector's editable config (no password) if it is
// visible to the caller. RequireAdmin.
func (h *AuthHandler) GetConnector(c *gin.Context) {
	inst, ok := h.connectorByID(c.GetString("admin_id"), c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "unknown connector"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"id": inst.ID, "type": inst.Type, "label": inst.Label, "url": inst.URL,
		"auth": inst.Auth, "username": inst.Username,
		"has_password": inst.PasswordEnc != "", "editable": true,
	})
}

// UpdateConnector edits a connector's config. Owner-or-superadmin-for-shared.
// id and type are immutable; a blank password keeps the stored one.
func (h *AuthHandler) UpdateConnector(c *gin.Context) {
	id := c.Param("id")
	adminID := c.GetString("admin_id")
	var typ string
	if err := database.GetDB().QueryRow(`SELECT type FROM connectors WHERE id = $1 AND owner_id = $2`, id, adminID).Scan(&typ); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "connector not found"})
		return
	}

	var req createConnectorReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	req.Label = strings.TrimSpace(req.Label)
	req.URL = strings.TrimSpace(req.URL)
	if req.Label == "" || req.URL == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "label and url are required"})
		return
	}
	tdef := connectorTypes[typ]
	if req.Auth == "" {
		req.Auth = tdef.DefaultAuth
	}
	if !contains(tdef.AuthOptions, req.Auth) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported auth for this connector type"})
		return
	}

	username := ""
	if req.Auth == "broker-mapped" {
		if strings.TrimSpace(req.Username) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "username is required for this connector type"})
			return
		}
		username = req.Username
	}

	// Password: only overwrite when a new one is supplied.
	if req.Password != "" && req.Auth == "broker-mapped" {
		if h.iam == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "credential storage is unavailable (encryption not configured)"})
			return
		}
		enc, err := h.iam.EncryptSecret(req.Password)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to encrypt credential"})
			return
		}
		_, err = database.GetDB().Exec(
			`UPDATE connectors SET label=$1, url=$2, auth=$3, username=$4, password_enc=$5 WHERE id=$6 AND owner_id=$7`,
			req.Label, req.URL, req.Auth, username, enc, id, adminID)
		if err != nil {
			log.Error().Err(err).Str("id", id).Msg("update connector failed")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update connector"})
			return
		}
	} else {
		_, err := database.GetDB().Exec(
			`UPDATE connectors SET label=$1, url=$2, auth=$3, username=$4 WHERE id=$5 AND owner_id=$6`,
			req.Label, req.URL, req.Auth, username, id, adminID)
		if err != nil {
			log.Error().Err(err).Str("id", id).Msg("update connector failed")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update connector"})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"id": id})
}

// DeleteConnector removes one of the caller's own connectors.
func (h *AuthHandler) DeleteConnector(c *gin.Context) {
	id := c.Param("id")
	adminID := c.GetString("admin_id")
	res, err := database.GetDB().Exec(`DELETE FROM connectors WHERE id = $1 AND owner_id = $2`, id, adminID)
	if err != nil {
		log.Error().Err(err).Str("id", id).Msg("delete connector failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete connector"})
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "connector not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": id})
}

func contains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}
