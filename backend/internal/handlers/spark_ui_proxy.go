package handlers

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/rs/zerolog/log"
)

// Spark UI proxy — exposes each kernel's Spark Web UI (port 4040) through the
// app so users can see DAGs, stages, the SQL tab (execution plans) and metrics
// without a port-forward. The UI is loaded in an iframe, so auth comes via a
// ?token= query param (browsers can't set Authorization on an iframe/navigation)
// which we also stash in a path-scoped cookie so the UI's own asset/XHR requests
// authenticate. Spark UI emits absolute links (/jobs, /static, …); we rewrite
// the response so everything resolves under the proxy prefix.
//
// Adapted from the proven api-gateway implementation; the per-user kernel's UI
// is on the same host as its Jupyter gateway, just port 4040 instead of 8888.

const sparkProxyCookie = "spark_proxy_token"

// authSparkUI validates the request's token (query → cookie → Authorization
// header), sets the identity on the context so checkNotebookWriteAccess works,
// and returns false (writing a 401) if it can't. It mirrors the session guards:
// special-purpose tokens (kernel / oidc_state) are rejected.
func (h *LocalKernelHandler) authSparkUI(c *gin.Context) bool {
	tok := c.Query("token")
	if tok == "" {
		if ck, err := c.Cookie(sparkProxyCookie); err == nil {
			tok = ck
		}
	}
	if tok == "" {
		if ah := c.GetHeader("Authorization"); strings.HasPrefix(strings.ToLower(ah), "bearer ") {
			tok = ah[len("Bearer "):]
		}
	}
	if tok == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing token"})
		return false
	}

	claims := jwt.MapClaims{}
	parsed, err := jwt.ParseWithClaims(tok, claims, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrSignatureInvalid
		}
		return []byte(h.cfg.JWTSecretKey), nil
	})
	if err != nil || !parsed.Valid {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
		return false
	}
	if typ, _ := claims["typ"].(string); typ != "" && typ != "session" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
		return false
	}

	if v, ok := claims["admin_id"].(string); ok && v != "" {
		c.Set("admin_id", v)
		c.Set("role", "admin")
		if u, ok := claims["admin_username"].(string); ok {
			c.Set("admin_username", u)
		}
		if r, ok := claims["admin_role"].(string); ok && r != "" {
			c.Set("admin_role", r)
		}
	} else if v, ok := claims["user_id"].(string); ok && v != "" {
		c.Set("user_id", v)
		c.Set("role", "student")
	} else {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
		return false
	}
	return true
}

