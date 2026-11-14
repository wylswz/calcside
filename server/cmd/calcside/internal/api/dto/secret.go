package dto

import (
	"time"

	"calcside/internal/store"
)

// Secret has no Ciphertext field — sealed bytes are never serializable.
type Secret struct {
	ID             string    `json:"id"`
	UserID         string    `json:"user_id"`
	Name           string    `json:"name"`
	AllowedDomains []string  `json:"allowed_domains"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func NewSecret(s *store.Secret) Secret {
	return Secret{
		ID: s.ID, UserID: s.UserID, Name: s.Name,
		AllowedDomains: s.AllowedDomains, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt,
	}
}

func NewSecrets(lst []*store.Secret) []Secret {
	out := make([]Secret, len(lst))
	for i, s := range lst {
		out[i] = NewSecret(s)
	}
	return out
}

type SecretEnvelope struct {
	Secret Secret `json:"secret"`
}

type SecretsEnvelope struct {
	Secrets []Secret `json:"secrets"`
}
