package handlers

import (
	"context"
	cryptorand "crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"golang.org/x/crypto/bcrypt"

	"github.com/sparklabx/sparklabx/backend/internal/config"
	"github.com/sparklabx/sparklabx/backend/internal/database"
	"github.com/sparklabx/sparklabx/backend/internal/models"
	"github.com/sparklabx/sparklabx/backend/internal/services"
)

// Shared HTTP client for external API calls (Google, Microsoft, etc.)
var httpClient = &http.Client{Timeout: 10 * time.Second}

// checkEmailVerified validates the email_verified field (can be bool or string).
func checkEmailVerified(val interface{}) error {
	switch v := val.(type) {
	case bool:
		if !v {
			return fmt.Errorf("email not verified")
		}
	case string:
		if v != "true" {
			return fmt.Errorf("email not verified")
		}
	}
	return nil
}

type AuthHandler struct {
	cfg *config.Config
	iam *services.MinIOIAM // nil if MinIO not configured — provisioning skipped
}

func NewAuthHandler(cfg *config.Config, iam *services.MinIOIAM) *AuthHandler {
	return &AuthHandler{cfg: cfg, iam: iam}
}

func (h *AuthHandler) Login(c *gin.Context) {
	var req models.LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	db := database.GetDB()
	var admin models.Admin
	err := db.QueryRow(
		"SELECT id, username, email, password_hash, COALESCE(role, 'admin'), created_at FROM admins WHERE username = $1",
		req.Username,
	).Scan(&admin.ID, &admin.Username, &admin.Email, &admin.PasswordHash, &admin.Role, &admin.CreatedAt)

	if err != nil {
		log.Warn().Str("username", req.Username).Msg("login failed: user not found")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(admin.PasswordHash), []byte(req.Password)); err != nil {
		log.Warn().Str("username", req.Username).Msg("login failed: wrong password")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"admin_id":       admin.ID.String(),
		"admin_username": admin.Username,
		"admin_role":     admin.Role,
		"exp":            time.Now().Add(time.Duration(h.cfg.JWTExpireMinutes) * time.Minute).Unix(),
		"iat":            time.Now().Unix(),
	})

	tokenString, err := token.SignedString([]byte(h.cfg.JWTSecretKey))
	if err != nil {
		log.Error().Err(err).Msg("failed to sign JWT token")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate token"})
		return
	}

	log.Info().Str("username", admin.Username).Msg("admin logged in")
	c.JSON(http.StatusOK, models.LoginResponse{
		Token: tokenString,
		Admin: admin,
	})
}

func (h *AuthHandler) Me(c *gin.Context) {
	adminID := c.GetString("admin_id")

	db := database.GetDB()
	var admin models.Admin
	err := db.QueryRow(
		"SELECT id, username, email, COALESCE(role, 'admin'), created_at FROM admins WHERE id = $1",
		adminID,
	).Scan(&admin.ID, &admin.Username, &admin.Email, &admin.Role, &admin.CreatedAt)

	if err != nil {
		log.Error().Err(err).Str("admin_id", adminID).Msg("failed to fetch admin")
		c.JSON(http.StatusNotFound, gin.H{"error": "admin not found"})
		return
	}

	c.JSON(http.StatusOK, admin)
}

