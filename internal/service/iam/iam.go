// Package iam implements API-key management. All operations require a
// session actor (cookie or dev-anonymous); API keys cannot manage keys.
package iam

import (
	"context"
	"errors"
	"time"

	"calcside/internal/auth"
	"calcside/internal/service"
	"calcside/internal/store"
)

type Service struct {
	st  store.Store
	now func() time.Time
}

// New builds the service; a nil now uses time.Now.
func New(st store.Store, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{st: st, now: now}
}

func session(a service.Actor) error {
	if a.ViaKey() {
		return service.Forbidden("session required to manage API keys")
	}
	return nil
}

func (s *Service) ListKeys(ctx context.Context, a service.Actor) ([]*store.APIKey, error) {
	if err := session(a); err != nil {
		return nil, err
	}
	keys, err := s.st.ListAPIKeys(ctx, a.UserID)
	if err != nil {
		return nil, service.Internal(err)
	}
	if keys == nil {
		keys = []*store.APIKey{}
	}
	return keys, nil
}

// CreateKey returns the stored key and its plaintext secret.
func (s *Service) CreateKey(ctx context.Context, a service.Actor, name string, expiresInSeconds int64) (*store.APIKey, string, error) {
	if err := session(a); err != nil {
		return nil, "", err
	}
	var exp *time.Time
	if expiresInSeconds > 0 {
		t := s.now().Add(time.Duration(expiresInSeconds) * time.Second)
		exp = &t
	}
	secret, key := auth.NewAPIKey(a.UserID, name, exp)
	if err := s.st.CreateAPIKey(ctx, key); err != nil {
		return nil, "", service.Internal(err)
	}
	return key, secret, nil
}

func (s *Service) RevokeKey(ctx context.Context, a service.Actor, id string) error {
	if err := session(a); err != nil {
		return err
	}
	if err := s.st.RevokeAPIKey(ctx, a.UserID, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return service.NotFound("key not found")
		}
		return service.Internal(err)
	}
	return nil
}
