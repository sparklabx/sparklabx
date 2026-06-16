package handlers

import (
	"database/sql"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/sparklabx/sparklabx/backend/internal/database"
)

// AllowedDomainHandler manages the OAuth email allowlist.
// Rules are checked at login time in GoogleLogin / MicrosoftLogin.
// When the table is empty, OAuth login is rejected entirely — admins must
// bootstrap via the existing username/password login (/admin/login) and add
// at least one rule before OAuth becomes usable.
type AllowedDomainHandler struct{}

func NewAllowedDomainHandler() *AllowedDomainHandler {
	return &AllowedDomainHandler{}
}

type allowedRule struct {
	ID        uuid.UUID `json:"id"`
	RuleType  string    `json:"rule_type"`
	Value     string    `json:"value"`
	Enabled   bool      `json:"enabled"`
	AddedBy   string    `json:"added_by"`
	Note      string    `json:"note"`
	CreatedAt time.Time `json:"created_at"`
}

type createRuleRequest struct {
	RuleType string `json:"rule_type" binding:"required"`
	Value    string `json:"value" binding:"required"`
	Note     string `json:"note"`
}

type updateRuleRequest struct {
	Enabled *bool   `json:"enabled"`
	Note    *string `json:"note"`
}

var (
	domainRegex = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)
	emailRegex  = regexp.MustCompile(`^[a-z0-9._%+-]+@[a-z0-9.-]+\.[a-z]{2,}$`)

	// Public free-email providers — flagged because adding them effectively
	// opens registration to everyone on that provider. Admin must explicitly
	// opt-in by setting force=true.
	publicEmailProviders = map[string]bool{
		"gmail.com":      true,
		"googlemail.com": true,
		"outlook.com":    true,
		"hotmail.com":    true,
		"live.com":       true,
		"yahoo.com":      true,
		"yahoo.co.uk":    true,
		"icloud.com":     true,
		"me.com":         true,
		"proton.me":      true,
		"protonmail.com": true,
		"aol.com":        true,
		"qq.com":         true,
		"163.com":        true,
	}
)

// List returns all allowlist rules, ordered by most recently created first.
func (h *AllowedDomainHandler) List(c *gin.Context) {
	db := database.GetDB()
	rows, err := db.Query(
		`SELECT id, rule_type, value, enabled, added_by, COALESCE(note, ''), created_at
		 FROM allowed_email_rules
		 ORDER BY created_at DESC`,
	)
	if err != nil {
		log.Error().Err(err).Msg("failed to list allowed email rules")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list rules"})
		return
	}
	defer rows.Close()

	rules := []allowedRule{}
	for rows.Next() {
		var r allowedRule
		if err := rows.Scan(&r.ID, &r.RuleType, &r.Value, &r.Enabled, &r.AddedBy, &r.Note, &r.CreatedAt); err != nil {
			log.Error().Err(err).Msg("failed to scan allowed email rule")
			continue
		}
		rules = append(rules, r)
	}

	c.JSON(http.StatusOK, gin.H{"rules": rules})
}

