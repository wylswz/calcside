// Package ext implements the "ext" capability: extensions are Starlark
// modules (never native code) fetched from git remotes or local
// directories. Each extension declares the base capabilities it uses;
// its ops run inside the instance gate so policy, allowlists, secret
// injection/redaction, and audit apply unchanged.
package ext

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"go.starlark.net/starlark"

	"calcside/internal/capability"
	"calcside/internal/stdlib"
	"calcside/internal/types"
)

// boundModule pairs a module with its per-alias init context: the
// predeclared environment (base capability bindings + config + json/math)
// and a cache so each load()ed file executes once.
type boundModule struct {
	mod   *Module
	pre   starlark.StringDict
	cache map[string]starlark.StringDict
}

// init executes main.star (and its load()ed files) on a fresh thread
// with only the declared base capability bindings, a frozen `config`
// dict, and json/math predeclared. The gate is not armed during init, so
// any capability call at import time fails with ErrOutOfScope.
func (m *Module) init(ctx context.Context, bindings map[types.CapabilityName]starlark.Value, cfg map[string]any, maxSteps uint64) (starlark.StringDict, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	pre := stdlib.Modules()
	cfgDict, err := configDict(cfg)
	if err != nil {
		return nil, fmt.Errorf("ext %s: config: %w", m.ID, err)
	}
	pre["config"] = cfgDict
	for _, dep := range m.Manifest.Dependencies {
		b, ok := bindings[dep]
		if !ok {
			return nil, fmt.Errorf("ext %s: requires capability %q which this instance does not grant", m.Manifest.Name, dep)
		}
		pre[string(dep)] = b
	}
	b := &boundModule{mod: m, pre: pre, cache: map[string]starlark.StringDict{}}
	thread := &starlark.Thread{Name: "ext " + string(m.ID), Load: b.load}
	thread.SetLocal(capability.ContextKey, ctx)
	stop := context.AfterFunc(ctx, func() { thread.Cancel("extension initialization canceled") })
	defer stop()
	if maxSteps > 0 {
		thread.SetMaxExecutionSteps(maxSteps)
	}
	// ExecFileOptions freezes the returned globals.
	globals, err := starlark.ExecFileOptions(fileOpts, thread, filepath.Join(m.Root, "main.star"), m.Sources["main.star"], pre)
	if err != nil {
		return nil, fmt.Errorf("ext %s: %w", m.ID, err)
	}
	return globals, nil
}

// load resolves load() arguments within one module execution: only
// relative .star files inside this extension's source tree.
func (b *boundModule) load(thread *starlark.Thread, name string) (starlark.StringDict, error) {
	rel, err := b.mod.resolveLoad(name)
	if err != nil {
		return nil, err
	}
	if g, ok := b.cache[rel]; ok {
		return g, nil
	}
	g, err := starlark.ExecFileOptions(fileOpts, thread, filepath.Join(b.mod.Root, rel), b.mod.Sources[rel], b.pre)
	if err != nil {
		return nil, fmt.Errorf("ext %s: load(%q): %w", b.mod.ID, name, err)
	}
	b.cache[rel] = g
	return g, nil
}

// configDict converts validated JSON config values to a frozen starlark
// dict.
func configDict(cfg map[string]any) (starlark.Value, error) {
	d := starlark.NewDict(len(cfg))
	for k, v := range cfg {
		sv, err := toStarlark(v)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
		if err := d.SetKey(starlark.String(k), sv); err != nil {
			return nil, err
		}
	}
	d.Freeze()
	return d, nil
}

// toStarlark converts decoded JSON values to starlark values.
func toStarlark(v any) (starlark.Value, error) {
	switch t := v.(type) {
	case nil:
		return starlark.None, nil
	case bool:
		return starlark.Bool(t), nil
	case string:
		return starlark.String(t), nil
	case float64:
		return starlark.Float(t), nil
	case int:
		return starlark.MakeInt(t), nil
	case int64:
		return starlark.MakeInt64(t), nil
	case []any:
		l := starlark.NewList(make([]starlark.Value, len(t)))
		for i, e := range t {
			sv, err := toStarlark(e)
			if err != nil {
				return nil, err
			}
			l.SetIndex(i, sv)
		}
		return l, nil
	case map[string]any:
		d := starlark.NewDict(len(t))
		for k, e := range t {
			sv, err := toStarlark(e)
			if err != nil {
				return nil, err
			}
			if err := d.SetKey(starlark.String(k), sv); err != nil {
				return nil, err
			}
		}
		return d, nil
	}
	return nil, fmt.Errorf("unsupported config value type %T", v)
}
