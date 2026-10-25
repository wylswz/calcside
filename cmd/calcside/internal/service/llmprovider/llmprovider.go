// Package llmprovider implements the workspace-level LLM provider
// registry: named, pre-validated extension sources plus config that ext
// aliases select by name at instance creation.
package llmprovider

import (
	"context"
	"encoding/json"
	"errors"

	"calcside/cmd/calcside/internal/service"
	"calcside/cmd/calcside/internal/service/access"
	capext "calcside/internal/capability/ext"
	"calcside/internal/runtime"
	"calcside/internal/store"
	"calcside/internal/types"
)

type Service struct {
	st     store.Store
	access *access.Checker
	ext    *capext.Options
}

func New(st store.Store, ac *access.Checker, opts *capext.Options) *Service {
	return &Service{st: st, access: ac, ext: opts}
}

func (s *Service) List(ctx context.Context, a service.Actor) ([]*store.LLMProvider, error) {
	lst, err := s.st.ListLLMProviders(ctx, a.WorkspaceID)
	if err != nil {
		return nil, service.Internal(err)
	}
	if lst == nil {
		lst = []*store.LLMProvider{}
	}
	return lst, nil
}

func writeErr(err error) error {
	if errors.Is(err, store.ErrConflict) {
		return service.Conflict("llm provider with that name already exists")
	}
	return service.Internal(err)
}

// validateSpec checks the name and that source+config form a usable
// interface: llm extension.
func (s *Service) validateSpec(name, source, sum string, config json.RawMessage) error {
	if !runtime.ValidPolicyName(name) {
		return service.BadRequest("provider name must match [A-Za-z0-9][A-Za-z0-9_.-]{0,63}")
	}
	cfg := map[string]any{}
	if len(config) > 0 && string(config) != "null" {
		if err := json.Unmarshal(config, &cfg); err != nil {
			return service.BadRequest("config must be a JSON object")
		}
	}
	if err := capext.ValidateProvider(s.ext, source, sum, cfg); err != nil {
		return service.Errf(types.ErrCodeBadSpec, "%s", err.Error())
	}
	return nil
}

func (s *Service) Create(ctx context.Context, a service.Actor, name, source, sum string, config json.RawMessage) (*store.LLMProvider, error) {
	if err := s.access.Workspace(ctx, a, a.WorkspaceID, access.Configure); err != nil {
		return nil, err
	}
	if name == "" || source == "" {
		return nil, service.BadRequest("name and source required")
	}
	if err := s.validateSpec(name, source, sum, config); err != nil {
		return nil, err
	}
	if len(config) == 0 {
		config = json.RawMessage(`{}`)
	}
	p := &store.LLMProvider{UserID: a.UserID, WorkspaceID: a.WorkspaceID,
		Name: name, Source: source, Sum: sum, Config: config}
	if err := s.st.CreateLLMProvider(ctx, p); err != nil {
		return nil, writeErr(err)
	}
	return p, nil
}

func (s *Service) Get(ctx context.Context, a service.Actor, id string) (*store.LLMProvider, error) {
	return s.access.LLMProvider(ctx, a, id, access.View)
}

func (s *Service) Update(ctx context.Context, a service.Actor, id string, name, source, sum *string, config json.RawMessage) (*store.LLMProvider, error) {
	p, err := s.access.LLMProvider(ctx, a, id, access.Configure)
	if err != nil {
		return nil, err
	}
	if name != nil && *name != p.Name {
		if !runtime.ValidPolicyName(*name) {
			return nil, service.BadRequest("provider name must match [A-Za-z0-9][A-Za-z0-9_.-]{0,63}")
		}
		p.Name = *name
	}
	if source != nil {
		p.Source = *source
	}
	if sum != nil {
		p.Sum = *sum
	}
	if len(config) > 0 {
		p.Config = config
	}
	if err := s.validateSpec(p.Name, p.Source, p.Sum, p.Config); err != nil {
		return nil, err
	}
	if err := s.st.UpdateLLMProvider(ctx, p); err != nil {
		return nil, writeErr(err)
	}
	return p, nil
}

func (s *Service) Delete(ctx context.Context, a service.Actor, id string) error {
	p, err := s.access.LLMProvider(ctx, a, id, access.Configure)
	if err != nil {
		return err
	}
	if err := s.st.DeleteLLMProvider(ctx, p.ID); err != nil {
		return service.Internal(err)
	}
	return nil
}