// GoogleLogin verifies a Google token and creates/finds a user.
// Accepts either "credential" (ID token from GoogleLogin component)
// or "access_token" (from useGoogleLogin hook).
func (h *AuthHandler) GoogleLogin(c *gin.Context) {
	var req struct {
		Credential  string `json:"credential"`
		AccessToken string `json:"access_token"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "credential or access_token required"})
		return
	}

	var googleUser *GoogleUserInfo
	var err error

	if req.AccessToken != "" {
		// Verify via Google userinfo API (from useGoogleLogin hook)
		googleUser, err = verifyGoogleAccessToken(req.AccessToken)
	} else if req.Credential != "" {
		// Verify via tokeninfo (from GoogleLogin component)
		googleUser, err = verifyGoogleToken(req.Credential, h.cfg.GoogleClientID)
	} else {
		c.JSON(http.StatusBadRequest, gin.H{"error": "credential or access_token required"})
		return
	}

	if err != nil {
		log.Error().Err(err).Msg("Google token verification failed")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid Google token"})
		return
	}

	db := database.GetDB()
	email := googleUser.Email
	name := googleUser.Name

	// Allowlist check — block before any DB write so spam emails don't create accounts.
	if allowed, anyRule := IsEmailAllowed(db, email); !allowed {
		if !anyRule {
			c.JSON(http.StatusForbidden, gin.H{"error": "OAuth login is not configured. Contact an administrator."})
		} else {
			c.JSON(http.StatusForbidden, gin.H{"error": "this email is not permitted to login"})
		}
		return
	}

	// Lookup or auto-provision admin. In notebook-lite mode every authenticated
	// user is an admin — first login auto-creates a row with role=admin.
	adminID, adminUsername, adminRole, err := upsertOAuthAdmin(db, email, name)
	if err != nil {
		log.Error().Err(err).Str("email", email).Msg("OAuth admin upsert failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create account"})
		return
	}

	claims := jwt.MapClaims{
		"admin_id":       adminID,
		"admin_username": adminUsername,
		"admin_role":     adminRole,
		"email":          email,
		"name":           name,
		"role":           "admin",
		"exp":            time.Now().Add(time.Duration(h.cfg.JWTExpireMinutes) * time.Minute).Unix(),
		"iat":            time.Now().Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := token.SignedString([]byte(h.cfg.JWTSecretKey))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate token"})
		return
	}

	log.Info().Str("email", email).Str("admin_role", adminRole).Msg("Google login successful")
	c.JSON(http.StatusOK, gin.H{
		"token": tokenString,
		"user": gin.H{
			"id":         adminID,
			"email":      email,
			"name":       name,
			"role":       "admin",
			"picture":    googleUser.Picture,
			"is_admin":   true,
			"admin_role": adminRole,
		},
	})
}

// verifyGoogleToken calls Google's tokeninfo endpoint to verify an ID token.
func verifyGoogleToken(idToken, clientID string) (*GoogleUserInfo, error) {
	client := httpClient
	resp, err := client.Get(fmt.Sprintf("https://oauth2.googleapis.com/tokeninfo?id_token=%s", url.QueryEscape(idToken)))
	if err != nil {
		return nil, fmt.Errorf("failed to call Google tokeninfo: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Google token invalid: %s", string(body))
	}

	var info GoogleUserInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("failed to decode Google response: %w", err)
	}

	// Verify audience matches our client ID
	if info.Aud != clientID {
		return nil, fmt.Errorf("token audience mismatch: got %s, want %s", info.Aud, clientID)
	}

	if info.Email == "" {
		return nil, fmt.Errorf("no email in Google token")
	}

	if err := checkEmailVerified(info.EmailVerified); err != nil {
		return nil, err
	}

	return &info, nil
}

type GoogleUserInfo struct {
	Email         string      `json:"email"`
	EmailVerified interface{} `json:"email_verified"`
	Name          string      `json:"name"`
	Picture       string      `json:"picture"`
	Aud           string      `json:"aud"`
	Sub           string      `json:"sub"`
}

// verifyGoogleAccessToken calls Google's userinfo endpoint with an access token.
func verifyGoogleAccessToken(accessToken string) (*GoogleUserInfo, error) {
	client := httpClient
	req, _ := http.NewRequest("GET", "https://www.googleapis.com/oauth2/v3/userinfo", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to call Google userinfo: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Google userinfo error (%d): %s", resp.StatusCode, string(body))
	}

	var info GoogleUserInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("failed to decode Google userinfo: %w", err)
	}

	if info.Email == "" {
		return nil, fmt.Errorf("no email in Google userinfo")
	}

	// Check email_verified (can be bool or string depending on endpoint)
	switch v := info.EmailVerified.(type) {
	case bool:
		if !v {
			return nil, fmt.Errorf("Google email not verified")
		}
	case string:
		if v != "true" {
			return nil, fmt.Errorf("Google email not verified")
		}
	}

	return &info, nil
}

// MicrosoftLogin verifies a Microsoft access token and creates/finds a user.
func (h *AuthHandler) MicrosoftLogin(c *gin.Context) {
	var req struct {
		AccessToken string `json:"access_token" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "access_token required"})
		return
	}

	msUser, err := verifyMicrosoftToken(req.AccessToken)
	if err != nil {
		log.Error().Err(err).Msg("Microsoft token verification failed")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid Microsoft token"})
		return
	}

	db := database.GetDB()
	email := msUser.Email
	name := msUser.DisplayName

	if allowed, anyRule := IsEmailAllowed(db, email); !allowed {
		if !anyRule {
			c.JSON(http.StatusForbidden, gin.H{"error": "OAuth login is not configured. Contact an administrator."})
		} else {
			c.JSON(http.StatusForbidden, gin.H{"error": "this email is not permitted to login"})
		}
		return
	}

	adminID, adminUsername, adminRole, err := upsertOAuthAdmin(db, email, name)
	if err != nil {
		log.Error().Err(err).Str("email", email).Msg("OAuth admin upsert failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create account"})
		return
	}

	claims := jwt.MapClaims{
		"admin_id":       adminID,
		"admin_username": adminUsername,
		"admin_role":     adminRole,
		"email":          email,
		"name":           name,
		"role":           "admin",
		"exp":            time.Now().Add(time.Duration(h.cfg.JWTExpireMinutes) * time.Minute).Unix(),
		"iat":            time.Now().Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := token.SignedString([]byte(h.cfg.JWTSecretKey))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate token"})
		return
	}

	log.Info().Str("email", email).Str("admin_role", adminRole).Msg("Microsoft login successful")
	c.JSON(http.StatusOK, gin.H{
		"token": tokenString,
		"user": gin.H{
			"id":         adminID,
			"email":      email,
			"name":       name,
			"role":       "admin",
			"is_admin":   true,
			"admin_role": adminRole,
		},
	})
}

