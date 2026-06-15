package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/rs/zerolog/log"

	"github.com/sparklabx/sparklabx/backend/internal/database"
)

// MintKernelToken issues a short-lived bearer token the kernel uses to call
// back to GET /kernel/oidc-token for a freshly-refreshed OIDC access token.
// It carries admin_id so the standard RequireAdmin middleware authenticates it
// as the user — scoped to the user's own kernel, same trust as the user's own
// session token.
func (h *AuthHandler) MintKernelToken(adminID string) (string, error) {
	claims := jwt.MapClaims{
		"admin_id": adminID,
		"role":     "admin",
		"typ":      "kernel",
		"exp":      time.Now().Add(12 * time.Hour).Unix(),
		"iat":      time.Now().Unix(),
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(h.cfg.JWTSecretKey))
}

// KernelOIDCToken returns a currently-valid OIDC access token for the calling
// user, refreshed server-side if needed. The kernel's data helpers (e.g.
// trino()) call this per query so they never hold an expired token — the
// refresh token never leaves the backend. Returns an empty token (200) for
// non-SSO logins; callers then simply skip the Authorization.
func (h *AuthHandler) KernelOIDCToken(c *gin.Context) {
	adminID := c.GetString("admin_id")
	if adminID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	token, err := h.ValidOIDCAccessToken(adminID)
	if err != nil {
		log.Warn().Err(err).Str("admin_id", adminID).Msg("kernel oidc-token fetch failed")
		c.JSON(http.StatusOK, gin.H{"access_token": ""})
		return
	}
	c.JSON(http.StatusOK, gin.H{"access_token": token})
}

// OIDC token broker — retains the IdP access/refresh tokens from an SSO login
// (encrypted at rest, reusing the same AES-GCM key as the MinIO secrets) so the
// kernel can authenticate to external services (e.g. Trino) as the logged-in
// user via token passthrough. The kernel-spawn path calls ValidOIDCAccessToken
// to fetch a fresh token per user.

// storeOIDCTokens persists the IdP tokens for an admin. No-op when encryption
// isn't configured or there's nothing to store.
func (h *AuthHandler) storeOIDCTokens(adminID, accessToken, refreshToken string, expiresIn int) {
	if h.iam == nil || accessToken == "" {
		return
	}
	accEnc, err := h.iam.EncryptSecret(accessToken)
	if err != nil {
		log.Error().Err(err).Msg("encrypt OIDC access token failed")
		return
	}
	refEnc := ""
	if refreshToken != "" {
		if refEnc, err = h.iam.EncryptSecret(refreshToken); err != nil {
			log.Error().Err(err).Msg("encrypt OIDC refresh token failed")
			return
		}
	}
	expiresAt := time.Now().Add(time.Duration(expiresIn) * time.Second)
	if _, err := database.GetDB().Exec(
		`UPDATE admins SET oidc_access_token_enc = $1, oidc_refresh_token_enc = $2, oidc_token_expires_at = $3 WHERE id = $4`,
		accEnc, refEnc, expiresAt, adminID,
	); err != nil {
		log.Error().Err(err).Str("admin_id", adminID).Msg("store OIDC tokens failed")
	}
}

// ValidOIDCAccessToken returns a currently-valid OIDC access token for the
// admin, refreshing via the stored refresh token if it has (nearly) expired.
// Returns ("", nil) when there's no usable stored token (logged in via
// password/Google/Microsoft, or refresh failed) — callers treat that as
// "no token, skip passthrough" rather than a hard error.
func (h *AuthHandler) ValidOIDCAccessToken(adminID string) (string, error) {
	if h.iam == nil {
		return "", nil
	}
	var accEnc, refEnc string
	var expiresAt sql.NullTime
	err := database.GetDB().QueryRow(
		`SELECT oidc_access_token_enc, oidc_refresh_token_enc, oidc_token_expires_at FROM admins WHERE id = $1`,
		adminID,
	).Scan(&accEnc, &refEnc, &expiresAt)
	if err != nil || accEnc == "" {
		return "", nil // no stored token
	}

	// Still valid (30s safety margin)? Use it as-is.
	if expiresAt.Valid && time.Now().Add(30*time.Second).Before(expiresAt.Time) {
		return h.iam.DecryptSecret(accEnc)
	}

	// Expired/expiring — refresh if we can.
	if refEnc == "" {
		return "", nil
	}
	refreshToken, err := h.iam.DecryptSecret(refEnc)
	if err != nil {
		return "", err
	}
	access, newRefresh, expiresIn, err := h.refreshOIDCToken(refreshToken)
	if err != nil {
		log.Warn().Err(err).Str("admin_id", adminID).Msg("OIDC token refresh failed")
		return "", nil // don't fail the kernel spawn over a refresh miss
	}
	if newRefresh == "" {
		newRefresh = refreshToken // some IdPs don't rotate the refresh token
	}
	h.storeOIDCTokens(adminID, access, newRefresh, expiresIn)
	return access, nil
}

// refreshOIDCToken exchanges a refresh token for a fresh access token at the IdP.
func (h *AuthHandler) refreshOIDCToken(refreshToken string) (access, refresh string, expiresIn int, err error) {
	ep, err := h.discoverOIDC()
	if err != nil {
		return "", "", 0, err
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", h.cfg.OIDCClientID)
	form.Set("client_secret", h.cfg.OIDCClientSecret)

	resp, err := httpClient.PostForm(ep.TokenEndpoint, form)
	if err != nil {
		return "", "", 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", "", 0, fmt.Errorf("refresh status %d: %s", resp.StatusCode, string(body))
	}
	var t struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return "", "", 0, err
	}
	return t.AccessToken, t.RefreshToken, t.ExpiresIn, nil
}
