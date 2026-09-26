package capability

import (
	"encoding/json"

	"calcside/internal/runtime"
	"calcside/internal/types"
)

// Bounds is the subset of the server limits a spec is normalized
// against. Both tiers normalize with these: the API tier before it
// persists the spec, the execution node when it builds the instance.
func (l ServerLimits) Bounds() runtime.Bounds {
	return runtime.Bounds{
		DefaultTTL:     l.DefaultTTL,
		MaxTTL:         l.MaxTTL,
		MaxExecTimeout: l.MaxExecTimeout,
		MaxSteps:       l.MaxSteps,
		MaxOutputBytes: l.MaxOutputBytes,
	}
}

// SpecValidator returns a runtime.CapValidator that checks a spec's
// capability configs against the registry. Typed configs are collected
// into out when it is non-nil; the API tier passes nil because it only
// needs the verdict, while a node keeps the configs to build bindings.
//
// Both tiers run this so a bad spec is rejected with a 400 before a
// placement is burned, and so a node never trusts that the API tier
// checked.
func SpecValidator(reg *Registry, limits ServerLimits, out map[string]any) runtime.CapValidator {
	return func(name string, raw json.RawMessage) error {
		f, ok := reg.Get(types.CapabilityName(name))
		if !ok {
			return runtime.Errf(runtime.ErrBadCapability, "unknown capability: %q", name)
		}
		cfg, err := f.Validate(raw, limits)
		if err != nil {
			return runtime.Errf(runtime.ErrBadSpec, "%s", err)
		}
		if out != nil {
			out[name] = cfg
		}
		return nil
	}
}

// NormalizeSpec parses, normalizes and validates a raw spec in one
// step, returning the normalized spec and the typed capability configs.
func NormalizeSpec(raw []byte, reg *Registry, limits ServerLimits) (*runtime.Spec, map[string]any, error) {
	var spec runtime.Spec
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &spec); err != nil {
			return nil, nil, runtime.Errf(runtime.ErrBadSpec, "invalid spec JSON: %s", err)
		}
	}
	typed := map[string]any{}
	if err := spec.Normalize(limits.Bounds(), SpecValidator(reg, limits, typed)); err != nil {
		return nil, nil, err
	}
	return &spec, typed, nil
}