// upsertOAuthAdmin returns (adminID, username, role) for the email. Auto-creates
// an admin row on first OAuth login — anyone past the email allowlist becomes
// an admin in notebook-lite mode (team internal workspace). The first admin
// ever created is promoted to superadmin via the migration trigger.
//
// Username is derived from the email's local-part (trung.nt@x.com → "trung.nt")
// since this becomes the user's storage prefix (users/<username>/). If the
// local-part is already taken, append a 4-char random suffix.
func upsertOAuthAdmin(db interface {
	QueryRow(string, ...interface{}) *sql.Row
	Exec(string, ...interface{}) (sql.Result, error)
}, email, name string) (id, username, role string, err error) {
	emailLower := strings.ToLower(strings.TrimSpace(email))
	err = db.QueryRow(
		"SELECT id, username, COALESCE(role, 'admin') FROM admins WHERE LOWER(email) = $1",
		emailLower,
	).Scan(&id, &username, &role)
	if err == nil {
		return id, username, role, nil
	}
	// Auto-provision. Derive a friendly username from the email local-part
	// (used as storage prefix). On UNIQUE collision, append a 4-char random
	// suffix and retry once — single retry is enough at our user scale.
	id = uuid.New().String()
	role = "admin"
	username = usernameSlugFromEmail(emailLower)
	_, err = db.Exec(
		"INSERT INTO admins (id, username, email, password_hash, role) VALUES ($1, $2, $3, '', $4)",
		id, username, emailLower, role,
	)
	if err != nil {
		username = username + "-" + randHex(4)
		if _, err = db.Exec(
			"INSERT INTO admins (id, username, email, password_hash, role) VALUES ($1, $2, $3, '', $4)",
			id, username, emailLower, role,
		); err != nil {
			return "", "", "", err
		}
	}
	log.Info().Str("email", emailLower).Str("username", username).Str("name", name).Msg("auto-provisioned admin from OAuth")
	return id, username, role, nil
}

