package ext

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"calcside/internal/capability"
	"calcside/internal/types"
)

const maxArgLen = 256

// Factory returns the capability.Factory for "ext" bound to opts.
func Factory(opts Options) capability.Factory {
	return factory{opts: &opts, locks: &fetchLocks{}}
}

type factory struct {
	opts  *Options
	locks *fetchLocks
}

func (factory) Name() types.CapabilityName { return types.CapExt }

// Ops is nil: ops are dynamic, declared per extension manifest.
func (factory) Ops() []capability.OpInfo { return nil }

func (factory) ConfigFields() []capability.FieldDoc {
	return []capability.FieldDoc{
		{Name: "source", Type: types.FieldString, Doc: "extension identifier: {domain}/{group}/{name}@{version} or an absolute local path"},
		{Name: "sum", Type: types.FieldString, Doc: "h1: integrity sum; required for remote sources"},
		{Name: "config", Type: types.FieldStringMap, Doc: "per-extension config keys declared in its capability.yaml"},
	}
}

// aliasSpec is one entry of capabilities.ext: {source,sum,config}.
type aliasSpec struct {
	Source string         `json:"source"`
	Sum    string         `json:"sum"`
	Config map[string]any `json:"config"`
}

// aliasCfg is a validated alias: loaded module plus effective config.
type aliasCfg struct {
	mod    *Module
	source string
	sum    string
	config map[string]any
}

type validated map[string]aliasCfg

func (f factory) Validate(raw json.RawMessage, limits capability.ServerLimits) (any, error) {
	var aliases map[string]json.RawMessage
	if err := json.Unmarshal(raw, &aliases); err != nil {
		return nil, fmt.Errorf("ext config: %w", err)
	}
	if len(aliases) == 0 {
		return nil, fmt.Errorf("ext config: at least one extension alias is required")
	}
	out := validated{}
	for alias, rawAlias := range aliases {
		if !identRe.MatchString(alias) {
			return nil, fmt.Errorf("ext: invalid alias %q (must be a starlark identifier)", alias)
		}
		var as aliasSpec
		if err := json.Unmarshal(rawAlias, &as); err != nil {
			return nil, fmt.Errorf("ext %s: %w", alias, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), f.opts.FetchTimeout)
		if f.opts.FetchTimeout <= 0 {
			ctx, cancel = context.WithTimeout(context.Background(), defaultFetchTimeout)
		}
		mod, err := (&CapabilityLoader{
			Identifier: CapabilityIdentifier(as.Source),
			Sum:        as.Sum,
			Options:    f.opts,
			locks:      f.locks,
		}).Load(ctx)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("ext %s: %w", alias, err)
		}
		cfg, err := validateConfig(alias, mod.Manifest.Config, as.Config)
		if err != nil {
			return nil, err
		}
		out[alias] = aliasCfg{mod: mod, source: as.Source, sum: as.Sum, config: cfg}
	}
	return out, nil
}

// validateConfig applies manifest defaults, rejects unknown keys and type
// mismatches.
func validateConfig(alias string, fields []ConfigField, raw map[string]any) (map[string]any, error) {
	declared := map[string]ConfigField{}
	for _, f := range fields {
		declared[f.Name] = f
	}
	for k := range raw {
		if _, ok := declared[k]; !ok {
			return nil, fmt.Errorf("ext %s: unknown config key %q", alias, k)
		}
	}
	out := map[string]any{}
	for _, f := range fields {
		v, ok := raw[f.Name]
		if !ok || v == nil {
			if f.Default != nil {
				v = f.Default
			} else {
				continue
			}
		}
		cv, err := coerceConfig(alias, f, v)
		if err != nil {
			return nil, err
		}
		out[f.Name] = cv
	}
	return out, nil
}

// secretRefRe matches a whole-string secret placeholder; secretRefFindRe
// finds placeholders inside arbitrary strings. Same NAME grammar as net.
var (
	secretRefRe     = regexp.MustCompile(`^\{\{\s*secrets\.([A-Z_][A-Z0-9_]{0,63})\s*\}\}$`)
	secretRefFindRe = regexp.MustCompile(`\{\{\s*secrets\.[A-Z_][A-Z0-9_]{0,63}\s*\}\}`)
)

// secretRefName extracts the NAME from a normalized secret field value.
func secretRefName(v any) string {
	s, _ := v.(string)
	if m := secretRefRe.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}

