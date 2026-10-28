package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	capext "calcside/internal/capability/ext"
	"calcside/internal/extartifact"
	"calcside/internal/store"
)

// Repository is the API-owned source boundary. Workers receive only
// immutable captured artifacts and never consult mutable source roots.
type Repository struct {
	store store.ExtensionArtifactStore
	opts  capext.Options
}

func NewRepository(st store.ExtensionArtifactStore, opts capext.Options) (*Repository, error) {
	if opts.CacheDir == "" || st == nil {
		return nil, errors.New("extensions: artifact store and cache directory required")
	}
	opts.SourceResolver = nil
	opts.LocalResolver = nil
	opts.LocalRoots = append([]string(nil), opts.LocalRoots...)
	opts.AllowSources = append([]string(nil), opts.AllowSources...)
	return &Repository{store: st, opts: opts}, nil
}

func (r *Repository) Options() capext.Options {
	opts := r.opts
	opts.SourceResolver = r.Resolve
	return opts
}

// Check policy even for a cache hit: revoking a source must not leave a
// public spec-validation bypass through previously captured artifacts.
func (r *Repository) permitted(source string) error {
	p, err := capext.ParseIdentifier(source)
	if err != nil {
		return errors.New("extensions: invalid source")
	}
	if p.Domain != "" {
		name := p.Domain + "/" + p.Group + "/" + p.Name
		for _, prefix := range r.opts.AllowSources {
			prefix = strings.TrimSuffix(prefix, "/")
			if prefix != "" && (name == prefix || strings.HasPrefix(name, prefix+"/")) {
				return nil
			}
		}
		return errors.New("extensions: remote source not permitted")
	}
	namespace, _, _ := strings.Cut(p.Local, "/")
	roots := map[string]bool{}
	for _, root := range r.opts.LocalRoots {
		absolute, err := filepath.Abs(root)
		if err == nil && filepath.Base(absolute) == namespace {
			roots[absolute] = true
		}
	}
	if len(roots) != 1 {
		return errors.New("extensions: local source namespace unavailable or ambiguous")
	}
	return nil
}

func (r *Repository) Capture(ctx context.Context, source, sum string) (*store.ExtensionArtifact, error) {
	if err := r.permitted(source); err != nil {
		return nil, err
	}
	if sum != "" {
		existing, err := r.store.GetExtensionArtifact(ctx, source, sum)
		if err == nil {
			return existing, nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
	}
	module, err := (&capext.CapabilityLoader{
		Identifier: capext.CapabilityIdentifier(source), Sum: sum, Options: &r.opts,
	}).Load(ctx)
	if err != nil {
		return nil, err
	}
	artifact := &store.ExtensionArtifact{Source: source, Sum: module.Sum,
		Files: module.Sources, CreatedAt: time.Now().UTC()}
	if err := r.store.PutExtensionArtifact(ctx, artifact); err != nil {
		return nil, err
	}
	return artifact, nil
}

func (r *Repository) Resolve(ctx context.Context, source, sum string) (string, error) {
	artifact, err := r.Capture(ctx, source, sum)
	if err != nil {
		return "", err
	}
	return extartifact.Install(filepath.Join(r.opts.CacheDir, "artifacts"),
		source, artifact.Sum, artifact.Files)
}

// PinSpec changes only extension sums. All other JSON stays in raw
// messages, including provider configs, policies [] and large integers.
func (r *Repository) PinSpec(ctx context.Context, raw []byte) (json.RawMessage, error) {
	var spec, caps map[string]json.RawMessage
	if err := json.Unmarshal(raw, &spec); err != nil || spec == nil {
		return nil, errors.New("extensions: invalid runtime spec")
	}
	if err := json.Unmarshal(spec["capabilities"], &caps); err != nil || caps == nil {
		if spec["capabilities"] == nil || string(spec["capabilities"]) == "null" {
			return append(json.RawMessage(nil), raw...), nil
		}
		return nil, errors.New("extensions: invalid capabilities")
	}
	ext := caps["ext"]
	if ext == nil || string(ext) == "null" {
		return append(json.RawMessage(nil), raw...), nil
	}
	var aliases map[string]json.RawMessage
	if err := json.Unmarshal(ext, &aliases); err != nil {
		return nil, errors.New("extensions: invalid aliases")
	}
	for alias, value := range aliases {
		var config map[string]json.RawMessage
		if err := json.Unmarshal(value, &config); err != nil || config == nil {
			return nil, errors.New("extensions: invalid alias configuration")
		}
		if err := r.pin(ctx, config); err != nil {
			return nil, err
		}
		if providers, exists := config["llm"]; exists {
			var registry []map[string]json.RawMessage
			if err := json.Unmarshal(providers, &registry); err != nil {
				return nil, errors.New("extensions: invalid provider registry")
			}
			for _, provider := range registry {
				if err := r.pin(ctx, provider); err != nil {
					return nil, err
				}
			}
			config["llm"], _ = json.Marshal(registry)
		}
		aliases[alias], _ = json.Marshal(config)
	}
	caps["ext"], _ = json.Marshal(aliases)
	spec["capabilities"], _ = json.Marshal(caps)
	return json.Marshal(spec)
}

func (r *Repository) pin(ctx context.Context, config map[string]json.RawMessage) error {
	var source, sum string
	if json.Unmarshal(config["source"], &source) != nil || source == "" {
		return errors.New("extensions: source required")
	}
	if supplied, ok := config["sum"]; ok {
		if err := json.Unmarshal(supplied, &sum); err != nil {
			return errors.New("extensions: invalid integrity sum")
		}
	}
	artifact, err := r.Capture(ctx, source, sum)
	if err != nil {
		return fmt.Errorf("extensions: cannot capture source: %w", err)
	}
	config["sum"], _ = json.Marshal(artifact.Sum)
	return nil
}
