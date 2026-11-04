package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"slices"
	"strings"

	"calcside/cmd/calcside/internal/service"
	capext "calcside/internal/capability/ext"
	"calcside/internal/runtime"
	"calcside/internal/types"
)

// environment.go resolves spec.environment — the consumer-owned
// per-extension secret bindings — into the existing ext.config
// placeholder mechanism plus spec.secrets references, running
// between PinSpec and NormalizeSpec.
//
// Design: a module or consumer never carries credentials. Each
// environment binding names vault references for the pinned
// extension's manifest `secret` fields. The `default` binding is the
// only one execution uses (v1 never falls back); secondary bindings
// are validated descriptor-only — existence and domain narrowing are
// checked, plaintext is never opened. Deterministic internal secret
// names (EXT_BIND_<hash>) make normalization idempotent: a stored
// spec re-submitted carries the same generated entries and passes
// unchanged; any different consumer-supplied content under those
// names or config fields is a conflict, never an overwrite.
//
// Effective domains (the vault allowlist, or the binding's narrowing)
// are written back into the environment object and the generated
// secret descriptors, so a normalized spec re-validates identically.

// envSecretName derives the deterministic internal spec.secrets name
// for one (alias, field) binding: EXT_BIND_ + uppercase hex of the
// first 24 digest bytes — always a legal secret name (57 chars).
func envSecretName(alias, field string) string {
	sum := sha256.Sum256([]byte(alias + "\x00" + field))
	return "EXT_BIND_" + strings.ToUpper(hex.EncodeToString(sum[:24]))
}

