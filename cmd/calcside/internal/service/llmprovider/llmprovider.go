// Package llmprovider implements the workspace-level LLM provider
// registry: named provider extensions plus validated config and a model
// catalogue. The llm capability ext selects one by name at instance
// creation.
package llmprovider

import (
	"context"
	"encoding/json"
	"errors"

	"calcside/cmd/calcside/internal/service"
	"calcside/cmd/calcside/internal/service/access"
	capext "calcside/internal/capability/ext"
	"calcside/internal/llm"
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

// validateSpec loads the provider extension, validates its config and
// resolves the model catalogue (request override else manifest default).
func (s *Service) validateSpec(ctx context.Context, wsID, name, source, sum string, models []string, config json.RawMessage) (json.RawMessage, []string, error) {
	if !runtime.ValidPolicyName(name) {
		return nil, nil, service.BadRequest("provider name must match [A-Za-z0-9][A-Za-z0-9_.-]{0,63}")
	}
	if source == "" {
		return nil, nil, service.BadRequest("provider source is required")
	}
	cfg := map[string]any{}
	if len(config) > 0 && string(config) != "null" {
		if err := json.Unmarshal(config, &cfg); err != nil {
			return nil, nil, service.BadRequest("config must be a JSON object")
		}
	}
	info, err := capext.ValidateProvider(s.ext, source, sum, cfg)
	if err != nil {
		return nil, nil, service.Errf(types.ErrCodeBadSpec, "%s", err.Error())
	}
	if len(models) == 0 {
		models = info.Models
	}
	models, err = llm.NormalizeModels(models)
	if err != nil {
		return nil, nil, service.Errf(types.ErrCodeBadSpec, "%s", err.Error())
	}
	if len(models) == 0 {
		return nil, nil, service.BadRequest("provider requires at least one model")
	}
	for _, secretName := range info.SecretNames {
		if _, err := s.st.GetSecretByName(ctx, wsID, secretName); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil, nil, service.BadRequest("provider references unknown workspace secret %q", secretName)
			}
			return nil, nil, service.Internal(err)
		}
	}
	b, err := json.Marshal(info.Config)
	if err != nil {
		return nil, nil, service.Internal(err)
	}
	return b, models, nil
}

func (s *Service) Create(ctx context.Context, a service.Actor, name, source, sum string, models []string, config json.RawMessage) (*store.LLMProvider, error) {
	if err := s.access.Workspace(ctx, a, a.WorkspaceID, access.Configure); err != nil {
		return nil, err
	}
	if name == "" || source == "" {
		return nil, service.BadRequest("name and source required")
	}
	eff, models, err := s.validateSpec(ctx, a.WorkspaceID, name, source, sum, models, config)
	if err != nil {
		return nil, err
	}
	p := &store.LLMProvider{UserID: a.UserID, WorkspaceID: a.WorkspaceID,
		Name: name, Source: source, Sum: sum, Models: models, Config: eff}
	if err := s.st.CreateLLMProvider(ctx, p); err != nil {
		return nil, writeErr(err)
	}
	return p, nil
}

func (s *Service) Get(ctx context.Context, a service.Actor, id string) (*store.LLMProvider, error) {
	return s.access.LLMProvider(ctx, a, id, access.View)
}

func (s *Service) Update(ctx context.Context, a service.Actor, id string, name, source, sum *string, models *[]string, config json.RawMessage) (*store.LLMProvider, error) {
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
	if models != nil {
		p.Models = *models
	}
	if len(config) > 0 {
		p.Config = config
	}
	eff, normModels, err := s.validateSpec(ctx, p.WorkspaceID, p.Name, p.Source, p.Sum, p.Models, p.Config)
	if err != nil {
		return nil, err
	}
	p.Config = eff
	p.Models = normModels
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
