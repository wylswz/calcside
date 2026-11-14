package vault

import (
	"context"
	"fmt"

	"calcside/internal/hostmatch"
	"calcside/internal/runtime"
	"calcside/internal/secrets"
	"calcside/internal/store"
	"calcside/internal/types"
)

// checkEntry validates one spec secret entry's shape: exactly one of
// ref/value, a consistent source marker, size and domain syntax. It is
// shared by Describe and Resolve so both modes agree on syntax.
func checkEntry(name string, ss runtime.SecretSpec) error {
	if ss.Ref != "" && ss.Value != "" {
		return fmt.Errorf("secret %s: exactly one of ref or value", name)
	}
	if ss.Ref == "" && ss.Value == "" {
		return fmt.Errorf("secret %s: exactly one of ref or value", name)
	}
	if ss.Source != "" && ss.Source != types.SecretVault && ss.Source != types.SecretInline {
		return fmt.Errorf("secret %s: invalid source %q", name, ss.Source)
	}
	if ss.Source == types.SecretVault && ss.Ref == "" {
		return fmt.Errorf("secret %s: source %q requires ref", name, ss.Source)
	}
	if ss.Source == types.SecretInline && ss.Value == "" {
		return fmt.Errorf("secret %s: source %q requires value", name, ss.Source)
	}
	if ss.Value != "" && len(ss.Value) > secrets.MaxValueBytes {
		return fmt.Errorf("secret %s: value exceeds %d bytes", name, secrets.MaxValueBytes)
	}
	if _, err := secrets.ValidateDomains(ss.AllowedDomains); err != nil {
		return fmt.Errorf("secret %s: %w", name, err)
	}
	return nil
}

// describeRef looks up the vault entry and computes the effective
// allowlist — vault's own, or the spec's narrowing (widening rejected)
// — without ever decrypting.
func (s *Service) describeRef(ctx context.Context, workspaceID, name string, ss runtime.SecretSpec) (*store.Secret, []string, error) {
	rec, err := s.st.GetSecretByName(ctx, workspaceID, ss.Ref)
	if err != nil {
		return nil, nil, fmt.Errorf("secret %s: unknown vault ref %q", name, ss.Ref)
	}
	vaultRules, err := hostmatch.ParseAll(rec.AllowedDomains)
	if err != nil {
		return nil, nil, fmt.Errorf("secret %s: bad vault domains", name)
	}
	if len(ss.AllowedDomains) == 0 {
		return rec, rec.AllowedDomains, nil
	}
	narrowed, err := hostmatch.ParseAll(ss.AllowedDomains)
	if err != nil {
		return nil, nil, fmt.Errorf("secret %s: %w", name, err)
	}
	// An unrestricted vault secret (no domains) may always be narrowed.
	for _, nr := range narrowed {
		if len(vaultRules) == 0 {
			break
		}
		covered := false
		for _, vr := range vaultRules {
			if hostmatch.Covers(vr, nr) {
				covered = true
				break
			}
		}
		if !covered {
			entry := nr.Host
			if nr.Port != "" {
				entry += ":" + nr.Port
			}
			return nil, nil, fmt.Errorf("secret %s: domain %q not covered by vault allowlist", name, entry)
		}
	}
	return rec, ss.AllowedDomains, nil
}

// Describe runs every check Resolve performs — entry shape, vault
// existence, allowlist narrowing — but never decrypts: the result is
// the sanitized descriptor map only. Draft saves, previews and debug
// specs use this so ciphertext is never opened outside real creation.
func (s *Service) Describe(ctx context.Context, workspaceID string, in map[string]runtime.SecretSpec) (map[string]runtime.SecretSpec, error) {
	sanitized := map[string]runtime.SecretSpec{}
	for name, ss := range in {
		if err := checkEntry(name, ss); err != nil {
			return nil, err
		}
		switch {
		case ss.Ref != "":
			if s.cipher == nil {
				return nil, fmt.Errorf("secret %s: vault secrets disabled (no --secret-key)", name)
			}
			_, domains, err := s.describeRef(ctx, workspaceID, name, ss)
			if err != nil {
				return nil, err
			}
			sanitized[name] = runtime.SecretSpec{Ref: ss.Ref, AllowedDomains: domains, Source: types.SecretVault}
		default:
			sanitized[name] = runtime.SecretSpec{AllowedDomains: ss.AllowedDomains, Source: types.SecretInline}
		}
	}
	return sanitized, nil
}

// Resolve turns a spec's secrets map into the plaintext set shipped to
// the execution tier, plus the sanitized descriptors persisted with the
// instance.
//
// This runs on the API tier on purpose. Vault lookup, decryption, and
// the check that a spec may only *narrow* a vault secret's allowlist all
// need the database and the cipher key, and an execution node has
// neither. What crosses the tier boundary is the already-authorized
// result.
//
// Unlike the CRUD methods, Resolve takes no Actor and does not require a
// session: creating an instance that references vault secrets is allowed
// from an API key, it just cannot read them back.
func (s *Service) Resolve(ctx context.Context, workspaceID string, in map[string]runtime.SecretSpec) ([]runtime.Secret, map[string]runtime.SecretSpec, error) {
	var out []runtime.Secret
	sanitized := map[string]runtime.SecretSpec{}
	for name, ss := range in {
		if err := checkEntry(name, ss); err != nil {
			return nil, nil, err
		}
		switch {
		case ss.Ref != "":
			value, domains, err := s.resolveRef(ctx, workspaceID, name, ss)
			if err != nil {
				return nil, nil, err
			}
			out = append(out, runtime.Secret{
				Name: name, Value: string(value),
				AllowedDomains: domains, Source: types.SecretVault,
			})
			sanitized[name] = runtime.SecretSpec{Ref: ss.Ref, AllowedDomains: domains, Source: types.SecretVault}
		default:
			out = append(out, runtime.Secret{
				Name: name, Value: ss.Value,
				AllowedDomains: ss.AllowedDomains, Source: types.SecretInline,
			})
			sanitized[name] = runtime.SecretSpec{AllowedDomains: ss.AllowedDomains, Source: types.SecretInline}
		}
	}
	return out, sanitized, nil
}

// resolveRef decrypts a vault secret and returns its plaintext plus the
// effective allowlist from describeRef. Widening is rejected.
func (s *Service) resolveRef(ctx context.Context, workspaceID, name string, ss runtime.SecretSpec) ([]byte, []string, error) {
	if s.cipher == nil {
		return nil, nil, fmt.Errorf("secret %s: vault secrets disabled (no --secret-key)", name)
	}
	rec, domains, err := s.describeRef(ctx, workspaceID, name, ss)
	if err != nil {
		return nil, nil, err
	}
	value, err := s.cipher.Open(rec.Ciphertext, rec.UserID+"/"+rec.Name)
	if err != nil {
		return nil, nil, fmt.Errorf("secret %s: vault decrypt failed", name)
	}
	return value, domains, nil
}
