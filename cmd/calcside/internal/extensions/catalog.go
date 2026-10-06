package extensions

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"

	capext "calcside/internal/capability/ext"
)

type Catalog struct{ opts *capext.Options }

func NewCatalog(opts capext.Options) *Catalog { return &Catalog{opts: &opts} }

var _ capext.CatalogProvider = (*Catalog)(nil)

// Available scans each LocalRoots entry's immediate subdirectories for
// capability.yaml and loads them like Validate does; failures are
// logged and skipped.
func (f *Catalog) Available() capext.Catalog {
	c := capext.Catalog{
		Extensions:    []capext.Info{},
		RemoteEnabled: len(f.opts.AllowSources) > 0 && f.opts.CacheDir != "",
		LocalEnabled:  len(f.opts.LocalRoots) > 0,
	}
	seen := map[string]bool{}
	for _, root := range f.opts.LocalRoots {
		root, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		ents, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if !e.IsDir() {
				continue
			}
			dir := filepath.Join(root, e.Name())
			// Use the configured root name, not its host path, so
			// source matches what a spec must reference.
			source := filepath.Base(root) + "/" + e.Name()
			if _, err := os.Stat(filepath.Join(dir, "capability.yaml")); err != nil {
				continue
			}
			if seen[source] {
				continue
			}
			seen[source] = true
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			m, err := (&capext.CapabilityLoader{
				Identifier: capext.CapabilityIdentifier(source),
				Options:    f.opts,
			}).Load(ctx)
			cancel()
			if err != nil {
				slog.Warn("ext: skipping catalog entry", "dir", dir, "err", err)
				continue
			}
			c.Extensions = append(c.Extensions, capext.Info{
				Source:       source,
				Name:         m.Manifest.Name,
				Version:      m.Manifest.Version,
				Description:  m.Manifest.Description,
				Icon:         packagedIcon(m),
				Dependencies: m.Manifest.Dependencies,
				Ops:          m.Manifest.Ops,
				Config:       m.Manifest.Config,
			})
		}
	}
	sort.Slice(c.Extensions, func(i, j int) bool {
		a, b := c.Extensions[i], c.Extensions[j]
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Source < b.Source
	})
	return c
}