// applyEnvironment rewrites rawSpec in place shape: it validates every
// environment binding against the pinned extension manifests and
// merges the `default` bindings into capabilities.ext[alias].config
// ({{secrets.NAME}} placeholders) plus spec.secrets (vault refs
// carrying each binding's effective domains). The environment object
// is rewritten with effective domains so the stored normalized form
// round-trips the binding view and later review sees what the vault
// actually allows.
func (s *Service) applyEnvironment(ctx context.Context, wsID string, raw []byte) ([]byte, error) {
	var spec map[string]json.RawMessage
	if err := json.Unmarshal(raw, &spec); err != nil || spec == nil {
		return raw, nil // malformed JSON is reported by NormalizeSpec
	}
	rawEnv, ok := spec["environment"]
	if !ok || string(rawEnv) == "null" {
		return raw, nil
	}
	// Decode the typed contract first — strict shape and bounds are
	// enforced before any manifest or vault work, and malformed
	// entries fail fast rather than mid-resolution.
	var env runtime.Environment
	if err := json.Unmarshal(rawEnv, &env); err != nil {
		return nil, service.Errf(types.ErrCodeBadSpec, "environment: %s", err.Error())
	}
	if err := env.Validate(); err != nil {
		return nil, service.Errf(types.ErrCodeBadSpec, "%s", err)
	}
	if len(env.Extensions) == 0 {
		return raw, nil
	}
	if s.manifests == nil {
		return nil, service.Errf(types.ErrCodeBadSpec, "environment: extension manifest resolution unavailable")
	}
	if s.secrets == nil {
		return nil, service.Errf(types.ErrCodeBadSpec, "environment: secret resolution unavailable")
	}

	var caps map[string]json.RawMessage
	if c := spec["capabilities"]; c != nil {
		if err := json.Unmarshal(c, &caps); err != nil || caps == nil {
			return nil, service.Errf(types.ErrCodeBadSpec, "capabilities must be an object")
		}
	} else {
		caps = map[string]json.RawMessage{}
	}
	aliases := map[string]json.RawMessage{}
	if e := caps["ext"]; e != nil && string(e) != "null" {
		if err := json.Unmarshal(e, &aliases); err != nil || aliases == nil {
			return nil, service.Errf(types.ErrCodeBadSpec, "capabilities.ext must be an object")
		}
	}
	secs := map[string]json.RawMessage{}
	if sm := spec["secrets"]; sm != nil && string(sm) != "null" {
		if err := json.Unmarshal(sm, &secs); err != nil || secs == nil {
			return nil, service.Errf(types.ErrCodeBadSpec, "secrets must be an object")
		}
	}
	changed := false

	for _, alias := range slices.Sorted(maps.Keys(env.Extensions)) {
		ee := env.Extensions[alias]
		def, hasDefault := ee.Bindings["default"]
		if !hasDefault {
			return nil, service.Errf(types.ErrCodeBadSpec, "environment: extension %q requires a default binding", alias)
		}
		entryJSON, ok := aliases[alias]
		if !ok {
			return nil, service.Errf(types.ErrCodeBadSpec, "environment: extension %q is not granted by the spec", alias)
		}
		var entry map[string]json.RawMessage
		if err := json.Unmarshal(entryJSON, &entry); err != nil || entry == nil {
			return nil, service.Errf(types.ErrCodeBadSpec, "environment: extension %q has invalid configuration", alias)
		}
		var source, sum string
		_ = json.Unmarshal(entry["source"], &source)
		_ = json.Unmarshal(entry["sum"], &sum)
		manifest, err := s.manifests.Manifest(ctx, source, sum)
		if err != nil {
			return nil, service.Errf(types.ErrCodeBadSpec, "environment: extension %q source cannot be inspected", alias)
		}
		secretFields := map[string]capext.ConfigField{}
		for _, cf := range manifest.Config {
			if cf.Type == "secret" {
				secretFields[cf.Name] = cf
			}
		}

		// Every binding (default and secondary) is field-checked
		// against the manifest and descriptor-checked against the
		// vault — never decrypted here — then rewritten with the
		// effective domains so review sees the real allowlist.
		for bname, b := range ee.Bindings {
			refs := map[string]runtime.SecretSpec{}
			for field, ref := range b.Secrets {
				if _, ok := secretFields[field]; !ok {
					return nil, service.Errf(types.ErrCodeBadSpec, "environment: %q is not a secret field of extension %q", field, alias)
				}
				refs[field] = runtime.SecretSpec{Ref: ref.Ref, AllowedDomains: ref.AllowedDomains}
			}
			described, err := s.secrets.Describe(ctx, wsID, refs)
			if err != nil {
				return nil, service.Errf(types.ErrCodeBadSpec, "environment: binding %q: %s", bname, err)
			}
			for field, d := range described {
				cur := b.Secrets[field]
				if !slices.Equal(cur.AllowedDomains, d.AllowedDomains) {
					cur.AllowedDomains = d.AllowedDomains
					b.Secrets[field] = cur
					changed = true
				}
			}
			ee.Bindings[bname] = b
		}
		env.Extensions[alias] = ee

		// Manifest secret fields without a default value are required:
		// the default binding must cover them.
		for _, cf := range manifest.Config {
			if cf.Type != "secret" || cf.Default != nil {
				continue
			}
			if _, ok := def.Secrets[cf.Name]; !ok {
				return nil, service.Errf(types.ErrCodeBadSpec, "environment: extension %q requires a binding for secret field %q", alias, cf.Name)
			}
		}

		if len(def.Secrets) == 0 {
			continue
		}
		config := map[string]json.RawMessage{}
		if c := entry["config"]; c != nil && string(c) != "null" {
			if err := json.Unmarshal(c, &config); err != nil || config == nil {
				return nil, service.Errf(types.ErrCodeBadSpec, "environment: extension %q config must be an object", alias)
			}
		}
		for _, field := range slices.Sorted(maps.Keys(def.Secrets)) {
			// def.Secrets was rewritten with effective domains above.
			ref := def.Secrets[field]
			internal := envSecretName(alias, field)
			want := runtime.SecretSpec{Ref: ref.Ref, AllowedDomains: ref.AllowedDomains, Source: types.SecretVault}
			wb, _ := json.Marshal(want)
			if existing, ok := secs[internal]; ok {
				// Idempotence: only a previously generated vault
				// descriptor with identical ref+effective domains
				// may occupy a generated name. Inline values or a
				// different marker are collisions, never overwrites.
				var cur runtime.SecretSpec
				if err := json.Unmarshal(existing, &cur); err != nil ||
					cur.Value != "" ||
					(cur.Source != "" && cur.Source != types.SecretVault) ||
					cur.Ref != want.Ref || !slices.Equal(cur.AllowedDomains, want.AllowedDomains) {
					return nil, service.Errf(types.ErrCodeBadSpec, "environment: generated secret name %s collides with existing entry", internal)
				}
			} else {
				secs[internal] = wb
				changed = true
			}
			placeholder, _ := json.Marshal("{{secrets." + internal + "}}")
			if cur, ok := config[field]; ok && string(cur) != string(placeholder) {
				return nil, service.Errf(types.ErrCodeBadSpec, "environment: extension %q field %q conflicts with existing config", alias, field)
			}
			if !ok || string(config[field]) != string(placeholder) {
				config[field] = placeholder
				changed = true
			}
		}
		cb, _ := json.Marshal(config)
		entry["config"] = cb
		ab, _ := json.Marshal(entry)
		aliases[alias] = ab
	}

	if !changed {
		return raw, nil
	}
	eb, _ := json.Marshal(aliases)
	caps["ext"] = eb
	cb, _ := json.Marshal(caps)
	spec["capabilities"] = cb
	sb, _ := json.Marshal(secs)
	spec["secrets"] = sb
	// The rewritten environment (effective domains) replaces the
	// request form; strict decode makes it loss-free.
	enb, _ := json.Marshal(&env)
	spec["environment"] = enb
	return json.Marshal(spec)
}

