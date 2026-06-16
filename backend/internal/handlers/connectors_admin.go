package handlers

import (
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/lib/pq"
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
}

// CreateConnector adds a connector. RequireSuperAdmin.
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
	if req.ID == envSeedID {
		c.JSON(http.StatusBadRequest, gin.H{"error": `"trino" is reserved for the TRINO_URL connector`})
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

	_, err := database.GetDB().Exec(
		`INSERT INTO connectors (id, type, label, url, auth, username, password_enc, added_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		req.ID, req.Type, req.Label, req.URL, req.Auth, username, passwordEnc, c.GetString("admin_id"),
	)
	if err != nil {
		if pqErr, ok := err.(*pq.Error); ok && pqErr.Code == "23505" {
			c.JSON(http.StatusConflict, gin.H{"error": "a connector with this id already exists"})
			return
		}
		log.Error().Err(err).Msg("create connector failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create connector"})
		return
	}
	log.Info().Str("id", req.ID).Str("type", req.Type).Msg("connector created")
	c.JSON(http.StatusCreated, gin.H{"id": req.ID})
}

// DeleteConnector removes a connector. RequireSuperAdmin. The env seed is refused.
func (h *AuthHandler) DeleteConnector(c *gin.Context) {
	id := c.Param("id")
	if id == envSeedID {
		c.JSON(http.StatusBadRequest, gin.H{"error": "the TRINO_URL connector is configured via env and cannot be deleted here"})
		return
	}
	res, err := database.GetDB().Exec(`DELETE FROM connectors WHERE id = $1`, id)
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
