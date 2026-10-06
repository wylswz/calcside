package ext

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"

	"calcside/internal/types"
)

// Info describes one loadable extension for the catalog endpoint.
type Info struct {
	Source       string                 `json:"source"`
	Name         string                 `json:"name"`
	Version      string                 `json:"version"`
	Description  string                 `json:"description"`
	IconURL      string                 `json:"icon_url,omitempty"`
	Icon         []byte                 `json:"-"`
	Dependencies []types.CapabilityName `json:"dependencies"`
	Ops          []OpSpec               `json:"ops"`
	Config       []ConfigField          `json:"config"`
}

// Catalog is the response of GET /api/v1/extensions.
type Catalog struct {
	Extensions    []Info `json:"extensions"`
	RemoteEnabled bool   `json:"remote_enabled"`
	LocalEnabled  bool   `json:"local_enabled"`
}

// Available scans each LocalRoots entry's immediate subdirectories for
// capability.yaml and loads them like Validate does; failures are
// logged and skipped.
func (f factory) Available() Catalog {
	c := Catalog{
		Extensions:    []Info{},
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
			if _, err := os.Stat(filepath.Join(dir, manifestFN)); err != nil {
				continue
			}
			if seen[source] {
				continue
			}
			seen[source] = true
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			m, err := (&CapabilityLoader{
				Identifier: CapabilityIdentifier(source),
				Options:    f.opts,
				locks:      f.locks,
			}).Load(ctx)
			cancel()
			if err != nil {
				slog.Warn("ext: skipping catalog entry", "dir", dir, "err", err)
				continue
			}
			c.Extensions = append(c.Extensions, Info{
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