// EnsureUserMinIOSecret returns the caller's MinIO IAM secret (decrypted),
// creating one on first call. Idempotent — the same user always gets back the
// same secret. Returns "" without error if IAM is not configured.
//
// Two-step provisioning (DB row first, IAM second) — if IAM call fails we
// retry next request rather than block login.
func EnsureUserMinIOSecret(iam *services.MinIOIAM, adminID, slug string) (string, error) {
	if iam == nil {
		return "", nil
	}
	db := database.GetDB()
	var enc string
	if err := db.QueryRow("SELECT minio_secret_enc FROM admins WHERE id = $1", adminID).Scan(&enc); err != nil {
		return "", err
	}
	if enc != "" {
		secret, err := iam.DecryptSecret(enc)
		if err != nil {
			return "", err
		}
		// Re-run EnsureUser to repair MinIO state if the server was wiped/re-init.
		// EnsureUser is idempotent — re-setting same secret is a no-op semantically.
		if err := iam.EnsureUser(context.Background(), slug, secret); err != nil {
			log.Warn().Err(err).Str("slug", slug).Msg("MinIO EnsureUser failed on re-attach (continuing)")
		}
		return secret, nil
	}
	secret, err := services.GenerateSecret()
	if err != nil {
		return "", err
	}
	if err := iam.EnsureUser(context.Background(), slug, secret); err != nil {
		return "", fmt.Errorf("provision MinIO user: %w", err)
	}
	encNew, err := iam.EncryptSecret(secret)
	if err != nil {
		return "", err
	}
	if _, err := db.Exec("UPDATE admins SET minio_secret_enc = $1 WHERE id = $2", encNew, adminID); err != nil {
		return "", err
	}
	log.Info().Str("slug", slug).Msg("provisioned MinIO IAM user")
	return secret, nil
}

// usernameSlugFromEmail extracts a URL-safe slug from the email's local-part.
// "Trung.NT+test@TechX.com" → "trung.nt-test". Falls back to "user" if empty.
func usernameSlugFromEmail(emailLower string) string {
	local := emailLower
	if at := strings.IndexByte(local, '@'); at >= 0 {
		local = local[:at]
	}
	if plus := strings.IndexByte(local, '+'); plus >= 0 {
		local = local[:plus]
	}
	// Keep [a-z0-9.-_]; replace others with "-"; collapse runs of "-".
	var b strings.Builder
	prevDash := false
	for _, r := range local {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-'
		if ok {
			b.WriteRune(r)
			prevDash = false
		} else if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	s := strings.Trim(b.String(), ".-_")
	if s == "" {
		return "user"
	}
	return s
}

// randHex returns n random lowercase hex chars (n must be even).
func randHex(n int) string {
	buf := make([]byte, n/2)
	_, _ = cryptorand.Read(buf)
	return hex.EncodeToString(buf)
}

// verifyMicrosoftToken calls Microsoft Graph /me to get user info.
func verifyMicrosoftToken(accessToken string) (*MicrosoftUserInfo, error) {
	client := httpClient
	req, _ := http.NewRequest("GET", "https://graph.microsoft.com/v1.0/me", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to call Microsoft Graph: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Microsoft Graph error (%d): %s", resp.StatusCode, string(body))
	}

	var info MicrosoftUserInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("failed to decode Microsoft response: %w", err)
	}

	// Email: use mail field, fallback to userPrincipalName (personal accounts)
	if info.Email == "" {
		info.Email = info.UserPrincipalName
	}
	if info.Email == "" {
		return nil, fmt.Errorf("no email in Microsoft token")
	}

	return &info, nil
}

type MicrosoftUserInfo struct {
	ID                string `json:"id"`
	DisplayName       string `json:"displayName"`
	Email             string `json:"mail"`
	UserPrincipalName string `json:"userPrincipalName"`
}

