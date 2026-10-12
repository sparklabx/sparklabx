package middleware

import (
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/sparklabx/sparklabx/backend/internal/database"
)

// Error codes the frontend keys off to log the user out with a reason.
const (
	CodeSessionExpired  = "session_expired"
	CodePendingApproval = "pending_approval"
	CodeAccessExpired   = "access_expired"
	CodeAccountRemoved  = "account_removed"
)

// AccessDenial is why an account may not use the app right now (nil = allowed).
type AccessDenial struct {
	Code    string
	Message string
}

// CheckAccess reads the account's live status from the DB, so an approval, a
// changed time limit or a deletion takes effect on the very next request rather
// than when the JWT expires. Superadmins are never time-boxed.
// ponytail: one indexed PK lookup per request; cache it if the API ever gets hot.
func CheckAccess(adminID string) (*AccessDenial, error) {
	var role, status string
	var expires sql.NullTime
	err := database.GetDB().QueryRow(
		"SELECT COALESCE(role, 'admin'), status, access_expires_at FROM admins WHERE id = $1", adminID,
	).Scan(&role, &status, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return &AccessDenial{CodeAccountRemoved, "this account no longer exists"}, nil
	}
	if err != nil {
		return nil, err
	}
	if role == "superadmin" {
		return nil, nil
	}
	if status == "pending" {
		return &AccessDenial{CodePendingApproval, "your registration is waiting for administrator approval"}, nil
	}
	if expires.Valid && !time.Now().Before(expires.Time) {
		return &AccessDenial{CodeAccessExpired, "your trial access has expired"}, nil
	}
	return nil, nil
}

// abortIfDenied enforces CheckAccess on an authenticated request. 401 + code so
// the frontend drops the session and shows the reason on the login page.
func abortIfDenied(c *gin.Context, adminID string) bool {
	denial, err := CheckAccess(adminID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to check account access"})
		c.Abort()
		return true
	}
	if denial != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": denial.Message, "code": denial.Code})
		c.Abort()
		return true
	}
	return false
}
