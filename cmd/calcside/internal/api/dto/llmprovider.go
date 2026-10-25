package dto

import (
	"encoding/json"
	"time"

	"calcside/internal/store"
)

// LLMProvider is the API view of a workspace LLM provider record.
type LLMProvider struct {
	ID        string          `json:"id"`
	UserID    string          `json:"user_id,omitempty"`
	Name      string          `json:"name"`
	Source    string          `json:"source"`
	Sum       string          `json:"sum,omitempty"`
	Models    []string        `json:"models"`
	Config    json.RawMessage `json:"config"`
	CreatedAt *time.Time      `json:"created_at,omitempty"`
	UpdatedAt *time.Time      `json:"updated_at,omitempty"`
}

func NewLLMProvider(p *store.LLMProvider) LLMProvider {
	cfg := p.Config
	if len(cfg) == 0 {
		cfg = json.RawMessage(`{}`)
	}
	return LLMProvider{
		ID: p.ID, UserID: p.UserID, Name: p.Name, Source: p.Source,
		Sum: p.Sum, Models: p.Models, Config: cfg,
		CreatedAt: &p.CreatedAt, UpdatedAt: &p.UpdatedAt,
	}
}

func NewLLMProviders(lst []*store.LLMProvider) []LLMProvider {
	out := make([]LLMProvider, len(lst))
	for i, p := range lst {
		out[i] = NewLLMProvider(p)
	}
	return out
}

type LLMProviderEnvelope struct {
	Provider LLMProvider `json:"provider"`
}

type LLMProvidersEnvelope struct {
	Providers []LLMProvider `json:"providers"`
}
