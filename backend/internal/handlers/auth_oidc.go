package handlers

import (
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/rs/zerolog/log"
)

// Name of the cookie that binds an in-flight OIDC login to this browser. Holds a
// signed JWT with the state nonce + PKCE code_verifier; checked on callback.
const oidcFlowCookie = "sparklabx_oidc_flow"

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

// cached after first successful discovery (endpoints are static per IdP);
// guarded because concurrent first-hit logins would otherwise race on the write.
var (
	oidcDiscoCache *oidcEndpoints
	oidcDiscoMu    sync.RWMutex
)

// discoverOIDC fetches the IdP's .well-known config. The IdP advertises its
// external URLs; the browser-facing authorize endpoint is used as-is, while the
// back-channel endpoints (token/userinfo) are rewritten to the internal issuer
// base when one is configured — this is the local-docker case where the backend
// container can't reach the browser's host:port. In production internal ==
// external and the rewrite is a no-op.
func (h *AuthHandler) discoverOIDC() (*oidcEndpoints, error) {
	oidcDiscoMu.RLock()
	cached := oidcDiscoCache
	oidcDiscoMu.RUnlock()
	if cached != nil {
		return cached, nil
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

	ep := &oidcEndpoints{
		AuthorizationEndpoint: doc.AuthorizationEndpoint,
		TokenEndpoint:         toInternal(doc.TokenEndpoint),
		UserinfoEndpoint:      toInternal(doc.UserinfoEndpoint),
	}
	oidcDiscoMu.Lock()
	oidcDiscoCache = ep
	oidcDiscoMu.Unlock()
	return ep, nil
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

	// CSRF + PKCE, bound to THIS browser. `state` is a signed JWT with a random
	// nonce; the same nonce plus a PKCE code_verifier go into a short-lived
	// HttpOnly cookie. On callback we require the returned state's nonce to equal
	// the cookie's — a signed-but-unbound state alone doesn't stop login-CSRF /
	// authorization-code injection. PKCE additionally binds the code to us.
	nonceRaw := make([]byte, 16)
	if _, err := cryptorand.Read(nonceRaw); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create state"})
		return
	}
	nonce := hex.EncodeToString(nonceRaw)
	verifierRaw := make([]byte, 32)
	if _, err := cryptorand.Read(verifierRaw); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create state"})
		return
	}
	codeVerifier := base64.RawURLEncoding.EncodeToString(verifierRaw)
	challenge := sha256.Sum256([]byte(codeVerifier))
	codeChallenge := base64.RawURLEncoding.EncodeToString(challenge[:])

	exp := time.Now().Add(10 * time.Minute).Unix()
	state, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"nonce": nonce, "typ": "oidc_state", "exp": exp, "iat": time.Now().Unix(),
	}).SignedString([]byte(h.cfg.JWTSecretKey))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create state"})
		return
	}
	// Silent renew: a hidden-iframe call (silent=1) asks the IdP not to prompt
	// (prompt=none). If the user's IdP SSO session is still alive it returns fresh
	// tokens with zero interaction; if not, it errors with login_required and the
	// callback posts that back to the parent (no full-page redirect either way).
	silent := c.Query("silent") == "1"

	flow, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"nonce": nonce, "cv": codeVerifier, "typ": "oidc_flow", "silent": silent, "exp": exp, "iat": time.Now().Unix(),
	}).SignedString([]byte(h.cfg.JWTSecretKey))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create state"})
		return
	}

	// Secure only when the redirect is https (so local-docker http still works);
	// SameSite=Lax so the cookie rides the top-level redirect back from the IdP.
	secure := strings.HasPrefix(h.cfg.OIDCRedirectURL, "https://")
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(oidcFlowCookie, flow, 600, "/api/v1/auth/oidc", "", secure, true)

	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", h.cfg.OIDCClientID)
	q.Set("redirect_uri", h.cfg.OIDCRedirectURL)
	q.Set("scope", h.cfg.OIDCScopes)
	q.Set("state", state)
	q.Set("code_challenge", codeChallenge)
	q.Set("code_challenge_method", "S256")
	if silent {
		q.Set("prompt", "none")
	}
	c.Redirect(http.StatusFound, ep.AuthorizationEndpoint+"?"+q.Encode())
}

