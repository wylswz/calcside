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
	"calcside/internal/types"
)

// ErrVersionConflict wraps store.ErrConflict: the (family, version)
// pair is already bound to different content and can never be remapped.
var ErrVersionConflict = fmt.Errorf("extensions: version already bound to different content: %w", store.ErrConflict)

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
// The check works on the parsed family — the version suffix plays no
// role in allowlist membership.
func (r *Repository) permitted(source string) error {
	p, err := capext.ParseIdentifier(source)
	if err != nil {
		return errors.New("extensions: invalid source")
	}
	if p.Domain != "" {
		for _, prefix := range r.opts.AllowSources {
			prefix = strings.TrimSuffix(prefix, "/")
			if prefix != "" && (p.Family == prefix || strings.HasPrefix(p.Family, prefix+"/")) {
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

var errExplicitVersion = errors.New("extensions: local sources require an explicit @version")

func (r *Repository) Capture(ctx context.Context, source, sum string) (*store.ExtensionArtifact, error) {
	if err := r.permitted(source); err != nil {
		return nil, err
	}
	p, err := capext.ParseIdentifier(source)
	if err != nil {
		return nil, errors.New("extensions: invalid source")
	}
	if p.Domain == "" && p.Version == "" {
		// Legacy unversioned local source: only an exact pinned
		// artifact lookup — never the filesystem.
		if sum == "" {
			return nil, errExplicitVersion
		}
		artifact, err := r.store.GetExtensionArtifact(ctx, source, sum)
		if errors.Is(err, store.ErrNotFound) {
			return nil, errExplicitVersion
		}
		return artifact, err
	}

	binding, err := r.store.GetExtensionVersion(ctx, p.Family, p.Version)
	if err == nil {
		if sum != "" && sum != binding.Sum {
			return nil, ErrVersionConflict
		}
		return r.store.GetExtensionArtifact(ctx, binding.ArtifactSource, binding.Sum)
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}

	if sum != "" {
		// A pre-binding artifact captured under this exact source
		// (e.g. a remote pin) adopts the version binding in place.
		if artifact, err := r.store.GetExtensionArtifact(ctx, source, sum); err == nil {
			if err := r.bind(ctx, p.Family, p.Version, sum, source, nil); err != nil {
				return nil, err
			}
			return artifact, nil
		} else if !errors.Is(err, store.ErrNotFound) {
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
	if err := r.bind(ctx, p.Family, p.Version, module.Sum, source, artifact); err != nil {
		return nil, err
	}
	return artifact, nil
}

// bind stores artifact + binding in one store transaction and maps a
// lost race or a different-sum conflict to ErrVersionConflict.
func (r *Repository) bind(ctx context.Context, family, version, sum, artifactSource string, artifact *store.ExtensionArtifact) error {
	err := r.store.BindExtensionVersion(ctx, &store.ExtensionVersion{
		Family: family, Version: version, Sum: sum,
		ArtifactSource: artifactSource, CreatedAt: time.Now().UTC(),
	}, artifact)
	if errors.Is(err, store.ErrConflict) {
		return ErrVersionConflict
	}
	return err
}

// Admit captures a filesystem candidate (versioned local sources only)
// into the catalog: the candidate is always loaded from disk; an
// existing binding with a different sum is ErrVersionConflict.
func (r *Repository) Admit(ctx context.Context, source string) error {
	if err := r.permitted(source); err != nil {
		return err
	}
	p, err := capext.ParseIdentifier(source)
	if err != nil {
		return errors.New("extensions: invalid source")
	}
	if p.Domain != "" || p.Version == "" {
		return errors.New("extensions: admission requires a versioned local source")
	}
	module, err := (&capext.CapabilityLoader{
		Identifier: capext.CapabilityIdentifier(source), Options: &r.opts,
	}).Load(ctx)
	if err != nil {
		return err
	}
	binding, err := r.store.GetExtensionVersion(ctx, p.Family, p.Version)
	if err == nil {
		if binding.Sum == module.Sum {
			return nil
		}
		return fmt.Errorf("extensions: %w: content changed under an already-bound version; publish a new version directory", ErrVersionConflict)
	}
	if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	artifact := &store.ExtensionArtifact{Source: source, Sum: module.Sum,
		Files: module.Sources, CreatedAt: time.Now().UTC()}
	return r.bind(ctx, p.Family, p.Version, module.Sum, source, artifact)
}

// Pin resolves one (source, sum) pair to its canonical form: a
// versioned source is captured and returned unchanged; a legacy
// unversioned local pair is either migrated via its stored artifact's
// manifest version or resolved against the sole filesystem candidate.
func (r *Repository) Pin(ctx context.Context, source, sum string) (canonicalSource, pinnedSum string, err error) {
	if err := r.permitted(source); err != nil {
		return "", "", err
	}
	p, err := capext.ParseIdentifier(source)
	if err != nil {
		return "", "", errors.New("extensions: invalid source")
	}
	if p.Domain != "" || p.Version != "" {
		artifact, err := r.Capture(ctx, source, sum)
		if err != nil {
			return "", "", err
		}
		return source, artifact.Sum, nil
	}
	if sum != "" {
		if artifact, err := r.store.GetExtensionArtifact(ctx, source, sum); err == nil {
			m, err := capext.ParseManifest(artifact.Files["capability.yaml"])
			if err == nil && capext.ValidVersion(m.Version) {
				err := r.store.BindExtensionVersion(ctx, &store.ExtensionVersion{
					Family: p.Family, Version: m.Version, Sum: sum,
					ArtifactSource: source, CreatedAt: time.Now().UTC(),
				}, nil)
				switch {
				case err == nil:
					return capext.Canonical(p.Family, m.Version), sum, nil
				case errors.Is(err, store.ErrConflict):
					// An operator-chosen or already-bound different
					// sum claims this version: keep the legacy pair.
					return source, sum, nil
				default:
					return "", "", err
				}
			}
			return source, sum, nil
		} else if !errors.Is(err, store.ErrNotFound) {
			return "", "", err
		}
	}
	versions, err := r.opts.LocalVersions(p.Family)
	if err != nil {
		return "", "", err
	}
	if len(versions) != 1 {
		return "", "", fmt.Errorf("extensions: %s has %d version candidates; select an explicit version", source, len(versions))
	}
	canonical := capext.Canonical(p.Family, versions[0])
	artifact, err := r.Capture(ctx, canonical, sum)
	if err != nil {
		return "", "", err
	}
	return canonical, artifact.Sum, nil
}

// Manifest returns the validated capability.yaml metadata for a
// pinned (source,sum), re-checking the source allowlists through
// Capture — a revoked source cannot be inspected either. It reads and
// validates the manifest only: nothing is initialized, executed or
// materialized to disk, and the returned struct is a copy.
func (r *Repository) Manifest(ctx context.Context, source, sum string) (*capext.CapabilityManifest, error) {
	artifact, err := r.Capture(ctx, source, sum)
	if err != nil {
		return nil, err
	}
	data, ok := artifact.Files["capability.yaml"]
	if !ok {
		return nil, errors.New("extensions: source has no capability.yaml")
	}
	m, err := capext.ParseManifest(data)
	if err != nil {
		return nil, err
	}
	out := *m
	out.Ops = append([]capext.OpSpec(nil), m.Ops...)
	out.Config = append([]capext.ConfigField(nil), m.Config...)
	out.Dependencies = append([]types.CapabilityName(nil), m.Dependencies...)
	out.Models = append([]string(nil), m.Models...)
	return &out, nil
}

func (r *Repository) Resolve(ctx context.Context, source, sum string) (string, error) {
	artifact, err := r.Capture(ctx, source, sum)
	if err != nil {
		return "", err
	}
	return extartifact.Install(filepath.Join(r.opts.CacheDir, "artifacts"),
		source, artifact.Sum, artifact.Files)
}

// PinSpec changes only extension sources and sums. All other JSON
// stays in raw messages, including provider configs, policies [] and
// large integers.
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
	canonical, pinned, err := r.Pin(ctx, source, sum)
	if err != nil {
		return fmt.Errorf("extensions: cannot capture source: %w", err)
	}
	config["source"], _ = json.Marshal(canonical)
	config["sum"], _ = json.Marshal(pinned)
	return nil
}

// WithStore returns a clone bound to a different artifact store —
// same options, same policy — so transaction-rebound boundaries never
// escape to the root store. Options() on the clone binds the clone's
// Resolve as SourceResolver.
func (r *Repository) WithStore(st store.ExtensionArtifactStore) *Repository {
	return &Repository{store: st, opts: r.opts}
}
