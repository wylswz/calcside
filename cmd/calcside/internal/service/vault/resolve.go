package vault

import (
	"context"
	"fmt"

	"calcside/internal/hostmatch"
	"calcside/internal/runtime"
	"calcside/internal/secrets"
	"calcside/internal/types"
)

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
func (s *Service) Resolve(ctx context.Context, userID string, in map[string]runtime.SecretSpec) ([]runtime.Secret, map[string]runtime.SecretSpec, error) {
	var out []runtime.Secret
	sanitized := map[string]runtime.SecretSpec{}
	for name, ss := range in {
		if ss.Ref != "" && ss.Value != "" {
			return nil, nil, fmt.Errorf("secret %s: exactly one of ref or value", name)
		}
		if ss.Source != "" && ss.Source != types.SecretVault && ss.Source != types.SecretInline {
			return nil, nil, fmt.Errorf("secret %s: invalid source %q", name, ss.Source)
		}
		if ss.Source == types.SecretVault && ss.Ref == "" {
			return nil, nil, fmt.Errorf("secret %s: source %q requires ref", name, ss.Source)
		}
		if ss.Source == types.SecretInline && ss.Value == "" {
			return nil, nil, fmt.Errorf("secret %s: source %q requires value", name, ss.Source)
		}
		switch {
		case ss.Ref != "":
			value, domains, err := s.resolveRef(ctx, userID, name, ss)
			if err != nil {
				return nil, nil, err
			}
			out = append(out, runtime.Secret{
				Name: name, Value: string(value),
				AllowedDomains: domains, Source: types.SecretVault,
			})
			sanitized[name] = runtime.SecretSpec{Ref: ss.Ref, AllowedDomains: domains, Source: types.SecretVault}
		case ss.Value != "":
			if len(ss.Value) > secrets.MaxValueBytes {
				return nil, nil, fmt.Errorf("secret %s: value exceeds %d bytes", name, secrets.MaxValueBytes)
			}
			if _, err := secrets.ValidateDomains(ss.AllowedDomains); err != nil {
				return nil, nil, fmt.Errorf("secret %s: %w", name, err)
			}
			out = append(out, runtime.Secret{
				Name: name, Value: ss.Value,
				AllowedDomains: ss.AllowedDomains, Source: types.SecretInline,
			})
			sanitized[name] = runtime.SecretSpec{AllowedDomains: ss.AllowedDomains, Source: types.SecretInline}
		default:
			return nil, nil, fmt.Errorf("secret %s: exactly one of ref or value", name)
		}
	}
	return out, sanitized, nil
}

// resolveRef decrypts a vault secret and returns its plaintext plus the
// effective allowlist: the vault's own, or the spec's if the spec
// narrowed it. Widening is rejected.
func (s *Service) resolveRef(ctx context.Context, userID, name string, ss runtime.SecretSpec) ([]byte, []string, error) {
	if s.cipher == nil {
		return nil, nil, fmt.Errorf("secret %s: vault secrets disabled (no --secret-key)", name)
	}
	rec, err := s.st.GetSecretByName(ctx, userID, ss.Ref)
	if err != nil {
		return nil, nil, fmt.Errorf("secret %s: unknown vault ref %q", name, ss.Ref)
	}
	value, err := s.cipher.Open(rec.Ciphertext, userID+"/"+rec.Name)
	if err != nil {
		return nil, nil, fmt.Errorf("secret %s: vault decrypt failed", name)
	}
	vaultRules, err := hostmatch.ParseAll(rec.AllowedDomains)
	if err != nil {
		return nil, nil, fmt.Errorf("secret %s: bad vault domains", name)
	}
	if len(ss.AllowedDomains) == 0 {
		return value, rec.AllowedDomains, nil
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
	return value, ss.AllowedDomains, nil
}
