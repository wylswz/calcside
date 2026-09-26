// Package vault implements per-user secrets: name/domain validation,
// sealing via the secrets cipher, and ownership checks. All operations
// require a session actor; a nil cipher disables the vault (503).
package vault

import (
	"context"
	"errors"
	"fmt"

	"calcside/internal/secrets"
	"calcside/internal/service"
	"calcside/internal/store"
	"calcside/internal/types"
)

type Service struct {
	st     store.Store
	cipher *secrets.Cipher
}

func New(st store.Store, cipher *secrets.Cipher) *Service {
	return &Service{st: st, cipher: cipher}
}

// Enabled reports whether the vault has a cipher (reported by
// /api/v1/auth/config as "secrets").
func (s *Service) Enabled() bool { return s.cipher != nil }

func (s *Service) ready(a service.Actor) error {
	if a.ViaKey() {
		// Same message as the API-keys gate: the wire contract reuses
		// it for secrets even though the wording mentions keys.
		return service.Forbidden("session required to manage API keys")
	}
	if s.cipher == nil {
		return service.Errf(types.ErrCodeSecretsDisabled, "secrets vault disabled (no --secret-key)")
	}
	return nil
}

func (s *Service) List(ctx context.Context, a service.Actor) ([]*store.Secret, error) {
	if err := s.ready(a); err != nil {
		return nil, err
	}
	lst, err := s.st.ListSecrets(ctx, a.UserID)
	if err != nil {
		return nil, service.Internal(err)
	}
	if lst == nil {
		lst = []*store.Secret{}
	}
	return lst, nil
}

func validateSecretInput(name, value string, domains []string) error {
	if !secrets.ValidName(name) {
		return fmt.Errorf("invalid secret name %q (want [A-Z_][A-Z0-9_])", name)
	}
	if len(value) == 0 {
		return fmt.Errorf("value required")
	}
	if len(value) > secrets.MaxValueBytes {
		return fmt.Errorf("value exceeds %d bytes", secrets.MaxValueBytes)
	}
	if _, err := secrets.ValidateDomains(domains); err != nil {
		return err
	}
	return nil
}

func (s *Service) Create(ctx context.Context, a service.Actor, name, value string, domains []string) (*store.Secret, error) {
	if err := s.ready(a); err != nil {
		return nil, err
	}
	if err := validateSecretInput(name, value, domains); err != nil {
		return nil, service.Errf(types.ErrCodeBadSecret, "%s", err.Error())
	}
	ct, err := s.cipher.Seal([]byte(value), a.UserID+"/"+name)
	if err != nil {
		return nil, service.Internal(err)
	}
	if domains == nil {
		domains = []string{} // unrestricted; responses always carry an array
	}
	sec := &store.Secret{UserID: a.UserID, Name: name, Ciphertext: ct, AllowedDomains: domains}
	if err := s.st.CreateSecret(ctx, sec); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil, service.Conflict("secret with that name already exists")
		}
		return nil, service.Internal(err)
	}
	return sec, nil
}

func (s *Service) Update(ctx context.Context, a service.Actor, id string, value *string, domains *[]string) (*store.Secret, error) {
	if err := s.ready(a); err != nil {
		return nil, err
	}
	sec, err := s.st.GetSecret(ctx, id)
	if err != nil || sec.UserID != a.UserID {
		return nil, service.NotFound("secret not found")
	}
	if value != nil {
		if len(*value) == 0 || len(*value) > secrets.MaxValueBytes {
			return nil, service.Errf(types.ErrCodeBadSecret, "value must be 1..16KiB")
		}
		ct, err := s.cipher.Seal([]byte(*value), a.UserID+"/"+sec.Name)
		if err != nil {
			return nil, service.Internal(err)
		}
		sec.Ciphertext = ct
	}
	if domains != nil {
		if _, err := secrets.ValidateDomains(*domains); err != nil {
			return nil, service.Errf(types.ErrCodeBadSecret, "%s", err.Error())
		}
		sec.AllowedDomains = *domains
	}
	if err := s.st.UpdateSecret(ctx, sec); err != nil {
		return nil, service.Internal(err)
	}
	return sec, nil
}

func (s *Service) Delete(ctx context.Context, a service.Actor, id string) error {
	if err := s.ready(a); err != nil {
		return err
	}
	sec, err := s.st.GetSecret(ctx, id)
	if err != nil || sec.UserID != a.UserID {
		return service.NotFound("secret not found")
	}
	if err := s.st.DeleteSecret(ctx, sec.ID); err != nil {
		return service.Internal(err)
	}
	return nil
}
