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

// MintKernelToken issues a short-lived bearer token the kernel uses to call back
// to GET /kernel/oidc-token for a freshly-refreshed OIDC access token. The
// typ="kernel" claim restricts it to the RequireKernelToken guard only — it is
// NOT a session token and cannot reach the admin API, so a token extracted from
// the kernel pod's env can do nothing but fetch that user's own OIDC token.
func (h *AuthHandler) MintKernelToken(adminID string) (string, error) {
	claims := jwt.MapClaims{
		"admin_id": adminID,
		"typ":      "kernel",
		"exp":      time.Now().Add(12 * time.Hour).Unix(),
		"iat":      time.Now().Unix(),
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(h.cfg.JWTSecretKey))
}

// KernelOIDCToken returns a currently-valid OIDC access token for the calling
// user, refreshed server-side if needed. The kernel's data helpers (e.g.
// trino()) call this only when their cached access token is stale, so the
// refresh token never leaves the backend. The response carries:
//   - access_token: the token (empty if none available)
//   - expires_in:   seconds until it expires, so the kernel can cache it
//   - sso_expired:  true when the user IS an SSO user but their IdP session
//     ended (refresh failed) — lets the helper say "re-login"
//     instead of surfacing a cryptic downstream auth error.
func (h *AuthHandler) KernelOIDCToken(c *gin.Context) {
	adminID := c.GetString("admin_id")
	if adminID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	token, expiresIn, ssoExpired, err := h.ValidOIDCAccessToken(adminID)
	if err != nil {
		log.Warn().Err(err).Str("admin_id", adminID).Msg("kernel oidc-token fetch failed")
	}
	c.JSON(http.StatusOK, gin.H{
		"access_token": token,
		"expires_in":   expiresIn,
		"sso_expired":  ssoExpired,
	})
}

// IsOIDCUser reports whether the admin has ever stored OIDC tokens (i.e. logged
// in via SSO). Used to decide whether to wire SSO passthrough into their kernel
// — true even when the stored token is currently expired, so the kernel can
// reach the backend and report an expired session clearly.
func (h *AuthHandler) IsOIDCUser(adminID string) bool {
	if h.iam == nil {
		return false
	}
	var present bool
	err := database.GetDB().QueryRow(
		`SELECT oidc_access_token_enc <> '' FROM admins WHERE id = $1`, adminID,
	).Scan(&present)
	return err == nil && present
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
// Returns:
//   - token:      the access token ("" when unavailable)
//   - expiresIn:  seconds until it expires (so the kernel can cache it)
//   - ssoExpired: true when the user HAS an OIDC login but it can no longer be
//     refreshed (IdP session ended) — distinct from a non-SSO user,
//     so the kernel can prompt a re-login instead of a vague error
//
// A non-SSO user (password/Google/Microsoft) returns ("", 0, false, nil).
func (h *AuthHandler) ValidOIDCAccessToken(adminID string) (token string, expiresIn int, ssoExpired bool, err error) {
	if h.iam == nil {
		return "", 0, false, nil
	}
	var accEnc, refEnc string
	var expiresAt sql.NullTime
	err = database.GetDB().QueryRow(
		`SELECT oidc_access_token_enc, oidc_refresh_token_enc, oidc_token_expires_at FROM admins WHERE id = $1`,
		adminID,
	).Scan(&accEnc, &refEnc, &expiresAt)
	if err != nil || accEnc == "" {
		return "", 0, false, nil // non-SSO user / no stored token
	}

	// Still valid (30s safety margin)? Use it as-is.
	if expiresAt.Valid && time.Now().Add(30*time.Second).Before(expiresAt.Time) {
		tok, derr := h.iam.DecryptSecret(accEnc)
		if derr != nil {
			return "", 0, true, derr // SSO user but token unusable
		}
		return tok, int(time.Until(expiresAt.Time).Seconds()), false, nil
	}

	// Expired/expiring — refresh if we can. From here on the user IS an SSO user,
	// so any failure is an expired SSO session (ssoExpired=true), not "no token".
	if refEnc == "" {
		return "", 0, true, nil
	}
	refreshToken, derr := h.iam.DecryptSecret(refEnc)
	if derr != nil {
		return "", 0, true, derr
	}
	access, newRefresh, exp, rerr := h.refreshOIDCToken(refreshToken)
	if rerr != nil {
		log.Warn().Err(rerr).Str("admin_id", adminID).Msg("OIDC token refresh failed")
		return "", 0, true, nil // IdP session ended — caller prompts re-login
	}
	if newRefresh == "" {
		newRefresh = refreshToken // some IdPs don't rotate the refresh token
	}
	h.storeOIDCTokens(adminID, access, newRefresh, exp)
	return access, exp, false, nil
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
