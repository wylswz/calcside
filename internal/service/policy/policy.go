// Package policy implements per-user rego policy CRUD plus a
// validate-only operation used by the policies/validate endpoint.
package policy

import (
	"context"

	rego "calcside/internal/policy"
	"calcside/internal/service"
	"calcside/internal/store"
	"calcside/internal/types"
)

type Service struct {
	st store.Store
}

func New(st store.Store) *Service {
	return &Service{st: st}
}

func (s *Service) List(ctx context.Context, a service.Actor) ([]*store.Policy, error) {
	lst, err := s.st.ListPolicies(ctx, a.UserID)
	if err != nil {
		return nil, service.Internal(err)
	}
	if lst == nil {
		lst = []*store.Policy{}
	}
	return lst, nil
}

func (s *Service) Create(ctx context.Context, a service.Actor, name, src string, enabled *bool) (*store.Policy, error) {
	if name == "" || src == "" {
		return nil, service.BadRequest("name and rego required")
	}
	if err := rego.Validate(src); err != nil {
		return nil, service.Errf(types.ErrCodeBadPolicy, "%s", err.Error())
	}
	pol := &store.Policy{UserID: a.UserID, Name: name, Rego: src, Enabled: true}
	if enabled != nil {
		pol.Enabled = *enabled
	}
	if err := s.st.CreatePolicy(ctx, pol); err != nil {
		return nil, service.Internal(err)
	}
	return pol, nil
}

// ValidateRego checks a policy source; the error carries bad_policy so
// callers can render it (the validate endpoint reports 200 + valid:false).
func (s *Service) ValidateRego(src string) error {
	if err := rego.Validate(src); err != nil {
		return service.Errf(types.ErrCodeBadPolicy, "%s", err.Error())
	}
	return nil
}

func (s *Service) Get(ctx context.Context, a service.Actor, id string) (*store.Policy, error) {
	pol, err := s.st.GetPolicy(ctx, id)
	if err != nil || pol.UserID != a.UserID {
		return nil, service.NotFound("policy not found")
	}
	return pol, nil
}

func (s *Service) Update(ctx context.Context, a service.Actor, id string, name, src *string, enabled *bool) (*store.Policy, error) {
	pol, err := s.Get(ctx, a, id)
	if err != nil {
		return nil, err
	}
	if name != nil {
		pol.Name = *name
	}
	if src != nil {
		if err := rego.Validate(*src); err != nil {
			return nil, service.Errf(types.ErrCodeBadPolicy, "%s", err.Error())
		}
		pol.Rego = *src
	}
	if enabled != nil {
		pol.Enabled = *enabled
	}
	if err := s.st.UpdatePolicy(ctx, pol); err != nil {
		return nil, service.Internal(err)
	}
	return pol, nil
}

func (s *Service) Delete(ctx context.Context, a service.Actor, id string) error {
	pol, err := s.Get(ctx, a, id)
	if err != nil {
		return err
	}
	if err := s.st.DeletePolicy(ctx, pol.ID); err != nil {
		return service.Internal(err)
	}
	return nil
}
