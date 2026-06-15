package handlers

import (
	cryptorand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/rs/zerolog/log"
)

// Generic OIDC SSO — provider-agnostic backend authorization-code flow.
//
// Designed so any enterprise IdP (Keycloak, Okta, Auth0, Azure AD, Google, ...)
// works by configuration alone. Google/Microsoft keep their existing dedicated
// handlers for now; this flow is the path a future unification would build on
// (each becomes a pre-configured issuer here).
//
// Flow: GET /auth/oidc/start  → 302 to the IdP authorize endpoint
//       GET /auth/oidc/callback?code&state → exchange code (back-channel),
//       fetch userinfo, upsert admin, issue the SparkLabX app JWT, then 302
//       back to the SPA with the token in the URL fragment.

type oidcEndpoints struct {
	AuthorizationEndpoint string // external (browser-facing)
	TokenEndpoint         string // back-channel (internal base if configured)
	UserinfoEndpoint      string // back-channel (internal base if configured)
}

// cached after first successful discovery (endpoints are static per IdP)
var oidcDiscoCache *oidcEndpoints

// discoverOIDC fetches the IdP's .well-known config. The IdP advertises its
// external URLs; the browser-facing authorize endpoint is used as-is, while the
// back-channel endpoints (token/userinfo) are rewritten to the internal issuer
// base when one is configured — this is the local-docker case where the backend
// container can't reach the browser's host:port. In production internal ==
// external and the rewrite is a no-op.
func (h *AuthHandler) discoverOIDC() (*oidcEndpoints, error) {
	if oidcDiscoCache != nil {
		return oidcDiscoCache, nil
	}
	ext := strings.TrimRight(h.cfg.OIDCIssuerURL, "/")
	internal := strings.TrimRight(h.cfg.OIDCInternalIssuerURL, "/")
	if internal == "" {
		internal = ext
	}

	resp, err := httpClient.Get(internal + "/.well-known/openid-configuration")
	if err != nil {
		return nil, fmt.Errorf("oidc discovery request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("oidc discovery status %d: %s", resp.StatusCode, string(body))
	}

	var doc struct {
		AuthorizationEndpoint string `json:"authorization_endpoint"`
		TokenEndpoint         string `json:"token_endpoint"`
		UserinfoEndpoint      string `json:"userinfo_endpoint"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return nil, fmt.Errorf("oidc discovery decode: %w", err)
	}
	if doc.AuthorizationEndpoint == "" || doc.TokenEndpoint == "" || doc.UserinfoEndpoint == "" {
		return nil, fmt.Errorf("oidc discovery missing endpoints")
	}

	toInternal := func(u string) string {
		if internal != ext {
			return strings.Replace(u, ext, internal, 1)
		}
		return u
	}

	oidcDiscoCache = &oidcEndpoints{
		AuthorizationEndpoint: doc.AuthorizationEndpoint,
		TokenEndpoint:         toInternal(doc.TokenEndpoint),
		UserinfoEndpoint:      toInternal(doc.UserinfoEndpoint),
	}
	return oidcDiscoCache, nil
}

// AuthConfig is a public endpoint the login page reads to decide which SSO
// buttons to render. Keeps OIDC enablement runtime-configurable (env only, no
// frontend rebuild to toggle).
func (h *AuthHandler) AuthConfig(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"oidc": gin.H{
			"enabled":       h.cfg.OIDCEnabled(),
			"provider_name": h.cfg.OIDCProviderName,
		},
	})
}

// OIDCStart kicks off the authorization-code flow.
func (h *AuthHandler) OIDCStart(c *gin.Context) {
	if !h.cfg.OIDCEnabled() {
		c.JSON(http.StatusNotFound, gin.H{"error": "OIDC SSO not configured"})
		return
	}
	ep, err := h.discoverOIDC()
	if err != nil {
		log.Error().Err(err).Msg("OIDC discovery failed")
		c.JSON(http.StatusBadGateway, gin.H{"error": "OIDC provider unreachable"})
		return
	}

	// Stateless CSRF protection: state is a short-lived signed JWT (no server
	// session, no cookie — survives proxies). Verified on callback.
	nonce := make([]byte, 16)
	_, _ = cryptorand.Read(nonce)
	state, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"nonce": hex.EncodeToString(nonce),
		"typ":   "oidc_state",
		"exp":   time.Now().Add(10 * time.Minute).Unix(),
		"iat":   time.Now().Unix(),
	}).SignedString([]byte(h.cfg.JWTSecretKey))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create state"})
		return
	}

	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", h.cfg.OIDCClientID)
	q.Set("redirect_uri", h.cfg.OIDCRedirectURL)
	q.Set("scope", h.cfg.OIDCScopes)
	q.Set("state", state)
	c.Redirect(http.StatusFound, ep.AuthorizationEndpoint+"?"+q.Encode())
}

// OIDCCallback handles the IdP redirect: validate state, exchange code, fetch
// identity, upsert the admin, issue the app JWT, hand it to the SPA.
func (h *AuthHandler) OIDCCallback(c *gin.Context) {
	if !h.cfg.OIDCEnabled() {
		c.JSON(http.StatusNotFound, gin.H{"error": "OIDC SSO not configured"})
		return
	}
	if e := c.Query("error"); e != "" {
		h.oidcRedirectError(c, e+": "+c.Query("error_description"))
		return
	}
	code := c.Query("code")
	state := c.Query("state")
	if code == "" || state == "" {
		h.oidcRedirectError(c, "missing code or state")
		return
	}

	// Verify state signature + expiry.
	if _, err := jwt.Parse(state, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return []byte(h.cfg.JWTSecretKey), nil
	}); err != nil {
		h.oidcRedirectError(c, "invalid or expired state")
		return
	}

	ep, err := h.discoverOIDC()
	if err != nil {
		h.oidcRedirectError(c, "OIDC provider unreachable")
		return
	}

	// Exchange the code for tokens over the back-channel.
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", h.cfg.OIDCRedirectURL)
	form.Set("client_id", h.cfg.OIDCClientID)
	form.Set("client_secret", h.cfg.OIDCClientSecret)

	tokenResp, err := httpClient.PostForm(ep.TokenEndpoint, form)
	if err != nil {
		h.oidcRedirectError(c, "token exchange failed")
		return
	}
	defer tokenResp.Body.Close()
	if tokenResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(tokenResp.Body)
		log.Error().Int("status", tokenResp.StatusCode).Str("body", string(body)).Msg("OIDC token exchange rejected")
		h.oidcRedirectError(c, "token exchange rejected")
		return
	}
	var tok struct {
		AccessToken  string `json:"access_token"`
		IDToken      string `json:"id_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.NewDecoder(tokenResp.Body).Decode(&tok); err != nil || tok.AccessToken == "" {
		h.oidcRedirectError(c, "invalid token response")
		return
	}

	// Identity from the userinfo endpoint (access token authenticates the call).
	req, _ := http.NewRequest(http.MethodGet, ep.UserinfoEndpoint, nil)
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	uiResp, err := httpClient.Do(req)
	if err != nil {
		h.oidcRedirectError(c, "userinfo request failed")
		return
	}
	defer uiResp.Body.Close()
	if uiResp.StatusCode != http.StatusOK {
		h.oidcRedirectError(c, "userinfo rejected")
		return
	}
	var info struct {
		Email             string      `json:"email"`
		EmailVerified     interface{} `json:"email_verified"`
		Name              string      `json:"name"`
		PreferredUsername string      `json:"preferred_username"`
	}
	if err := json.NewDecoder(uiResp.Body).Decode(&info); err != nil {
		h.oidcRedirectError(c, "userinfo decode failed")
		return
	}
	if info.Email == "" {
		h.oidcRedirectError(c, "no email in OIDC profile")
		return
	}

	name := info.Name
	if name == "" {
		name = info.PreferredUsername
	}
	// Shared tail with the Google/Microsoft flows (allowlist → upsert → app JWT).
	appToken, adminID, _, adminRole, err := h.completeOAuthLogin(info.Email, name)
	if err != nil {
		switch {
		case errors.Is(err, errOAuthNotConfigured):
			h.oidcRedirectError(c, "SSO login is not configured. Contact an administrator.")
		case errors.Is(err, errOAuthNotPermitted):
			h.oidcRedirectError(c, "this email is not permitted to login")
		default:
			log.Error().Err(err).Str("email", info.Email).Msg("OIDC login failed")
			h.oidcRedirectError(c, "login failed")
		}
		return
	}

	// Retain the IdP tokens (encrypted) so the kernel can later authenticate to
	// external services (e.g. Trino) as this user via token passthrough.
	h.storeOIDCTokens(adminID, tok.AccessToken, tok.RefreshToken, tok.ExpiresIn)

	log.Info().Str("email", info.Email).Str("admin_role", adminRole).Msg("OIDC login successful")
	// Hand the app JWT to the SPA via the URL fragment — fragments are never sent
	// to servers, so the token stays out of access logs and proxies.
	dest := strings.TrimRight(h.cfg.OIDCPostLoginRedirect, "/") + "/#oidc_token=" + url.QueryEscape(appToken)
	c.Redirect(http.StatusFound, dest)
}

func (h *AuthHandler) oidcRedirectError(c *gin.Context, msg string) {
	log.Warn().Str("msg", msg).Msg("OIDC login failed")
	dest := strings.TrimRight(h.cfg.OIDCPostLoginRedirect, "/") + "/#oidc_error=" + url.QueryEscape(msg)
	c.Redirect(http.StatusFound, dest)
}