// verifyOIDCJWT validates a state/flow JWT signed with the app key (signature +
// expiry) and checks its typ, returning the claims.
func (h *AuthHandler) verifyOIDCJWT(token, wantTyp string) (jwt.MapClaims, error) {
	parsed, err := jwt.Parse(token, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return []byte(h.cfg.JWTSecretKey), nil
	})
	if err != nil || !parsed.Valid {
		return nil, fmt.Errorf("invalid token")
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return nil, fmt.Errorf("invalid claims")
	}
	if typ, _ := claims["typ"].(string); typ != wantTyp {
		return nil, fmt.Errorf("wrong token type")
	}
	return claims, nil
}

// OIDCCallback handles the IdP redirect: validate state, exchange code, fetch
// identity, upsert the admin, issue the app JWT, hand it to the SPA. In silent
// (hidden-iframe) renew it instead postMessages the parent — see oidcRespond.
func (h *AuthHandler) OIDCCallback(c *gin.Context) {
	if !h.cfg.OIDCEnabled() {
		c.JSON(http.StatusNotFound, gin.H{"error": "OIDC SSO not configured"})
		return
	}

	// Read the flow cookie up-front: it tells us whether this is a silent renew,
	// so EVERY exit path (incl. an IdP error like login_required) responds the
	// right way. The cookie also carries the nonce + PKCE verifier checked below.
	silent := false
	var flowClaims jwt.MapClaims
	if flowStr, ferr := c.Cookie(oidcFlowCookie); ferr == nil {
		if fc, verr := h.verifyOIDCJWT(flowStr, "oidc_flow"); verr == nil {
			flowClaims = fc
			silent, _ = fc["silent"].(bool)
		}
	}

	if e := c.Query("error"); e != "" {
		h.oidcRespond(c, silent, "", e+": "+c.Query("error_description"))
		return
	}
	code := c.Query("code")
	state := c.Query("state")
	if code == "" || state == "" {
		h.oidcRespond(c, silent, "", "missing code or state")
		return
	}

	// Verify state (signature + expiry), then bind it to THIS browser: the flow
	// cookie's nonce must match (stops login-CSRF / code injection) and carries
	// the PKCE code_verifier needed to redeem the code.
	stateClaims, err := h.verifyOIDCJWT(state, "oidc_state")
	if err != nil {
		h.oidcRespond(c, silent, "", "invalid or expired state")
		return
	}
	if flowClaims == nil {
		h.oidcRespond(c, silent, "", "missing login session — please retry")
		return
	}
	stateNonce, _ := stateClaims["nonce"].(string)
	flowNonce, _ := flowClaims["nonce"].(string)
	codeVerifier, _ := flowClaims["cv"].(string)
	if stateNonce == "" || stateNonce != flowNonce || codeVerifier == "" {
		h.oidcRespond(c, silent, "", "login session mismatch — please retry")
		return
	}
	// One-shot: clear the cookie now that it's consumed.
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(oidcFlowCookie, "", -1, "/api/v1/auth/oidc", "", strings.HasPrefix(h.cfg.OIDCRedirectURL, "https://"), true)

	ep, err := h.discoverOIDC()
	if err != nil {
		h.oidcRespond(c, silent, "", "OIDC provider unreachable")
		return
	}

	// Exchange the code for tokens over the back-channel.
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", h.cfg.OIDCRedirectURL)
	form.Set("client_id", h.cfg.OIDCClientID)
	form.Set("client_secret", h.cfg.OIDCClientSecret)
	form.Set("code_verifier", codeVerifier) // PKCE — proves we initiated this flow

	tokenResp, err := httpClient.PostForm(ep.TokenEndpoint, form)
	if err != nil {
		h.oidcRespond(c, silent, "", "token exchange failed")
		return
	}
	defer tokenResp.Body.Close()
	if tokenResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(tokenResp.Body)
		log.Error().Int("status", tokenResp.StatusCode).Str("body", string(body)).Msg("OIDC token exchange rejected")
		h.oidcRespond(c, silent, "", "token exchange rejected")
		return
	}
	// We deliberately don't read id_token: identity comes from the userinfo
	// endpoint below, authenticated by this access token over the TLS back-channel
	// (the code was just redeemed with our client_secret + PKCE verifier, so it's
	// trustworthy). Validating id_token against the IdP JWKS is a possible future
	// hardening, but userinfo-over-back-channel is a sound identity source here.
	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.NewDecoder(tokenResp.Body).Decode(&tok); err != nil || tok.AccessToken == "" {
		h.oidcRespond(c, silent, "", "invalid token response")
		return
	}

	// Identity from the userinfo endpoint (access token authenticates the call).
	req, _ := http.NewRequest(http.MethodGet, ep.UserinfoEndpoint, nil)
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	uiResp, err := httpClient.Do(req)
	if err != nil {
		h.oidcRespond(c, silent, "", "userinfo request failed")
		return
	}
	defer uiResp.Body.Close()
	if uiResp.StatusCode != http.StatusOK {
		h.oidcRespond(c, silent, "", "userinfo rejected")
		return
	}
	var info struct {
		Email             string      `json:"email"`
		EmailVerified     interface{} `json:"email_verified"`
		Name              string      `json:"name"`
		PreferredUsername string      `json:"preferred_username"`
	}
	if err := json.NewDecoder(uiResp.Body).Decode(&info); err != nil {
		h.oidcRespond(c, silent, "", "userinfo decode failed")
		return
	}
	if info.Email == "" {
		h.oidcRespond(c, silent, "", "no email in OIDC profile")
		return
	}
	// Reject an explicitly-unverified email (matches the Google path). A missing
	// claim is allowed — some enterprise IdPs omit it for trusted directories.
	if err := checkEmailVerified(info.EmailVerified); err != nil {
		h.oidcRespond(c, silent, "", "your email is not verified at the identity provider")
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
			h.oidcRespond(c, silent, "", "SSO login is not configured. Contact an administrator.")
		case errors.Is(err, errOAuthNotPermitted):
			h.oidcRespond(c, silent, "", "this email is not permitted to login")
		default:
			log.Error().Err(err).Str("email", info.Email).Msg("OIDC login failed")
			h.oidcRespond(c, silent, "", "login failed")
		}
		return
	}

	// Retain the IdP tokens (encrypted) so the kernel can later authenticate to
	// external services (e.g. Trino) as this user via token passthrough. This is
	// also what a silent renew refreshes.
	h.storeOIDCTokens(adminID, tok.AccessToken, tok.RefreshToken, tok.ExpiresIn)

	log.Info().Str("email", info.Email).Str("admin_role", adminRole).Bool("silent", silent).Msg("OIDC login successful")
	h.oidcRespond(c, silent, appToken, "")
}

