package dto

import (
	"time"

	"calcside/internal/store"
)

// Policy mirrors the wire shape of a store.Policy.
type Policy struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	Name      string    `json:"name"`
	Rego      string    `json:"rego"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func NewPolicy(p *store.Policy) Policy {
	return Policy{
		ID: p.ID, UserID: p.UserID, Name: p.Name, Rego: p.Rego,
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
}

func NewPolicies(lst []*store.Policy) []Policy {
	out := make([]Policy, len(lst))
	for i, p := range lst {
		out[i] = NewPolicy(p)
	}
	return out
}

type PolicyEnvelope struct {
	Policy Policy `json:"policy"`
}

type PoliciesEnvelope struct {
	Policies []Policy `json:"policies"`
}

// ValidationResult is the validate endpoint body; Error is omitted when
// the source is valid (the key is absent, not null).
type ValidationResult struct {
	Valid bool   `json:"valid"`
	Error string `json:"error,omitempty"`
}
