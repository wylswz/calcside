package dto

import (
	"time"

	policysvc "calcside/cmd/calcside/internal/service/policy"
)

// Policy describes either a built-in or a user Rego policy.
type Policy struct {
	Kind        string     `json:"kind"`
	Default     bool       `json:"default"`
	Description string     `json:"description,omitempty"`
	ID          string     `json:"id"`
	UserID      string     `json:"user_id,omitempty"`
	Name        string     `json:"name"`
	Rego        string     `json:"rego,omitempty"`
	CreatedAt   *time.Time `json:"created_at,omitempty"`
	UpdatedAt   *time.Time `json:"updated_at,omitempty"`
}

func NewPolicy(p *policysvc.Policy) Policy {
	out := Policy{
		ID: p.ID, UserID: p.UserID, Name: p.Name, Rego: p.Rego,
		Kind: p.Kind, Default: p.Default, Description: p.Description,
	}
	if p.Kind == "rego" {
		out.CreatedAt, out.UpdatedAt = &p.CreatedAt, &p.UpdatedAt
	}
	return out
}

func NewPolicies(lst []*policysvc.Policy) []Policy {
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
