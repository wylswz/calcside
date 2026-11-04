package runtime

import (
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"calcside/internal/module"
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
	// Policies names the built-in and library policies attached to this
	// instance. Nil selects defaults; an empty list selects none. Server
	// policies (--policy-dir) always apply and are never listed here.
	Policies []string `json:"policies"`
	// Modules maps mount alias → module reference (EP-0017). The alias
	// is the @-namespace consumer code loads through. Nil and an empty
	// map both mean "no modules"; the distinction is preserved through
	// serialization as policies are.
	Modules map[string]module.Reference `json:"modules"`
	// Environment is the consumer-owned configuration scope shared by
	// instances, applications and module debug runs: per-extension
	// named bindings of vault-secret references. It carries references
	// only — never resolved values.
	Environment *Environment `json:"environment,omitempty"`
}

// Environment is the spec.environment object: extension alias →
// per-binding secret references the consumer supplies for that
// extension's manifest secret fields. Field names are validated
// against the pinned extension manifest at resolution time; only the
// shape and reference syntax are checked at normalization.
type Environment struct {
	Extensions map[string]ExtensionEnvironment `json:"extensions,omitempty"`
}

// ExtensionEnvironment holds named bindings for one extension alias.
// Execution always selects the binding named "default"; other names
// exist in the data model for later selection surfaces.
type ExtensionEnvironment struct {
	Bindings map[string]SecretBinding `json:"bindings,omitempty"`
}

// SecretBinding maps manifest secret-field name → vault reference.
type SecretBinding struct {
	Secrets map[string]SecretRef `json:"secrets,omitempty"`
}

// strictFields decodes data into out after rejecting unknown keys —
// the environment tree must never silently drop runtime attributes.
func strictFields(data []byte, allowed []string, out any) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for k := range raw {
		ok := false
		for _, a := range allowed {
			if k == a {
				ok = true
				break
			}
		}
		if !ok {
			return fmt.Errorf("environment: unsupported field %q (references only)", k)
		}
	}
	return json.Unmarshal(data, out)
}

// UnmarshalJSON rejects unknown top-level environment keys.
func (e *Environment) UnmarshalJSON(data []byte) error {
	type plain Environment
	return strictFields(data, []string{"extensions"}, (*plain)(e))
}

// UnmarshalJSON rejects unknown per-extension keys.
func (e *ExtensionEnvironment) UnmarshalJSON(data []byte) error {
	type plain ExtensionEnvironment
	return strictFields(data, []string{"bindings"}, (*plain)(e))
}

// UnmarshalJSON rejects unknown per-binding keys.
func (b *SecretBinding) UnmarshalJSON(data []byte) error {
	type plain SecretBinding
	return strictFields(data, []string{"secrets"}, (*plain)(b))
}

// SecretRef references a workspace vault secret by name, with an
// optional per-binding domain allowlist narrowing the vault entry's
// own allowlist. Inline values are rejected at decode time.
type SecretRef struct {
	Ref            string   `json:"ref"`
	AllowedDomains []string `json:"allowed_domains,omitempty"`
}

// UnmarshalJSON rejects inline credential material: only ref and
// allowed_domains may appear in a binding field.
func (s *SecretRef) UnmarshalJSON(data []byte) error {
	type plain SecretRef
	return strictFields(data, []string{"ref", "allowed_domains"}, (*plain)(s))
}

// Environment bounds checked at normalization.
const (
	maxEnvExtensions = 32
	maxEnvBindings   = 16
	maxEnvSecrets    = 32
)

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
	maxPolicies     = 32
)

var policyNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

// ValidPolicyName reports whether name is a legal policy name: what a
// spec may reference in spec.policies and what the library may store.
func ValidPolicyName(name string) bool { return policyNameRe.MatchString(name) }

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
	s.Policies = EffectivePolicies(s.Policies)
	if len(s.Policies) > maxPolicies {
		return Errf(ErrBadSpec, "policies: more than %d entries", maxPolicies)
	}
	seen := make(map[string]bool, len(s.Policies))
	for _, name := range s.Policies {
		if !ValidPolicyName(name) {
			return Errf(ErrBadSpec, "policies: invalid name %q", name)
		}
		if seen[name] {
			return Errf(ErrBadSpec, "policies: duplicate %q", name)
		}
		seen[name] = true
	}
	if err := s.normalizeModules(); err != nil {
		return err
	}
	return s.normalizeEnvironment()
}