// StripEnvironmentDerived removes the materialized secrets/config
// placeholders applyEnvironment injects so a stored draft stays
// reference-only: spec.secrets[EXT_BIND_*] is removed ONLY when the
// entry is the exact generated descriptor (vault source, matching ref
// + effective domains, no inline value); ext.<alias>.config[field] is
// removed ONLY when it is the exact {{secrets.INTERNAL}} placeholder.
// Anything else is left untouched — collisions are errors, never
// silent overwrites.
func StripEnvironmentDerived(spec json.RawMessage) (json.RawMessage, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(spec, &raw); err != nil || raw == nil {
		return spec, nil
	}
	var env runtime.Environment
	if e, ok := raw["environment"]; ok && len(e) > 0 {
		if err := json.Unmarshal(e, &env); err != nil {
			return spec, nil
		}
	}
	if len(env.Extensions) == 0 {
		return spec, nil
	}
	var secs map[string]json.RawMessage
	if s, ok := raw["secrets"]; ok {
		_ = json.Unmarshal(s, &secs)
	}
	// Rebuild the secrets map without generated entries that still
	// match their authoritative binding.
	pruned := map[string]json.RawMessage{}
	for k, v := range secs {
		pruned[k] = v
	}
	verified := map[string]map[string]bool{}
	for alias, e := range env.Extensions {
		for binding, b := range e.Bindings {
			if binding != "default" {
				continue
			}
			for field, ref := range b.Secrets {
				internal := envSecretName(alias, field)
				cur, ok := pruned[internal]
				if !ok {
					continue // nothing derived to strip
				}
				var spec runtime.SecretSpec
				if err := json.Unmarshal(cur, &spec); err != nil {
					return nil, service.Errf(types.ErrCodeBadSpec, "environment: generated secret name %s collides with existing entry", internal)
				}
				// Only strip an exact generated descriptor — a user-
				// owned entry with the same name is a collision.
				if spec.Value != "" || (spec.Source != "" && spec.Source != types.SecretVault) ||
					spec.Ref != ref.Ref || !slices.Equal(spec.AllowedDomains, ref.AllowedDomains) {
					return nil, service.Errf(types.ErrCodeBadSpec, "environment: generated secret name %s collides with existing entry", internal)
				}
				delete(pruned, internal)
				if verified[alias] == nil {
					verified[alias] = map[string]bool{}
				}
				verified[alias][field] = true
			}
		}
	}
	if len(secs) > 0 {
		if len(pruned) == 0 {
			delete(raw, "secrets")
		} else {
			raw["secrets"], _ = json.Marshal(pruned)
		}
	}
	// Remove exact {{secrets.INTERNAL}} placeholders from ext config —
	// ONLY for fields the default binding declares AND whose derived
	// descriptor was verified + stripped above. Arbitrary EXT_BIND_*
	// placeholders stay untouched.
	if c, ok := raw["capabilities"]; ok {
		var caps map[string]json.RawMessage
		if err := json.Unmarshal(c, &caps); err == nil {
			if ext, ok := caps["ext"]; ok {
				var extMap map[string]json.RawMessage
				if err := json.Unmarshal(ext, &extMap); err == nil {
					for alias, entry := range extMap {
						var cfg struct {
							Config map[string]json.RawMessage `json:"config"`
						}
						if err := json.Unmarshal(entry, &cfg); err != nil || cfg.Config == nil {
							continue
						}
						changed := false
						for field := range verified[alias] {
							internal := envSecretName(alias, field)
							placeholder, _ := json.Marshal("{{secrets." + internal + "}}")
							if string(cfg.Config[field]) == string(placeholder) {
								delete(cfg.Config, field)
								changed = true
							}
						}
						if changed {
							var ent map[string]json.RawMessage
							_ = json.Unmarshal(entry, &ent)
							if len(cfg.Config) == 0 {
								delete(ent, "config")
							} else {
								ent["config"], _ = json.Marshal(cfg.Config)
							}
							extMap[alias], _ = json.Marshal(ent)
						}
					}
					caps["ext"], _ = json.Marshal(extMap)
					raw["capabilities"], _ = json.Marshal(caps)
				}
			}
		}
	}
	return json.Marshal(raw)
}
