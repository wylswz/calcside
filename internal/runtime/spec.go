package runtime

import (
	"encoding/json"
	"time"

	"calcside/internal/secrets"
	"calcside/internal/types"
)

// Spec is the JSON document POSTed to create an instance. It is part of
// the tier contract: the API tier normalizes and persists it, and the
// normalized form is what travels to the node.
type Spec struct {
	TTLSeconds   int64                      `json:"ttl_seconds"`
	Labels       map[string]string          `json:"labels"`
	Capabilities map[string]json.RawMessage `json:"capabilities"`
	Limits       Limits                     `json:"limits"`
	Env          map[string]string          `json:"env"`
	Secrets      map[string]SecretSpec      `json:"secrets"`
}

// SecretSpec is one entry of spec.secrets: either a vault ref (name of a
// vault secret owned by the instance owner) or an inline value.
type SecretSpec struct {
	Ref            string             `json:"ref,omitempty"`
	Value          string             `json:"value,omitempty"`
	AllowedDomains []string           `json:"allowed_domains,omitempty"`
	Source         types.SecretSource `json:"source,omitempty"` // set on output; input "source" is validated for consistency
}

// Limits requested per instance (clamped by server bounds).
type Limits struct {
	ExecTimeoutMs  int64  `json:"exec_timeout_ms"`
	MaxSteps       uint64 `json:"max_steps"`
	MaxOutputBytes int64  `json:"max_output_bytes"`
}

// Defaults applied to instance limits.
const (
	defaultExecTimeoutMs = 30000
	defaultMaxSteps      = 10000000
	defaultMaxOutput     = 1 << 20

	maxEnvEntries   = 64
	maxEnvValueByte = 4 << 10
)

// Bounds are the server-side caps a spec is normalized against. Both
// tiers must be configured with the same values: the API tier
// normalizes and persists, and the node re-normalizes and fails closed
// if its own bounds are stricter.
type Bounds struct {
	DefaultTTL     time.Duration
	MaxTTL         time.Duration
	MaxExecTimeout time.Duration
	MaxSteps       uint64
	MaxOutputBytes int64
}

// CapValidator validates one capability's raw config. It is supplied by
// the caller so that this package stays free of the capability registry
// (and therefore of the Starlark runtime); implementations typically
// also retain the typed config they produce.
type CapValidator func(name string, raw json.RawMessage) error

// Normalize fills defaults, clamps requested limits to the server
// bounds, and validates env and secret names in place. Capability
// configs are checked through validate, which may be nil to skip them.
//
// It is idempotent: normalizing an already-normalized spec is a no-op,
// which is what lets the API tier normalize once, persist, and ship the
// result to a node that normalizes again.
func (s *Spec) Normalize(b Bounds, validate CapValidator) error {
	if s.TTLSeconds <= 0 {
		s.TTLSeconds = int64(b.DefaultTTL / time.Second)
	}
	if b.MaxTTL > 0 && time.Duration(s.TTLSeconds)*time.Second > b.MaxTTL {
		return Errf(ErrBadSpec, "ttl_seconds exceeds server max %d", int64(b.MaxTTL/time.Second))
	}
	if s.Limits.ExecTimeoutMs <= 0 {
		s.Limits.ExecTimeoutMs = defaultExecTimeoutMs
	}
	if b.MaxExecTimeout > 0 && time.Duration(s.Limits.ExecTimeoutMs)*time.Millisecond > b.MaxExecTimeout {
		return Errf(ErrBadSpec, "exec_timeout_ms exceeds server max %d", b.MaxExecTimeout.Milliseconds())
	}
	if s.Limits.MaxSteps == 0 {
		s.Limits.MaxSteps = b.MaxSteps
		if s.Limits.MaxSteps == 0 {
			s.Limits.MaxSteps = defaultMaxSteps
		}
	}
	if b.MaxSteps > 0 && s.Limits.MaxSteps > b.MaxSteps {
		s.Limits.MaxSteps = b.MaxSteps
	}
	if s.Limits.MaxOutputBytes <= 0 {
		s.Limits.MaxOutputBytes = b.MaxOutputBytes
		if s.Limits.MaxOutputBytes == 0 {
			s.Limits.MaxOutputBytes = defaultMaxOutput
		}
	}
	if b.MaxOutputBytes > 0 && s.Limits.MaxOutputBytes > b.MaxOutputBytes {
		s.Limits.MaxOutputBytes = b.MaxOutputBytes
	}
	if validate != nil {
		for name, raw := range s.Capabilities {
			if err := validate(name, raw); err != nil {
				return err
			}
		}
	}
	if len(s.Env) > maxEnvEntries {
		return Errf(ErrBadSpec, "env: more than %d entries", maxEnvEntries)
	}
	for k, v := range s.Env {
		if !secrets.ValidName(k) {
			return Errf(ErrBadSpec, "env: invalid name %q", k)
		}
		if len(v) > maxEnvValueByte {
			return Errf(ErrBadSpec, "env: value for %s exceeds %d bytes", k, maxEnvValueByte)
		}
	}
	// Secret values are resolved by the API tier; only names are checked
	// here, because both tiers must agree on what a legal name is.
	for name := range s.Secrets {
		if !secrets.ValidName(name) {
			return Errf(ErrBadSpec, "secrets: invalid name %q", name)
		}
		if _, isEnv := s.Env[name]; isEnv {
			return Errf(ErrBadSpec, "secrets: name %q also used by env", name)
		}
	}
	return nil
}

// Sanitized returns the spec as it should be persisted and shipped:
// structure and env intact, with spec.secrets replaced by the resolved
// descriptors (inline values stripped, vault refs carrying their
// effective allowlist). An instance's stored spec must never contain a
// secret value.
func (s *Spec) Sanitized(resolved map[string]SecretSpec) json.RawMessage {
	out := *s
	if len(s.Secrets) > 0 {
		out.Secrets = resolved
	}
	b, err := json.Marshal(&out)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}
