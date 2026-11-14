// Package iam implements API-key management. All operations require a
// session actor (cookie or dev-anonymous); API keys cannot manage keys.
package iam

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"golang.org/x/crypto/bcrypt"

	"calcside/cmd/calcside/internal/auth"
	"calcside/cmd/calcside/internal/service"
	"calcside/cmd/calcside/internal/service/access"
	"calcside/internal/store"
	"calcside/internal/types"
)

// SpecValidator validates a would-be instance spec the same way
// instance creation does (normalization, policy selection, secret
// resolution); implemented by the sandbox service.
type SpecValidator interface {
	ValidateSpec(ctx context.Context, a service.Actor, rawSpec []byte, keySpec bool) (json.RawMessage, error)
}

type Service struct {
	st        store.Store
	access    *access.Checker
	specs     SpecValidator
	maxPerKey int // upper bound for a restricted key's max_instances
	now       func() time.Time
}

// New builds the service; a nil now uses time.Now.
func New(st store.Store, ac *access.Checker, specs SpecValidator, maxPerUser int, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{st: st, access: ac, specs: specs, maxPerKey: maxPerUser, now: now}
}

// session allows only actors with Configure — keys can never manage
// keys (the key ceiling has no Configure action).
func (s *Service) session(ctx context.Context, a service.Actor) error {
	return s.access.Workspace(ctx, a, a.WorkspaceID, access.Configure)
}

func (s *Service) ListKeys(ctx context.Context, a service.Actor) ([]*store.APIKey, error) {
	if err := s.session(ctx, a); err != nil {
		return nil, err
	}
	keys, err := s.st.ListAPIKeys(ctx, a.WorkspaceID)
	if err != nil {
		return nil, service.Internal(err)
	}
	if keys == nil {
		keys = []*store.APIKey{}
	}
	return keys, nil
}

// CreateKey returns the stored key and its plaintext secret. A non-nil
// spec makes a restricted key and requires maxInstances ≥ 1 (bounded by
// the per-user limit when configured); a nil spec is full access and
// maxInstances must be absent. instanceID or applicationID binds the
// key to that one resource.
func (s *Service) CreateKey(ctx context.Context, a service.Actor, name string, expiresInSeconds int64, spec json.RawMessage, maxInstances int, viewOnly bool, instanceID, applicationID string) (*store.APIKey, string, error) {
	if err := s.session(ctx, a); err != nil {
		return nil, "", err
	}
	var normalized json.RawMessage
	if len(spec) > 0 {
		var err error
		normalized, err = s.specs.ValidateSpec(ctx, a, spec, true)
		if err != nil {
			return nil, "", err
		}
		bound := s.maxPerKey
		if bound <= 0 {
			bound = 1 << 30 // no configured cap; any positive number is valid
		}
		if maxInstances < 1 || maxInstances > bound {
			return nil, "", service.Errf(types.ErrCodeBadSpec, "max_instances is required for a restricted key (1..%d)", bound)
		}
	} else if maxInstances != 0 {
		return nil, "", service.Errf(types.ErrCodeBadSpec, "max_instances requires a spec")
	}
	if viewOnly && len(spec) > 0 {
		return nil, "", service.Errf(types.ErrCodeBadSpec, "view_only cannot combine with a restricted spec")
	}
	var boundIn *store.Instance
	var boundApp *store.Application
	switch {
	case instanceID != "" && applicationID != "":
		return nil, "", service.Errf(types.ErrCodeBadSpec, "instance_id and application_id are mutually exclusive")
	case instanceID != "":
		if len(spec) > 0 || viewOnly {
			return nil, "", service.Errf(types.ErrCodeBadSpec, "instance_id cannot combine with spec or view_only")
		}
		var err error
		boundIn, err = s.access.Instance(ctx, a, instanceID, access.Run)
		if err != nil {
			return nil, "", err
		}
		if err := s.access.Workspace(ctx, a, boundIn.WorkspaceID, access.Configure); err != nil {
			return nil, "", err
		}
	case applicationID != "":
		if len(spec) > 0 || viewOnly {
			return nil, "", service.Errf(types.ErrCodeBadSpec, "application_id cannot combine with spec or view_only")
		}
		var err error
		boundApp, err = s.access.Application(ctx, a, applicationID, access.Run)
		if err != nil {
			return nil, "", err
		}
		if err := s.access.Workspace(ctx, a, boundApp.WorkspaceID, access.Configure); err != nil {
			return nil, "", err
		}
	}
	var exp *time.Time
	if expiresInSeconds > 0 {
		t := s.now().Add(time.Duration(expiresInSeconds) * time.Second)
		exp = &t
	}
	secret, key := auth.NewAPIKey(a.UserID, name, exp)
	key.Spec = normalized
	key.MaxInstances = maxInstances
	key.WorkspaceID = a.WorkspaceID
	key.ViewOnly = viewOnly
	if boundIn != nil {
		key.BoundID = boundIn.ID
		key.WorkspaceID = boundIn.WorkspaceID
	}
	if boundApp != nil {
		key.BoundID = boundApp.ID
		key.WorkspaceID = boundApp.WorkspaceID
	}
	if err := s.st.CreateAPIKey(ctx, key); err != nil {
		return nil, "", service.Internal(err)
	}
	return key, secret, nil
}

func (s *Service) RevokeKey(ctx context.Context, a service.Actor, id string) error {
	k, err := s.access.Key(ctx, a, id, access.Configure)
	if err != nil {
		return err
	}
	if err := s.st.RevokeAPIKey(ctx, k.UserID, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return service.NotFound("key not found")
		}
		return service.Internal(err)
	}
	return nil
}

func (s *Service) ChangePassword(ctx context.Context, a service.Actor, currentPassword, newPassword string) error {
	if a.Kind != types.AuthSession {
		return service.Forbidden("session required to change password")
	}
	if err := auth.ValidatePassword(newPassword); err != nil {
		return service.BadRequest("%s", err)
	}
	u, err := s.st.GetUser(ctx, a.UserID)
	if err != nil {
		return service.Internal(err)
	}
	if u.PasswordHash == "" {
		return service.Forbidden("this account does not have a local password")
	}
	if len(currentPassword) > 72 || bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(currentPassword)) != nil {
		return service.Forbidden("current password is incorrect")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return service.Internal(err)
	}
	if err := s.st.UpdateUserPassword(ctx, a.UserID, u.PasswordHash, string(hash)); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return service.Conflict("password changed concurrently; sign in again")
		}
		return &service.Error{Code: types.ErrCodeInternal, Msg: "password update failed", Err: err}
	}
	return nil
}
