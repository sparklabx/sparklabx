package handlers

import (
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/rs/zerolog/log"

	"github.com/sparklabx/sparklabx/backend/internal/database"
	"github.com/sparklabx/sparklabx/backend/internal/middleware"
)

// Trial access: self-service registration (pending until a superadmin approves),
// time-boxed accounts, and sliding session refresh.

const defaultTrialHours = 4

var emailPattern = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// Register queues a Google account for approval. No password: trial users must sign in
// with Google using this email once approved (password_hash stays empty, so the
// password login can never succeed for these accounts).
// POST /api/v1/auth/register
func (h *AuthHandler) Register(c *gin.Context) {
	if !h.cfg.SignupEnabled {
		c.JSON(http.StatusNotFound, gin.H{"error": "registration is disabled"})
		return
	}
	var req struct {
		Email string `json:"email" binding:"required"`
		Note  string `json:"note"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "email is required"})
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	switch {
	case !emailPattern.MatchString(email) || len(email) > 255:
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid email"})
		return
	case len(req.Note) > 1000:
		c.JSON(http.StatusBadRequest, gin.H{"error": "note is too long"})
		return
	}
	db := database.GetDB()
	var exists bool
	if err := db.QueryRow("SELECT EXISTS(SELECT 1 FROM admins WHERE LOWER(email) = $1)", email).Scan(&exists); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "registration failed"})
		return
	}
	if exists {
		c.JSON(http.StatusConflict, gin.H{"error": "this email is already registered"})
		return
	}
	// Same username/insert path as a first Google login, as a pending account.
	id, username, _, err := upsertOAuthAdmin(db, email, "", "pending")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "registration failed"})
		return
	}
	if _, err := db.Exec("UPDATE admins SET signup_note = $1 WHERE id = $2", strings.TrimSpace(req.Note), id); err != nil {
		log.Warn().Err(err).Msg("failed to store signup note")
	}
	log.Info().Str("username", username).Str("email", email).Msg("trial registration received")
	c.JSON(http.StatusCreated, gin.H{"message": "registration received; once approved, sign in with Google using this email"})
}

// RefreshToken re-issues the session JWT with a fresh expiry (sliding session),
// re-reading the role so promotions/demotions apply. RequireAdmin has already
// verified the token and the account's live access.
// POST /api/v1/auth/refresh
func (h *AuthHandler) RefreshToken(c *gin.Context) {
	adminID := c.GetString("admin_id")
	var username, email, role string
	err := database.GetDB().QueryRow(
		"SELECT username, email, COALESCE(role, 'admin') FROM admins WHERE id = $1", adminID,
	).Scan(&username, &email, &role)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "account not found", "code": middleware.CodeAccountRemoved})
		return
	}
	token, err := h.signSession(jwt.MapClaims{
		"admin_id":       adminID,
		"admin_username": username,
		"admin_role":     role,
		"email":          email,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate token"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"token": token})
}

// signSession adds exp/iat to the claims and signs a session JWT.
func (h *AuthHandler) signSession(claims jwt.MapClaims) (string, error) {
	now := time.Now()
	claims["exp"] = now.Add(time.Duration(h.cfg.JWTExpireMinutes) * time.Minute).Unix()
	claims["iat"] = now.Unix()
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(h.cfg.JWTSecretKey))
}

// SetAccess approves a pending account and/or sets its remaining time.
// hours > 0 → expires now+hours; hours == 0 → no limit. Omitted → default 4h.
// PUT /api/v1/admin/users/:id/access   (superadmin)
func (h *UserManagementHandler) SetAccess(c *gin.Context) {
	var req struct {
		Hours *float64 `json:"hours"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	hours := float64(defaultTrialHours)
	if req.Hours != nil {
		hours = *req.Hours
	}
	if hours < 0 || hours > 24*365 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "hours must be between 0 (no limit) and 8760"})
		return
	}
	var expires interface{} // NULL = no limit
	if hours > 0 {
		expires = time.Now().Add(time.Duration(hours * float64(time.Hour)))
	}
	res, err := database.GetDB().Exec(
		"UPDATE admins SET status = 'active', access_expires_at = $1 WHERE id = $2 AND COALESCE(role, 'admin') <> 'superadmin'",
		expires, c.Param("id"),
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update access"})
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found (superadmins have no time limit)"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "access updated", "access_expires_at": expires})
}
