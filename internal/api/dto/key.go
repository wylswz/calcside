package dto

import (
	"time"

	"calcside/internal/store"
)

// APIKey has no Hash field — the key hash is never serializable.
type APIKey struct {
	ID         string     `json:"id"`
	UserID     string     `json:"user_id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

func NewAPIKey(k *store.APIKey) APIKey {
	return APIKey{
		ID: k.ID, UserID: k.UserID, Name: k.Name, Prefix: k.Prefix,
		CreatedAt: k.CreatedAt, LastUsedAt: k.LastUsedAt,
		ExpiresAt: k.ExpiresAt, RevokedAt: k.RevokedAt,
	}
}

func NewAPIKeys(lst []*store.APIKey) []APIKey {
	out := make([]APIKey, len(lst))
	for i, k := range lst {
		out[i] = NewAPIKey(k)
	}
	return out
}

type KeysEnvelope struct {
	Keys []APIKey `json:"keys"`
}

// CreatedKey is the only body that ever carries the plaintext secret.
type CreatedKey struct {
	Key    APIKey `json:"key"`
	Secret string `json:"secret"`
}
