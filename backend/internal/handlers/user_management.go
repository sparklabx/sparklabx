package handlers

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"golang.org/x/crypto/bcrypt"

	"github.com/sparklabx/sparklabx/backend/internal/database"
	"github.com/sparklabx/sparklabx/backend/internal/middleware"
	"github.com/sparklabx/sparklabx/backend/internal/services"
)

type UserManagementHandler struct {
	kernels services.KernelGateway // stops a user's kernel on revoke/delete
	mailer  *services.Mailer       // approve/reject notifications; nil = off
	appURL  string
}

func NewUserManagementHandler(kernels services.KernelGateway, mailer *services.Mailer, appURL string) *UserManagementHandler {
	return &UserManagementHandler{kernels: kernels, mailer: mailer, appURL: appURL}
}

// stopKernel tears down the user's kernel pod/container in the background so a
// revoked or deleted user stops consuming resources right away, not at idle reap.
func (h *UserManagementHandler) stopKernel(adminID string) {
	if h.kernels == nil {
		return
	}
	go func() {
		if err := h.kernels.Destroy(adminID); err != nil {
			log.Warn().Err(err).Str("admin_id", adminID).Msg("failed to stop kernel of revoked user")
		}
	}()
}

// ListAdmins returns all admin accounts.
// GET /api/v1/admin/users
func (h *UserManagementHandler) ListAdmins(c *gin.Context) {
	db := database.GetDB()
	rows, err := db.Query(`SELECT id, username, email, COALESCE(role, 'admin'), created_at, status, access_expires_at, access_hours, signup_note
		FROM admins ORDER BY status = 'pending' DESC, created_at`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list admins"})
		return
	}
	defer rows.Close()

	type Admin struct {
		ID              string     `json:"id"`
		Username        string     `json:"username"`
		Email           string     `json:"email"`
		Role            string     `json:"role"`
		CreatedAt       string     `json:"created_at"`
		Status          string     `json:"status"`
		AccessExpiresAt *time.Time `json:"access_expires_at"`
		AccessHours     *float64   `json:"access_hours"` // granted, starts at first sign-in
		SignupNote      string     `json:"signup_note"`
	}
	var admins []Admin
	for rows.Next() {
		var a Admin
		if err := rows.Scan(&a.ID, &a.Username, &a.Email, &a.Role, &a.CreatedAt, &a.Status, &a.AccessExpiresAt, &a.AccessHours, &a.SignupNote); err != nil {
			log.Error().Err(err).Msg("failed to scan admin")
			continue
		}
		admins = append(admins, a)
	}
	if err := rows.Err(); err != nil {
		log.Error().Err(err).Msg("error iterating admins")
	}
	if admins == nil {
		admins = []Admin{}
	}
	c.JSON(http.StatusOK, admins)
}

// CreateAdmin creates a new admin account.
// POST /api/v1/admin/users
func (h *UserManagementHandler) CreateAdmin(c *gin.Context) {
	if !middleware.IsSuperAdmin(c) {
		c.JSON(http.StatusForbidden, gin.H{"error": "only superadmin can create admin accounts"})
		return
	}

	var req struct {
		Username string `json:"username" binding:"required"`
		Email    string `json:"email"`
		Password string `json:"password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "username and password required"})
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to hash password"})
		return
	}

	id := uuid.New()
	db := database.GetDB()
	// Store email lowercased+trimmed so OAuth lookups (which always lowercase
	// the IdP-provided email) match without needing case-insensitive WHERE.
	normalizedEmail := strings.ToLower(strings.TrimSpace(req.Email))
	_, err = db.Exec(
		"INSERT INTO admins (id, username, email, password_hash, created_at) VALUES ($1, $2, $3, $4, $5)",
		id, req.Username, normalizedEmail, string(hash), time.Now(),
	)
	if err != nil {
		log.Error().Err(err).Msg("failed to create admin")
		c.JSON(http.StatusConflict, gin.H{"error": "username or email already exists"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"id": id, "username": req.Username})
}

// DeleteAdmin deletes an admin account.
// DELETE /api/v1/admin/users/:id
func (h *UserManagementHandler) DeleteAdmin(c *gin.Context) {
	if !middleware.IsSuperAdmin(c) {
		c.JSON(http.StatusForbidden, gin.H{"error": "only superadmin can delete users"})
		return
	}

	id := c.Param("id")

	// Prevent deleting self
	currentID, _ := c.Get("admin_id")
	if currentID == id {
		c.JSON(http.StatusConflict, gin.H{"error": "cannot delete your own account"})
		return
	}

	db := database.GetDB()
	result, err := db.Exec("DELETE FROM admins WHERE id = $1", id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete admin"})
		return
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "admin not found"})
		return
	}
	h.stopKernel(id)
	c.JSON(http.StatusOK, gin.H{"message": "admin deleted"})
}

// UpdateRole changes an admin's role (superadmin only).
// PUT /api/v1/admin/users/:id/role
func (h *UserManagementHandler) UpdateRole(c *gin.Context) {
	if !middleware.IsSuperAdmin(c) {
		c.JSON(http.StatusForbidden, gin.H{"error": "only superadmin can change roles"})
		return
	}

	id := c.Param("id")
	var req struct {
		Role string `json:"role" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "role required"})
		return
	}

	if req.Role != "superadmin" && req.Role != "admin" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "role must be superadmin or admin"})
		return
	}

	// Prevent self-demote
	currentID, _ := c.Get("admin_id")
	if currentID == id && req.Role != "superadmin" {
		c.JSON(http.StatusConflict, gin.H{"error": "cannot demote yourself"})
		return
	}

	db := database.GetDB()
	result, err := db.Exec("UPDATE admins SET role = $1 WHERE id = $2", req.Role, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update role"})
		return
	}
	if n, _ := result.RowsAffected(); n == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "admin not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "role updated", "role": req.Role})
}

// ResetPassword resets an admin's password.
// PUT /api/v1/admin/users/:id/password
// Regular admins can only reset their own password. Only superadmin can reset
// another admin's password — otherwise any admin could elevate by overwriting
// the superadmin's credentials.
func (h *UserManagementHandler) ResetPassword(c *gin.Context) {
	id := c.Param("id")

	callerID, _ := c.Get("admin_id")
	if callerID != id && !middleware.IsSuperAdmin(c) {
		c.JSON(http.StatusForbidden, gin.H{"error": "only superadmin can reset other admins' passwords"})
		return
	}

	var req struct {
		Password string `json:"password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "password required"})
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to hash password"})
		return
	}

	db := database.GetDB()
	result, err := db.Exec("UPDATE admins SET password_hash = $1 WHERE id = $2", string(hash), id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to reset password"})
		return
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "admin not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "password reset"})
}
