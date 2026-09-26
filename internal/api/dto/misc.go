// Package dto defines the response bodies for the REST API. Domain
// types (store.*) never reach the wire directly, so sensitive fields
// (key hashes, ciphertext, full code) cannot leak by a missing json
// tag.
package dto

import (
	"time"

	"calcside/internal/store"
	"calcside/internal/types"
)

// OK is the shared {"ok": true} body; healthz reuses it as {"ok": true}.
type OK struct {
	OK bool `json:"ok"`
}

// APIError / ErrorEnvelope are the {"error":{code,message}} body.
type APIError struct {
	Code    types.APIErrorCode `json:"code"`
	Message string             `json:"message"`
}

type ErrorEnvelope struct {
	Error APIError `json:"error"`
}

// AuthConfig is the auth/config body.
type AuthConfig struct {
	Google  bool `json:"google"`
	DevMode bool `json:"dev_mode"`
	Secrets bool `json:"secrets"`
}

// User mirrors the wire shape of a store.User.
type User struct {
	ID          string    `json:"id"`
	Email       string    `json:"email"`
	Name        string    `json:"name"`
	GoogleSub   string    `json:"google_sub,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	LastLoginAt time.Time `json:"last_login_at"`
}

func NewUser(u *store.User) User {
	return User{
		ID: u.ID, Email: u.Email, Name: u.Name, GoogleSub: u.GoogleSub,
		CreatedAt: u.CreatedAt, LastLoginAt: u.LastLoginAt,
	}
}

// Me is the /me body.
type Me struct {
	User   User           `json:"user"`
	ViaKey bool           `json:"via_key"`
	Kind   types.AuthKind `json:"kind"`
}
