package extensions

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	capext "calcside/internal/capability/ext"
	"calcside/internal/store"
	"calcside/internal/types"
)

// Catalog lists one entry per bound (family, version), built from the
// archived artifacts — never from the mutable source roots. Filesystem
// candidates are admitted through Repository.Admit; failures become
// diagnostics and never unlist the archived original.
type Catalog struct {
	repo *Repository
	mu   sync.Mutex
	// Built Info values are keyed by family+version+sum — immutable, so
	// repeated calls never re-read artifact bytes.
	cache map[string]*capext.Info
}

func NewCatalog(repo *Repository) *Catalog {
	return &Catalog{repo: repo, cache: map[string]*capext.Info{}}
}

var _ capext.CatalogProvider = (*Catalog)(nil)

// Available scans each local root's <ext>/<version>/ directories for
// candidates, admits them through the repository, then lists every
// permitted binding from the archived artifacts.
func (f *Catalog) Available(ctx context.Context) capext.Catalog {
	opts := f.repo.Options()
	c := capext.Catalog{
		Extensions:    []capext.Info{},
		Diagnostics:   []capext.CatalogDiagnostic{},
		RemoteEnabled: len(opts.AllowSources) > 0 && opts.CacheDir != "",
		LocalEnabled:  len(opts.LocalRoots) > 0,
	}
	diag := func(source, msg string) {
		slog.Warn("ext: catalog admission failed", "source", source, "err", msg)
		c.Diagnostics = append(c.Diagnostics, capext.CatalogDiagnostic{Source: source, Message: msg})
	}
	var rootPaths []string
	for _, root := range opts.LocalRoots {
		if abs, err := filepath.Abs(root); err == nil {
			rootPaths = append(rootPaths, abs)
		}
	}
	// Diagnostic messages carry the extension identifier and reason but
	// never host filesystem paths.
	scrub := func(msg string) string {
		for _, p := range rootPaths {
			msg = strings.ReplaceAll(msg, p, "$root")
		}
		return msg
	}
	for _, root := range opts.LocalRoots {
		abs, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		namespace := filepath.Base(abs)
		exts, err := os.ReadDir(abs)
		if err != nil {
			continue
		}
		for _, e := range exts {
			if !e.IsDir() {
				continue
			}
			family := namespace + "/" + e.Name()
			vers, err := os.ReadDir(filepath.Join(abs, e.Name()))
			if err != nil {
				continue
			}
			for _, v := range vers {
				if !v.IsDir() {
					continue
				}
				if _, err := os.Stat(filepath.Join(abs, e.Name(), v.Name(), "capability.yaml")); err != nil {
					continue
				}
				source := family + "@" + v.Name()
				if !capext.ValidVersion(v.Name()) {
					diag(source, "invalid version directory")
					continue
				}
				if err := f.repo.Admit(ctx, source); err != nil {
					diag(source, scrub(err.Error()))
				}
			}
		}
	}
	bindings, err := f.repo.store.ListExtensionVersions(ctx)
	if err != nil {
		slog.Warn("ext: listing version bindings", "err", err)
		return c
	}
	for _, b := range bindings {
		source := capext.Canonical(b.Family, b.Version)
		if err := f.repo.permitted(source); err != nil {
			continue
		}
		info, err := f.info(ctx, b, source)
		if err != nil {
			diag(source, "stored artifact unavailable")
			continue
		}
		c.Extensions = append(c.Extensions, *info)
	}
	sort.Slice(c.Extensions, func(i, j int) bool {
		a, b := c.Extensions[i], c.Extensions[j]
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if a.Family != b.Family {
			return a.Family < b.Family
		}
		return a.Version < b.Version
	})
	return c
}

// info builds (or reuses) the catalog entry for one binding from its
// archived artifact — manifest parse only, no Starlark execution.
func (f *Catalog) info(ctx context.Context, b store.ExtensionVersion, source string) (*capext.Info, error) {
	key := b.Family + "\x00" + b.Version + "\x00" + b.Sum
	f.mu.Lock()
	defer f.mu.Unlock()
	if info, ok := f.cache[key]; ok {
		return info, nil
	}
	artifact, err := f.repo.store.GetExtensionArtifact(ctx, b.ArtifactSource, b.Sum)
	if err != nil {
		return nil, err
	}
	m, err := capext.ParseManifest(artifact.Files["capability.yaml"])
	if err != nil {
		return nil, err
	}
	models := m.Models
	if models == nil {
		models = []string{}
	}
	deps := m.Dependencies
	if deps == nil {
		deps = []types.CapabilityName{}
	}
	ops := m.Ops
	if ops == nil {
		ops = []capext.OpSpec{}
	}
	cfg := m.Config
	if cfg == nil {
		cfg = []capext.ConfigField{}
	}
	info := &capext.Info{
		Source:       source,
		Family:       b.Family,
		Version:      b.Version,
		Sum:          b.Sum,
		Name:         m.Name,
		Description:  m.Description,
		Icon:         packagedIcon(m, artifact.Files),
		Interface:    m.Interface,
		Models:       models,
		Dependencies: deps,
		Ops:          ops,
		Config:       cfg,
	}
	f.cache[key] = info
	return info, nil
}