// Create adds a new allowlist rule. Validates type and value format.
// Refuses public free-email providers (gmail.com, outlook.com, ...) unless
// the caller passes ?force=true to explicitly acknowledge the risk.
func (h *AllowedDomainHandler) Create(c *gin.Context) {
	var req createRuleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "rule_type and value required"})
		return
	}

	value := strings.ToLower(strings.TrimSpace(req.Value))
	if value == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "value cannot be empty"})
		return
	}

	switch req.RuleType {
	case "domain":
		// Strip leading "@" if user typed "@company.com"
		value = strings.TrimPrefix(value, "@")
		if !domainRegex.MatchString(value) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid domain format"})
			return
		}
		if publicEmailProviders[value] && c.Query("force") != "true" {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "refusing to add public email provider — this would allow anyone with a " + value + " account. Pass ?force=true to override.",
			})
			return
		}
	case "exact_email":
		if !emailRegex.MatchString(value) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid email format"})
			return
		}
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "rule_type must be 'domain' or 'exact_email'"})
		return
	}

	addedBy := c.GetString("admin_id")

	id := uuid.New()
	_, err := database.GetDB().Exec(
		`INSERT INTO allowed_email_rules (id, rule_type, value, enabled, added_by, note)
		 VALUES ($1, $2, $3, TRUE, $4, $5)`,
		id, req.RuleType, value, addedBy, req.Note,
	)
	if err != nil {
		if strings.Contains(err.Error(), "allowed_email_rules_unique") {
			c.JSON(http.StatusConflict, gin.H{"error": "rule already exists"})
			return
		}
		log.Error().Err(err).Msg("failed to create allowed email rule")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create rule"})
		return
	}

	log.Info().Str("rule_type", req.RuleType).Str("value", value).Str("added_by", addedBy).Msg("allowed email rule created")
	c.JSON(http.StatusCreated, allowedRule{
		ID:        id,
		RuleType:  req.RuleType,
		Value:     value,
		Enabled:   true,
		AddedBy:   addedBy,
		Note:      req.Note,
		CreatedAt: time.Now(),
	})
}

// Update toggles enabled state or updates the note for an existing rule.
func (h *AllowedDomainHandler) Update(c *gin.Context) {
	id := c.Param("id")
	if _, err := uuid.Parse(id); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid rule id"})
		return
	}

	var req updateRuleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	if req.Enabled == nil && req.Note == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "nothing to update"})
		return
	}

	db := database.GetDB()
	if req.Enabled != nil {
		if _, err := db.Exec("UPDATE allowed_email_rules SET enabled = $1 WHERE id = $2", *req.Enabled, id); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update enabled"})
			return
		}
	}
	if req.Note != nil {
		if _, err := db.Exec("UPDATE allowed_email_rules SET note = $1 WHERE id = $2", *req.Note, id); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update note"})
			return
		}
	}

	log.Info().Str("rule_id", id).Str("admin", c.GetString("admin_id")).Msg("allowed email rule updated")
	c.JSON(http.StatusOK, gin.H{"message": "updated"})
}

// Delete removes a rule. Existing JWTs issued under the removed rule remain
// valid until expiry — keep TTLs short or rely on token rotation.
func (h *AllowedDomainHandler) Delete(c *gin.Context) {
	id := c.Param("id")
	if _, err := uuid.Parse(id); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid rule id"})
		return
	}

	res, err := database.GetDB().Exec("DELETE FROM allowed_email_rules WHERE id = $1", id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete rule"})
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "rule not found"})
		return
	}

	log.Info().Str("rule_id", id).Str("admin", c.GetString("admin_id")).Msg("allowed email rule deleted")
	c.JSON(http.StatusOK, gin.H{"message": "deleted"})
}

// IsEmailAllowed checks whether the given email is permitted by any enabled rule.
// Returns (allowed, ruleExists). When no rules exist at all, returns
// (false, false) — OAuth login is closed by default until admin adds rules.
// Match priority: exact_email first, then domain. Case-insensitive.
func IsEmailAllowed(db *sql.DB, email string) (allowed bool, anyRule bool) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return false, false
	}

	// Single query gets both total (any rules at all?) and matches in one round trip.
	var total, match int
	err := db.QueryRow(
		`SELECT
		    COUNT(*) AS total,
		    COUNT(*) FILTER (
		        WHERE (rule_type = 'exact_email' AND lower(value) = $1)
		           OR (rule_type = 'domain' AND $1 LIKE '%@' || lower(value))
		    ) AS matched
		 FROM allowed_email_rules
		 WHERE enabled = TRUE`,
		email,
	).Scan(&total, &match)
	if err != nil {
		log.Error().Err(err).Str("email", email).Msg("failed to check email allowlist")
		return false, false
	}
	if total == 0 {
		return false, false
	}
	return match > 0, true
}