// ProxySparkUI reverse-proxies the kernel's Spark UI (:4040) for a notebook.
// Route: GET /api/v1/notebooks/:id/kernel/spark-ui/*path
func (h *LocalKernelHandler) ProxySparkUI(c *gin.Context) {
	if !h.authSparkUI(c) {
		return
	}
	notebookID := c.Param("id")
	if !checkNotebookWriteAccess(c, notebookID) {
		return
	}
	proxyPath := c.Param("path")
	if proxyPath == "" {
		proxyPath = "/"
	}
	// Source maps are large and useless here — skip them.
	if strings.HasSuffix(proxyPath, ".map") {
		c.Status(http.StatusNotFound)
		return
	}

	// Persist the token (from the iframe's ?token=) in a path-scoped cookie so
	// the UI's own asset/XHR requests — which carry no query param — authenticate.
	if token := c.Query("token"); token != "" {
		c.SetSameSite(http.SameSiteLaxMode)
		c.SetCookie(sparkProxyCookie, token, 3600, "/api/v1/notebooks", "", false, false)
	}

	userID := userIDString(c)
	h.gateway.Touch(userID)
	gatewayURL, ok := h.gatewayURLFor(c, userID)
	if !ok {
		return
	}
	// Same host as the Jupyter gateway, Spark UI port instead of 8888.
	targetURLStr := strings.Replace(gatewayURL, ":8888", ":4040", 1)
	targetURL, err := url.Parse(targetURLStr)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "invalid spark ui target"})
		return
	}

	proxyRoot := "/api/v1/notebooks/" + notebookID + "/kernel/spark-ui"
	proxy := httputil.NewSingleHostReverseProxy(targetURL)

	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = targetURL.Host
		req.Header.Set("Host", targetURL.Host)
		req.Header.Del("X-Forwarded-Host")
		req.Header.Del("X-Forwarded-For")
		req.Header.Set("X-Forwarded-Proto", "http")
		// Ask upstream for plain text so we can rewrite the body.
		req.Header.Del("Accept-Encoding")
		req.URL.Path = proxyPath
	}

	proxy.ModifyResponse = func(resp *http.Response) error {
		contentType := resp.Header.Get("Content-Type")

		if strings.Contains(proxyPath, "/static/") {
			resp.Header.Set("Cache-Control", "public, max-age=3600")
			// Mustache templates must pass through untouched.
			if strings.HasSuffix(proxyPath, "-template.html") {
				return nil
			}
			isJS := strings.HasSuffix(proxyPath, ".js") &&
				(strings.Contains(contentType, "javascript") || strings.Contains(contentType, "text/plain"))
			if !isJS {
				return nil // CSS / images / fonts pass through
			}
		}

		// Rewrite redirect Location to stay under the proxy.
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			if location := resp.Header.Get("Location"); location != "" {
				if strings.HasPrefix(location, "http") {
					if locURL, perr := url.Parse(location); perr == nil {
						location = locURL.Path
						if locURL.RawQuery != "" {
							location += "?" + locURL.RawQuery
						}
					}
				}
				if strings.HasPrefix(location, "/") {
					if location == "/" {
						resp.Header.Set("Location", proxyRoot+"/")
					} else {
						resp.Header.Set("Location", proxyRoot+location)
					}
				}
			}
		}

		needsRewrite := strings.Contains(contentType, "text/html") ||
			strings.Contains(contentType, "text/javascript") ||
			strings.Contains(contentType, "application/javascript") ||
			strings.Contains(contentType, "text/plain")
		if !needsRewrite || resp.Body == nil {
			return nil
		}

		bodyReader := resp.Body
		if resp.Header.Get("Content-Encoding") == "gzip" {
			gz, gerr := gzip.NewReader(resp.Body)
			if gerr != nil {
				return nil
			}
			bodyReader = gz
			resp.Header.Del("Content-Encoding")
		}
		bodyBytes, rerr := io.ReadAll(bodyReader)
		if rerr != nil {
			return rerr
		}
		resp.Body.Close()
		if bodyReader != resp.Body {
			bodyReader.Close()
		}
		body := string(bodyBytes)

		if strings.Contains(contentType, "javascript") || strings.Contains(contentType, "text/plain") {
			body = regexp.MustCompile(`(["'])/api/v1/([^"'\s]*)`).
				ReplaceAllString(body, `$1`+proxyRoot+`/api/v1/$2`)
			body = regexp.MustCompile(`(["'])https?://[^"'\s]*/api/v1/([^"'\s]*)`).
				ReplaceAllString(body, `$1`+proxyRoot+`/api/v1/$2`)
			body = regexp.MustCompile(`(["'])/(jobs|stages|storage|environment|executors|SQL)(/[^"'\s]*)?`).
				ReplaceAllString(body, `$1`+proxyRoot+`/$2$3`)
		}

		if strings.Contains(contentType, "text/html") {
			body = regexp.MustCompile(`(href|src|action)\s*=\s*(["'])/([^"'\s>]*)`).
				ReplaceAllString(body, `$1=$2`+proxyRoot+`/$3`)
			inject := `<script>(function(){if(typeof jQuery==='undefined')return;var pr='` + proxyRoot + `';` +
				`function rw(u){if(!u||typeof u!=='string')return u;if(u.indexOf(pr)===0)return u;` +
				`if(u.match(/^https?:\/\/[^\/]+\//)){var p=new URL(u);return pr+p.pathname+p.search+p.hash;}` +
				`if(u.match(/^\//))return pr+u;return u;}` +
				`var a=jQuery.ajax;jQuery.ajax=function(u,o){if(typeof u==='string')u=rw(u);else if(u&&u.url)u.url=rw(u.url);return a.call(this,u,o);};` +
				`var g=jQuery.get;jQuery.get=function(u,d,s,t){u=rw(u);return g.call(this,u,d,s,t);};` +
				`var gj=jQuery.getJSON;jQuery.getJSON=function(u,d,s){u=rw(u);return gj.call(this,u,d,s);};})();</script>`
			if strings.Contains(body, "</head>") {
				body = strings.Replace(body, "</head>", inject+"</head>", 1)
			} else {
				body = regexp.MustCompile(`<body[^>]*>`).ReplaceAllString(body, `$0`+inject)
			}
		}

		resp.Body = io.NopCloser(bytes.NewBufferString(body))
		resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
		resp.Header.Del("Transfer-Encoding")
		return nil
	}

	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		log.Warn().Err(err).Msg("spark UI proxy error")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("Spark UI unavailable — is a Spark session running?"))
	}

	proxy.ServeHTTP(c.Writer, c.Request)
}
