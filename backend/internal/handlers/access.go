package handlers

import (
	"database/sql"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/rs/zerolog/log"

	"github.com/sparklabx/sparklabx/backend/internal/database"
	"github.com/sparklabx/sparklabx/backend/internal/middleware"
	"github.com/sparklabx/sparklabx/backend/internal/services"
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
	note := strings.TrimSpace(req.Note)
	// Renewal: an account whose access ran out (time limit or revoke) asks again. It goes
	// back to pending with the new note; its notebooks and files stay. A live or pending
	// account cannot re-apply.
	res, err := db.Exec(
		`UPDATE admins SET status = 'pending', signup_note = $2
		 WHERE LOWER(email) = $1 AND status = 'active' AND COALESCE(role, 'admin') <> 'superadmin'
		   AND access_expires_at <= NOW()`, email, note)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "registration failed"})
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		log.Info().Str("email", email).Msg("trial renewal request received")
		h.notifySuperadmins("Renewal request", email, note)
		c.JSON(http.StatusCreated, gin.H{"message": "request received; once approved, sign in with Google using this email"})
		return
	}
	var exists bool
	if err := db.QueryRow("SELECT EXISTS(SELECT 1 FROM admins WHERE LOWER(email) = $1)", email).Scan(&exists); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "registration failed"})
		return
	}
	if exists {
		c.JSON(http.StatusConflict, gin.H{"error": "this email already has access or a request waiting for approval"})
		return
	}
	// Same username/insert path as a first Google login, as a pending account.
	id, username, _, err := upsertOAuthAdmin(db, email, "", "pending")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "registration failed"})
		return
	}
	if _, err := db.Exec("UPDATE admins SET signup_note = $1 WHERE id = $2", note, id); err != nil {
		log.Warn().Err(err).Msg("failed to store signup note")
	}
	log.Info().Str("username", username).Str("email", email).Msg("trial registration received")
	h.notifySuperadmins("New SparkLabX trial request", email, note)
	c.JSON(http.StatusCreated, gin.H{"message": "registration received; once approved, sign in with Google using this email"})
}

// notifySuperadmins mails every superadmin that a request is waiting for approval.
func (h *AuthHandler) notifySuperadmins(kind, email, note string) {
	rows, err := database.GetDB().Query("SELECT email FROM admins WHERE role = 'superadmin' AND email <> ''")
	if err != nil {
		log.Warn().Err(err).Msg("failed to list superadmins for sign-up notice")
		return
	}
	defer rows.Close()
	if note == "" {
		note = "(none)"
	}
	body := "Hello,\n\nA SparkLabX request (" + kind + ") is waiting for approval.\n\n" +
		"Email: " + email + "\nWhat will you use it for: " + note + "\n\n" +
		"Approve or reject it in User Management:\n" + h.cfg.AppURL + "\n\nThe SparkLabX team\n"
	for rows.Next() {
		var to string
		if rows.Scan(&to) == nil {
			h.Mailer.SendAsync(to, kind+": "+email, body)
		}
	}
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

// SetAccess approves a pending account and/or sets its time limit.
// hours > 0 → expires hours after now, or, if the user has never signed in, hours
// after their first sign-in (access_hours; CheckAccess starts the clock).
// hours == 0 → no limit. Omitted → default 4h.
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
	var limit *float64 // nil = no limit
	if hours > 0 {
		limit = &hours
	}
	// Never signed in (last_seen_at NULL): park the hours, the clock starts at the first
	// sign-in. Otherwise it starts now. Old status from the same statement, to mail only
	// on pending -> active.
	var email, oldStatus string
	var expires *time.Time
	err := database.GetDB().QueryRow(
		`UPDATE admins a SET status = 'active',
		   access_hours      = CASE WHEN a.last_seen_at IS NULL THEN $1::float8 END,
		   access_expires_at = CASE WHEN a.last_seen_at IS NULL THEN NULL ELSE NOW() + $1::float8 * INTERVAL '1 hour' END
		 FROM (SELECT id, status FROM admins WHERE id = $2 FOR UPDATE) old
		 WHERE a.id = old.id AND COALESCE(a.role, 'admin') <> 'superadmin'
		 RETURNING a.email, old.status, a.access_expires_at`,
		limit, c.Param("id"),
	).Scan(&email, &oldStatus, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found (superadmins have no time limit)"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update access"})
		return
	}
	if oldStatus == "pending" {
		access := "with no time limit"
		if limit != nil && expires == nil {
			access = "for " + formatHours(hours) + ", counted from your first sign-in"
		} else if limit != nil {
			access = "for " + formatHours(hours) + ", starting now (until " + expires.UTC().Format("2006-01-02 15:04 UTC") + ")"
		}
		h.mailer.SendAsync(email, "Your SparkLabX trial is approved",
			"Hello,\n\nYour SparkLabX trial request has been approved. You can use SparkLabX "+access+".\n\n"+
				"Sign in with Google using this email address ("+email+"):\n"+h.appURL+"\n\n"+
				"When your time runs out, your notebook kernel is stopped; your notebooks and local files are kept.\n"+
				"Need help or more time? Reply to support@sparklabx.com.\n\nThe SparkLabX team\n")
	}
	c.JSON(http.StatusOK, gin.H{"message": "access updated", "access_expires_at": expires, "access_hours": limit})
}

