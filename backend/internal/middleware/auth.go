package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"

	"github.com/sparklabx/sparklabx/backend/internal/config"
)

// RequireAdmin validates JWT and requires admin_id claim.
func RequireAdmin(cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		claims, err := parseJWT(c, cfg)
		if err != nil {
			return // already aborted
		}

		adminID, ok := claims["admin_id"].(string)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "admin access required"})
			c.Abort()
			return
		}

		c.Set("admin_id", adminID)
		c.Set("role", "admin")
		adminRole, _ := claims["admin_role"].(string)
		if adminRole == "" {
			adminRole = "admin"
		}
		c.Set("admin_role", adminRole)
		// admin_username used for storage prefix (users/<username>/). Storage
		// handler falls back to a DB lookup if missing (legacy tokens).
		if u, ok := claims["admin_username"].(string); ok {
			c.Set("admin_username", u)
		}
		c.Next()
	}
}

// RequireStudent validates JWT and requires user_id claim WITHOUT admin_id.
func RequireStudent(cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		claims, err := parseJWT(c, cfg)
		if err != nil {
			return
		}

		// Reject admin tokens (social login admins have both user_id and admin_id)
		if _, hasAdmin := claims["admin_id"].(string); hasAdmin {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "student access required"})
			c.Abort()
			return
		}

		userID, ok := claims["user_id"].(string)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "student access required"})
			c.Abort()
			return
		}

		c.Set("user_id", userID)
		c.Set("email", claims["email"])
		c.Set("name", claims["name"])
		c.Set("role", "student")
		c.Next()
	}
}

// RequireAuth validates JWT for any role (admin or student).
func RequireAuth(cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		claims, err := parseJWT(c, cfg)
		if err != nil {
			return
		}

		if adminID, ok := claims["admin_id"].(string); ok {
			c.Set("admin_id", adminID)
			c.Set("role", "admin")
			if u, ok := claims["admin_username"].(string); ok {
				c.Set("admin_username", u)
			}
		} else if userID, ok := claims["user_id"].(string); ok {
			c.Set("user_id", userID)
			c.Set("email", claims["email"])
			c.Set("name", claims["name"])
			c.Set("role", "student")
		} else {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
			c.Abort()
			return
		}

		c.Next()
	}
}

// IsSuperAdmin checks if the current admin is a superadmin.
func IsSuperAdmin(c *gin.Context) bool {
	role, _ := c.Get("admin_role")
	return role == "superadmin"
}

// RequireSuperAdmin is a route-level guard that rejects non-superadmin admins
// with 403. Use to protect settings-write endpoints (cloud creds, allowed
// domains) where a regular admin shouldn't be able to mutate global config.
// Composes after RequireAdmin (which sets admin_role on the context).
func RequireSuperAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !IsSuperAdmin(c) {
			c.JSON(http.StatusForbidden, gin.H{"error": "only superadmin can change this setting"})
			c.Abort()
			return
		}
		c.Next()
	}
}

func parseJWT(c *gin.Context, cfg *config.Config) (jwt.MapClaims, error) {
	authHeader := c.GetHeader("Authorization")

	// WebSocket upgrade requests can't send Authorization header reliably in browsers,
	// so allow query-token fallback only for actual WebSocket upgrades.
	if authHeader == "" && websocket.IsWebSocketUpgrade(c.Request) {
		if qToken := c.Query("token"); qToken != "" {
			authHeader = "Bearer " + qToken
		}
	}

	if authHeader == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing authorization header"})
		c.Abort()
		return nil, jwt.ErrTokenMalformed
	}

	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid authorization header"})
		c.Abort()
		return nil, jwt.ErrTokenMalformed
	}

	token, err := jwt.Parse(parts[1], func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrSignatureInvalid
		}
		return []byte(cfg.JWTSecretKey), nil
	})

	if err != nil || !token.Valid {
		log.Warn().Err(err).Msg("invalid JWT token")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
		c.Abort()
		return nil, err
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid token claims"})
		c.Abort()
		return nil, jwt.ErrTokenMalformed
	}

	return claims, nil
}