// normalizeModules checks the mount map: mount grammar, module-name
// grammar (name defaults to the alias), positive versions when set,
// and h1: sum shape when pinned. Mount count is bounded with the
// aggregate checked on the attached trees, not here — a spec carries
// references only.
func (s *Spec) normalizeModules() error {
	if len(s.Modules) > module.MaxMounts {
		return Errf(ErrBadSpec, "modules: more than %d mounts", module.MaxMounts)
	}
	for alias, ref := range s.Modules {
		if !module.ValidMount(alias) {
			return Errf(ErrBadSpec, "modules: invalid mount alias %q", alias)
		}
		if ref.Name == "" {
			ref.Name = alias
		}
		if !module.ValidName(ref.Name) {
			return Errf(ErrBadSpec, "modules: invalid name %q for mount %q", ref.Name, alias)
		}
		if ref.Version < 0 {
			return Errf(ErrBadSpec, "modules: invalid version for mount %q", alias)
		}
		if ref.Sum != "" && !module.ValidSum(ref.Sum) {
			return Errf(ErrBadSpec, "modules: mount %q sum must be an h1: hash", alias)
		}
		for _, e := range ref.Ext {
			if !identRe.MatchString(e) {
				return Errf(ErrBadSpec, "modules: mount %q has invalid ext alias %q", alias, e)
			}
		}
		s.Modules[alias] = ref
	}
	return nil
}

// Validate checks the consumer-owned environment scope's shape:
// bounded maps, alias/binding/field grammars, vault-name refs and
// domain allowlists. Field↔manifest and alias↔spec resolution happens
// later (sandbox); this is the shared shape contract both the spec
// normalizer and the service-side pre-validation enforce.
func (e *Environment) Validate() error {
	if e == nil {
		return nil
	}
	if len(e.Extensions) > maxEnvExtensions {
		return fmt.Errorf("environment: more than %d extension entries", maxEnvExtensions)
	}
	for alias, ee := range e.Extensions {
		if !identRe.MatchString(alias) {
			return fmt.Errorf("environment: invalid extension alias %q", alias)
		}
		if len(ee.Bindings) == 0 || len(ee.Bindings) > maxEnvBindings {
			return fmt.Errorf("environment: extension %q needs 1..%d bindings", alias, maxEnvBindings)
		}
		for bname, b := range ee.Bindings {
			if !module.ValidMount(bname) {
				return fmt.Errorf("environment: invalid binding name %q for extension %q", bname, alias)
			}
			if len(b.Secrets) == 0 || len(b.Secrets) > maxEnvSecrets {
				return fmt.Errorf("environment: binding %q for extension %q needs 1..%d secret fields", bname, alias, maxEnvSecrets)
			}
			for field, ref := range b.Secrets {
				if !identRe.MatchString(field) {
					return fmt.Errorf("environment: invalid secret field %q for extension %q", field, alias)
				}
				if !secrets.ValidName(ref.Ref) {
					return fmt.Errorf("environment: invalid vault reference %q for extension %q field %q", ref.Ref, alias, field)
				}
				if _, err := secrets.ValidateDomains(ref.AllowedDomains); err != nil {
					return fmt.Errorf("environment: extension %q field %q: %s", alias, field, err)
				}
			}
		}
	}
	return nil
}

// normalizeEnvironment checks the consumer-owned environment scope
// via the shared Validate contract.
func (s *Spec) normalizeEnvironment() error {
	if s.Environment == nil {
		return nil
	}
	if err := s.Environment.Validate(); err != nil {
		return Errf(ErrBadSpec, "%s", err)
	}
	return nil
}

// identRe is the Starlark identifier grammar used for ext aliases and
// manifest field names (≤64 chars).
var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

// VerifyModuleMounts checks a request's attached trees against the
// spec's normalized module references — the node must never silently
// create a module-less instance when the spec declares mounts, and an
// attached tree must match its reference exactly (name, version, sum).
// A version-0 reference is the debug-draft form and pairs only with a
// version-0 tree. Extra mounts the spec never declared are rejected.
func VerifyModuleMounts(refs map[string]module.Reference, trees map[string]module.Tree) error {
	for alias, ref := range refs {
		t, ok := trees[alias]
		if !ok {
			return Errf(ErrBadSpec, "modules: spec mount %q has no attached tree", alias)
		}
		if t.Name != ref.Name || t.Version != ref.Version {
			return Errf(ErrBadSpec, "modules: mount %q tree %s v%d does not match reference %s v%d", alias, t.Name, t.Version, ref.Name, ref.Version)
		}
		if t.Sum != ref.Sum {
			return Errf(ErrBadSpec, "modules: mount %q tree sum does not match the pinned reference", alias)
		}
	}
	for alias := range trees {
		if _, ok := refs[alias]; !ok {
			return Errf(ErrBadSpec, "modules: attached mount %q is not declared by the spec", alias)
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