// noSecretRefs rejects {{secrets.X}} placeholders in non-secret values.
func noSecretRefs(alias string, f ConfigField, v any) error {
	bad := fmt.Errorf("ext %s: config %s contains a secret reference; declare it as type secret", alias, f.Name)
	switch t := v.(type) {
	case string:
		if secretRefFindRe.MatchString(t) {
			return bad
		}
	case []any:
		for _, e := range t {
			if s, ok := e.(string); ok && secretRefFindRe.MatchString(s) {
				return bad
			}
		}
	case map[string]any:
		for _, e := range t {
			if s, ok := e.(string); ok && secretRefFindRe.MatchString(s) {
				return bad
			}
		}
	}
	return nil
}

func coerceConfig(alias string, f ConfigField, v any) (any, error) {
	errf := func() error {
		return fmt.Errorf("ext %s: config %s must be %s, got %v", alias, f.Name, f.Type, v)
	}
	if f.Type == "secret" {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("ext %s: config %s must be a secret reference like {{secrets.NAME}}", alias, f.Name)
		}
		m := secretRefRe.FindStringSubmatch(s)
		if m == nil {
			return nil, fmt.Errorf("ext %s: config %s must be a secret reference like {{secrets.NAME}}", alias, f.Name)
		}
		return "{{secrets." + m[1] + "}}", nil
	}
	switch types.FieldType(f.Type) {
	case types.FieldInt:
		switch n := v.(type) {
		case float64:
			if n != float64(int64(n)) {
				return nil, errf()
			}
			return int64(n), nil
		case int, int64:
			return n, nil
		}
		return nil, errf()
	case types.FieldBool:
		if b, ok := v.(bool); ok {
			return b, nil
		}
		return nil, errf()
	case types.FieldString:
		if s, ok := v.(string); ok {
			if err := noSecretRefs(alias, f, s); err != nil {
				return nil, err
			}
			return s, nil
		}
		return nil, errf()
	case types.FieldStringList:
		l, ok := v.([]any)
		if !ok {
			return nil, errf()
		}
		for _, e := range l {
			if _, ok := e.(string); !ok {
				return nil, errf()
			}
		}
		if err := noSecretRefs(alias, f, v); err != nil {
			return nil, err
		}
		return v, nil
	case types.FieldStringMap:
		m, ok := v.(map[string]any)
		if !ok {
			return nil, errf()
		}
		for _, e := range m {
			if _, ok := e.(string); !ok {
				return nil, errf()
			}
		}
		if err := noSecretRefs(alias, f, v); err != nil {
			return nil, err
		}
		return v, nil
	}
	return nil, fmt.Errorf("ext %s: config %s has unknown type %q", alias, f.Name, f.Type)
}

func (f factory) Prompt(cfgAny any) string {
	v, _ := cfgAny.(validated)
	var b strings.Builder
	b.WriteString("#### `ext`\n")
	aliases := make([]string, 0, len(v))
	for a := range v {
		aliases = append(aliases, a)
	}
	sort.Strings(aliases)
	for _, alias := range aliases {
		ac := v[alias]
		if ac.mod.Manifest.Description != "" {
			fmt.Fprintf(&b, "- `ext.%s` — %s\n", alias, ac.mod.Manifest.Description)
		} else {
			fmt.Fprintf(&b, "- `ext.%s` (%s)\n", alias, ac.source)
		}
		for _, op := range ac.mod.Manifest.Ops {
			b.WriteString("  " + capability.OpLine(types.CapabilityName("ext."+alias),
				capability.OpInfo{Name: types.Op(op.Name), Doc: op.Doc, Params: op.Params}) + "\n")
		}
		if len(ac.mod.Manifest.Dependencies) > 0 {
			deps := make([]string, len(ac.mod.Manifest.Dependencies))
			for i, d := range ac.mod.Manifest.Dependencies {
				deps[i] = string(d)
			}
			fmt.Fprintf(&b, "  - Uses base capabilities: %s\n", strings.Join(deps, ", "))
		}
		var used []string
		for _, cf := range ac.mod.Manifest.Config {
			if cf.Type == "secret" {
				if n := secretRefName(ac.config[cf.Name]); n != "" {
					used = append(used, cf.Name+" ← "+n)
				}
			}
		}
		if len(used) > 0 {
			fmt.Fprintf(&b, "  - Secrets used: %s\n", strings.Join(used, ", "))
		}
	}
	return b.String()
}

