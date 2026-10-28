package dto

import (
	"encoding/json"
	"strings"
	"time"

	"calcside/internal/store"
)

// APIKey has no Hash field — the key hash is never serializable.
type APIKey struct {
	ID            string          `json:"id"`
	UserID        string          `json:"user_id"`
	Name          string          `json:"name"`
	Prefix        string          `json:"prefix"`
	CreatedAt     time.Time       `json:"created_at"`
	LastUsedAt    *time.Time      `json:"last_used_at,omitempty"`
	ExpiresAt     *time.Time      `json:"expires_at,omitempty"`
	RevokedAt     *time.Time      `json:"revoked_at,omitempty"`
	Restricted    bool            `json:"restricted"`
	Spec          json.RawMessage `json:"spec,omitempty"`
	MaxInstances  int             `json:"max_instances,omitempty"`
	ViewOnly      bool            `json:"view_only"`
	InstanceID    string          `json:"instance_id,omitempty"`
	ApplicationID string          `json:"application_id,omitempty"`
}

func NewAPIKey(k *store.APIKey) APIKey {
	out := APIKey{
		ID: k.ID, UserID: k.UserID, Name: k.Name, Prefix: k.Prefix,
		CreatedAt: k.CreatedAt, LastUsedAt: k.LastUsedAt,
		ExpiresAt: k.ExpiresAt, RevokedAt: k.RevokedAt,
		Restricted: len(k.Spec) > 0, Spec: k.Spec, MaxInstances: k.MaxInstances, ViewOnly: k.ViewOnly,
	}
	// The bound id's prefix names the resource kind — expose it under the
	// same field the request used.
	if strings.HasPrefix(k.BoundID, string(store.PrefixApplication)) {
		out.ApplicationID = k.BoundID
	} else {
		out.InstanceID = k.BoundID
	}
	return out
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
