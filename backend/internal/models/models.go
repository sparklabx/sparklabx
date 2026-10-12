package models

import (
	"time"

	"github.com/google/uuid"
)

type Admin struct {
	ID           uuid.UUID `json:"id"`
	Username     string    `json:"username"`
	Email        string    `json:"email"`
	PasswordHash string    `json:"-"`
	Role         string    `json:"role"`
	CreatedAt    time.Time `json:"created_at"`
	// Trial time limit; nil = none. Only filled by /admin/me.
	AccessExpiresAt *time.Time `json:"access_expires_at,omitempty"`
}

type LoginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

type LoginResponse struct {
	Token string `json:"token"`
	Admin Admin  `json:"admin"`
}
