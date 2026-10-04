// Package policy implements the per-user rego policy library (CRUD plus
// a validate-only operation used by the policies/validate endpoint).
// Library policies apply only to instances whose spec.policies names
// them, snapshotted at instance creation.
package policy

import (
	"context"
	"errors"

	rego "calcside/internal/policy"
	"calcside/internal/runtime"
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

func validateName(name string) error {
	if !runtime.ValidPolicyName(name) {
		return service.BadRequest("policy name must match [A-Za-z0-9][A-Za-z0-9_.-]{0,63}")
	}
	return nil
}

func writeErr(err error) error {
	if errors.Is(err, store.ErrConflict) {
		return service.Conflict("policy with that name already exists")
	}
	return service.Internal(err)
}

func (s *Service) Create(ctx context.Context, a service.Actor, name, src string) (*store.Policy, error) {
	if name == "" || src == "" {
		return nil, service.BadRequest("name and rego required")
	}
	if err := validateName(name); err != nil {
		return nil, err
	}
	if err := rego.Validate(src); err != nil {
		return nil, service.Errf(types.ErrCodeBadPolicy, "%s", err.Error())
	}
	pol := &store.Policy{UserID: a.UserID, Name: name, Rego: src}
	if err := s.st.CreatePolicy(ctx, pol); err != nil {
		return nil, writeErr(err)
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

func (s *Service) Update(ctx context.Context, a service.Actor, id string, name, src *string) (*store.Policy, error) {
	pol, err := s.Get(ctx, a, id)
	if err != nil {
		return nil, err
	}
	if name != nil && *name != pol.Name {
		if err := validateName(*name); err != nil {
			return nil, err
		}
		pol.Name = *name
	}
	if src != nil {
		if err := rego.Validate(*src); err != nil {
			return nil, service.Errf(types.ErrCodeBadPolicy, "%s", err.Error())
		}
		pol.Rego = *src
	}
	if err := s.st.UpdatePolicy(ctx, pol); err != nil {
		return nil, writeErr(err)
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