// formatHours renders 4 → "4 hours", 1 → "1 hour", 0.5 → "0.5 hours".
func formatHours(h float64) string {
	if h == 1 {
		return "1 hour"
	}
	return strconv.FormatFloat(h, 'f', -1, 64) + " hours"
}

// Revoke ends a user's access immediately: access_expires_at = now, so every
// request (checked live) is refused from here on, and their kernel is stopped.
// The account and its data stay; "Set access time" grants access again.
// POST /api/v1/admin/users/:id/revoke   (superadmin)
func (h *UserManagementHandler) Revoke(c *gin.Context) {
	id := c.Param("id")
	res, err := database.GetDB().Exec(
		"UPDATE admins SET access_expires_at = NOW(), access_hours = NULL WHERE id = $1 AND COALESCE(role, 'admin') <> 'superadmin'", id,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to revoke access"})
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found (a superadmin cannot be revoked; demote first)"})
		return
	}
	h.stopKernel(id)
	c.JSON(http.StatusOK, gin.H{"message": "access revoked"})
}

// Reject declines a pending request and mails the applicant. A new sign-up is
// removed (the email can apply again); a renewal (it had a time limit before) only
// goes back to expired, keeping its notebooks and files.
// POST /api/v1/admin/users/:id/reject   (superadmin)
func (h *UserManagementHandler) Reject(c *gin.Context) {
	db := database.GetDB()
	var email string
	err := db.QueryRow(
		`UPDATE admins SET status = 'active' WHERE id = $1 AND status = 'pending' AND access_expires_at IS NOT NULL
		 RETURNING email`, c.Param("id"),
	).Scan(&email)
	if errors.Is(err, sql.ErrNoRows) {
		err = db.QueryRow("DELETE FROM admins WHERE id = $1 AND status = 'pending' RETURNING email", c.Param("id")).Scan(&email)
	}
	if errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "no pending request with this id"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to reject request"})
		return
	}
	h.mailer.SendAsync(email, "Your SparkLabX trial request",
		"Hello,\n\nThank you for your interest in SparkLabX. Unfortunately we cannot approve your trial request at this time.\n\n"+
			"Questions? Reply to support@sparklabx.com.\n\nThe SparkLabX team\n")
	c.JSON(http.StatusOK, gin.H{"message": "request rejected"})
}

// StartExpiredKernelSweeper stops the kernels of accounts whose access ran out on
// its own (the time limit, not a revoke): every minute, any kernel row whose owner
// has expired is destroyed, so it stops consuming the node within ~1 minute
// instead of lingering until the idle reaper. Revoke/delete stop kernels at once.
func StartExpiredKernelSweeper(kernels services.KernelGateway) {
	if kernels == nil {
		return
	}
	go func() {
		for range time.Tick(time.Minute) {
			rows, err := database.GetDB().Query(`
				SELECT p.user_id FROM user_kernel_pods p
				JOIN admins a ON a.id::text = p.user_id
				WHERE a.access_expires_at <= NOW() AND COALESCE(a.role, 'admin') <> 'superadmin'`)
			if err != nil {
				log.Warn().Err(err).Msg("expired-kernel sweep query failed")
				continue
			}
			var ids []string
			for rows.Next() {
				var id string
				if rows.Scan(&id) == nil {
					ids = append(ids, id)
				}
			}
			rows.Close()
			for _, id := range ids {
				if err := kernels.Destroy(id); err != nil {
					log.Warn().Err(err).Str("admin_id", id).Msg("failed to stop kernel of expired user")
				} else {
					log.Info().Str("admin_id", id).Msg("stopped kernel of expired trial user")
				}
			}
		}
	}()
}
