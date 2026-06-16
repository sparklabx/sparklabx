package handlers

import (
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"
)

// Trino catalog browser — lets the notebook sidebar list the catalogs / schemas
// / tables of the user's connected Trino, queried AS the user via their OIDC
// token (the same passthrough the trino() helper uses). Read-only metadata only.

// trinoHTTPBase derives the Trino HTTP base URL from the configured JDBC URL
// (jdbc:trino://host:port?SSL=true&SSLVerification=NONE → https://host:port) and
// whether to skip TLS verification (self-signed dev certs).
func (h *AuthHandler) trinoHTTPBase() (base string, insecure bool, ok bool) {
	return trinoHTTPBaseFrom(h.cfg.KernelTrinoURL)
}

// trinoHTTPBaseFrom derives the Trino HTTP base + TLS-skip flag from a JDBC URL.
func trinoHTTPBaseFrom(raw string) (base string, insecure bool, ok bool) {
	if raw == "" {
		return "", false, false
	}
	u, err := url.Parse(strings.TrimPrefix(raw, "jdbc:"))
	if err != nil || u.Host == "" {
		return "", false, false
	}
	q := u.Query()
	scheme := "http"
	if strings.EqualFold(q.Get("SSL"), "true") {
		scheme = "https"
	}
	insecure = strings.EqualFold(q.Get("SSLVerification"), "NONE")
	return scheme + "://" + u.Host, insecure, true
}

// jwtPreferredUsername reads preferred_username from a JWT without verifying it
// (the token is the IdP's, already trusted; we only need the principal to send
// as X-Trino-User so it matches Trino's JWT principal-field).
func jwtPreferredUsername(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return ""
	}
	seg := parts[1]
	if m := len(seg) % 4; m != 0 {
		seg += strings.Repeat("=", 4-m)
	}
	payload, err := base64.URLEncoding.DecodeString(seg)
	if err != nil {
		return ""
	}
	var claims struct {
		PreferredUsername string `json:"preferred_username"`
	}
	_ = json.Unmarshal(payload, &claims)
	return claims.PreferredUsername
}

// trinoShow runs a single-column SHOW statement over Trino's HTTP protocol
// (POST /v1/statement, then follow nextUri) and returns the first column as a
// string list. Authenticated as the user via their OIDC access token.
func trinoShow(base string, insecure bool, token, user, sql string) ([]string, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	if insecure {
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	}
	setHeaders := func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+token)
		if user != "" {
			r.Header.Set("X-Trino-User", user)
		}
	}

	req, _ := http.NewRequest(http.MethodPost, base+"/v1/statement", strings.NewReader(sql))
	req.Header.Set("Content-Type", "text/plain")
	setHeaders(req)

	var items []string
	for {
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			msg := string(body)
			if len(msg) > 200 {
				msg = msg[:200]
			}
			return nil, fmt.Errorf("trino http %d: %s", resp.StatusCode, msg)
		}
		var r struct {
			NextURI string          `json:"nextUri"`
			Data    [][]interface{} `json:"data"`
			Error   *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(body, &r); err != nil {
			return nil, err
		}
		if r.Error != nil {
			return nil, fmt.Errorf("trino: %s", r.Error.Message)
		}
		for _, row := range r.Data {
			if len(row) > 0 && row[0] != nil {
				items = append(items, fmt.Sprintf("%v", row[0]))
			}
		}
		if r.NextURI == "" {
			break
		}
		req, _ = http.NewRequest(http.MethodGet, r.NextURI, nil)
		setHeaders(req)
	}
	return items, nil
}

// quoteTrinoIdent double-quotes a Trino identifier (escaping embedded quotes),
// so a catalog/schema name from the request can't break out of its position.
func quoteTrinoIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// TrinoMetadata lists Trino catalogs / schemas / tables for the sidebar browser,
// queried as the logged-in user. Lazy by level:
//
//	(no params)              → SHOW CATALOGS
//	?catalog=c               → SHOW SCHEMAS FROM "c"
//	?catalog=c&schema=s      → SHOW TABLES FROM "c"."s"
func (h *AuthHandler) TrinoMetadata(c *gin.Context) {
	// Thin shim over the generic connector path so the old frontend's
	// /trino/metadata honours the trino instance's auth strategy (app-jwt or
	// idp-passthrough) — resolving the bearer via ValidOIDCAccessToken directly
	// would forward an IdP token that an app-jwt Trino rejects.
	inst, ok := h.connectorByID("trino")
	if !ok {
		c.JSON(http.StatusOK, gin.H{"enabled": false, "items": []string{}})
		return
	}
	token, _, ssoExpired, principal := h.resolveConnectorBearer(inst, c.GetString("admin_id"))
	if token == "" {
		// No credential to present: non-SSO/idp-passthrough users have no
		// identity, or an expired session must re-login.
		c.JSON(http.StatusOK, gin.H{
			"enabled":     true,
			"items":       []string{},
			"sso_expired": ssoExpired,
			"needs_sso":   !ssoExpired,
		})
		return
	}
	if principal == "" {
		principal = c.GetString("admin_username")
	}

	items, level, err := h.connectorMetadata(inst, token, principal, c.Query("catalog"), c.Query("schema"))
	if err != nil {
		log.Warn().Err(err).Msg("trino metadata query failed")
		c.JSON(http.StatusBadGateway, gin.H{"error": "trino query failed"})
		return
	}
	if items == nil {
		items = []string{}
	}
	c.JSON(http.StatusOK, gin.H{"enabled": true, "level": level, "items": items})
}