// New executes each extension's module init and binds ext.<alias>.<op>
// builtins that route through the gate.
func (f factory) New(cfgAny any, env capability.InstanceEnv) (starlark.Value, io.Closer, error) {
	v, ok := cfgAny.(validated)
	if !ok {
		return nil, nil, fmt.Errorf("ext: config must come from Validate")
	}
	aliases := make([]string, 0, len(v))
	for a := range v {
		aliases = append(aliases, a)
	}
	sort.Strings(aliases)
	extMembers := starlark.StringDict{}
	for _, alias := range aliases {
		ac := v[alias]
		globals, err := ac.mod.init(env.Bindings, ac.config, env.MaxSteps)
		if err != nil {
			return nil, nil, err
		}
		// Secret config refs must resolve to instance-defined secrets;
		// the config dict carries only the placeholder string and net
		// performs injection/redaction/domain checks at request time.
		for _, cf := range ac.mod.Manifest.Config {
			if cf.Type != "secret" {
				continue
			}
			name := secretRefName(ac.config[cf.Name])
			if name == "" {
				continue
			}
			if env.Secrets == nil || env.Secrets.Lookup(name) == nil {
				return nil, nil, fmt.Errorf("ext %s: config %s references secret %q which this instance does not define", alias, cf.Name, name)
			}
		}
		members := starlark.StringDict{}
		for _, op := range ac.mod.Manifest.Ops {
			fn := globals[op.Name]
			if _, ok := fn.(starlark.Callable); !ok {
				return nil, nil, fmt.Errorf("ext %s: %q is not callable", alias, op.Name)
			}
			members[op.Name] = opBuiltin(alias, op, fn, ac, env.Gate)
		}
		extMembers[alias] = starlarkstruct.FromStringDict(starlarkstruct.Default, members)
	}
	return starlarkstruct.FromStringDict(starlarkstruct.Default, extMembers), nil, nil
}

// opBuiltin builds the starlark builtin for ext.<alias>.<op>: args are
// summarized into gate Call.Args and the real call runs inside
// Gate.Invoke on the same thread (inheriting step limit + cancellation).
func opBuiltin(alias string, op OpSpec, fn starlark.Value, ac aliasCfg, gate *capability.Gate) starlark.Value {
	return starlark.NewBuiltin("ext."+alias+"."+op.Name,
		func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			return gate.Invoke(capability.ThreadContext(thread), types.CapExt,
				types.Op(alias+"."+op.Name), callArgs(op, args, kwargs),
				func(ctx context.Context, _ map[string]any) (starlark.Value, map[string]any, error) {
					v, err := starlark.Call(thread, fn, args, kwargs)
					meta := map[string]any{"source": ac.source}
					if ac.sum != "" {
						meta["sum"] = ac.sum
					}
					if ac.mod.Commit != "" {
						meta["commit"] = ac.mod.Commit
					}
					return v, meta, err
				})
		})
}

// callArgs summarizes call arguments for audit: param names for declared
// params, JSON-able scalars verbatim, strings truncated, anything else by
// type name.
func callArgs(op OpSpec, args starlark.Tuple, kwargs []starlark.Tuple) map[string]any {
	out := map[string]any{}
	for i, a := range args {
		name := fmt.Sprintf("arg%d", i)
		if i < len(op.Params) {
			name = op.Params[i]
		}
		out[name] = argValue(a)
	}
	for _, kw := range kwargs {
		out[string(kw[0].(starlark.String))] = argValue(kw[1])
	}
	return out
}

func argValue(v starlark.Value) any {
	switch t := v.(type) {
	case starlark.String:
		s := string(t)
		if len(s) > maxArgLen {
			s = s[:maxArgLen] + "…"
		}
		return s
	case starlark.Int:
		if i, ok := t.Int64(); ok {
			return i
		}
		return t.String()
	case starlark.Float:
		return float64(t)
	case starlark.Bool:
		return bool(t)
	}
	if v == starlark.None {
		return nil
	}
	switch v.(type) {
	case *starlark.List, starlark.Tuple:
		iter := v.(starlark.Iterable).Iterate()
		defer iter.Done()
		var x starlark.Value
		var out []any
		for iter.Next(&x) {
			out = append(out, argValue(x))
		}
		return out
	case *starlark.Dict:
		out := map[string]any{}
		for _, item := range v.(*starlark.Dict).Items() {
			k, _ := item[0].(starlark.String)
			out[string(k)] = argValue(item[1])
		}
		return out
	}
	return "<" + v.Type() + ">"
}

func (f factory) CompletionOps(cfg any) []capability.OpInfo {
	v, ok := cfg.(validated)
	if !ok {
		return nil
	}
	var out []capability.OpInfo
	for alias, ac := range v {
		for _, op := range ac.mod.Manifest.Ops {
			out = append(out, capability.OpInfo{Name: types.Op(alias + "." + op.Name), Params: op.Params, Doc: op.Doc})
		}
	}
	return out
}