// oidcRespond finishes the callback. In silent (hidden-iframe) renew it returns
// a tiny HTML page that postMessages the parent window — refreshing only the
// backend's stored passthrough tokens, leaving the app session untouched (no
// token is sent to the parent). Otherwise it redirects the SPA with the app JWT
// on success, or the error, in the URL fragment (fragments never hit servers).
// errMsg == "" means success.
func (h *AuthHandler) oidcRespond(c *gin.Context, silent bool, appToken, errMsg string) {
	if errMsg != "" {
		log.Warn().Str("msg", errMsg).Bool("silent", silent).Msg("OIDC login failed")
	}
	base := strings.TrimRight(h.cfg.OIDCPostLoginRedirect, "/")
	if silent {
		// json.Marshal escapes <,>,& so this is safe to inline in <script>.
		payload, _ := json.Marshal(gin.H{"type": "sparklabx-oidc", "ok": errMsg == "", "error": errMsg})
		origin, _ := json.Marshal(base)
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(http.StatusOK,
			`<!doctype html><meta charset="utf-8"><script>try{window.parent.postMessage(`+
				string(payload)+`,`+string(origin)+`)}catch(e){}</script>`)
		return
	}
	if errMsg == "" {
		c.Redirect(http.StatusFound, base+"/#oidc_token="+url.QueryEscape(appToken))
		return
	}
	c.Redirect(http.StatusFound, base+"/#oidc_error="+url.QueryEscape(errMsg))
}
