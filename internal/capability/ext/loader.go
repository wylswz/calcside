package ext

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"go.starlark.net/syntax"
)

// Module is a loaded extension: manifest, source tree, and provenance.
type Module struct {
	ID       CapabilityIdentifier
	Root     string
	Manifest *CapabilityManifest
	Sources  map[string][]byte // slash-relative path -> source
	Commit   string            // resolved commit for remote sources
	Sum      string            // verified h1: sum
	files    map[string]*syntax.File
}

// CapabilityLoader resolves, fetches and validates extension sources.
type CapabilityLoader struct {
	Identifier CapabilityIdentifier
	Sum        string // h1: sum (required for remote, optional for local)
	Options    *Options
	locks      *fetchLocks // shared per factory; nil ok
}

var fileOpts = &syntax.FileOptions{
	Set:             true,
	While:           true,
	TopLevelControl: true,
	GlobalReassign:  true,
	Recursion:       true,
}

// Load resolves the identifier (local or remote), fetches the source,
// verifies its sum, parses the manifest, and compiles every .star file so
// syntax errors surface at Validate time.
func (l *CapabilityLoader) Load(ctx context.Context) (*Module, error) {
	p, err := ParseIdentifier(string(l.Identifier))
	if err != nil {
		return nil, err
	}
	var root, commit string
	if p.Domain != "" {
		if l.locks == nil {
			l.locks = &fetchLocks{}
		}
		root, commit, err = l.Options.fetchRemote(ctx, p, l.Sum, l.locks)
	} else if l.Options.LocalResolver != nil {
		root, err = l.Options.LocalResolver(ctx, p)
		if err == nil && l.Sum != "" {
			err = verifySum(root, l.Sum)
		}
	} else {
		root, err = l.Options.resolveLocal(p, l.Sum)
	}
	if err != nil {
		return nil, err
	}

	sources, err := readSources(root)
	if err != nil {
		return nil, fmt.Errorf("ext: reading %s: %w", l.Identifier, err)
	}
	manifest, err := parseManifest(sources[manifestFN])
	if err != nil {
		return nil, fmt.Errorf("ext %s: %w", l.Identifier, err)
	}
	manifest.Identifier = l.Identifier
	if _, ok := sources["main.star"]; !ok {
		return nil, fmt.Errorf("ext %s: missing main.star", l.Identifier)
	}
	mod := &Module{
		ID: l.Identifier, Root: root, Manifest: manifest,
		Sources: sources, Commit: commit, Sum: l.Sum,
		files: map[string]*syntax.File{},
	}
	for rel, src := range sources {
		if !strings.HasSuffix(rel, ".star") {
			continue
		}
		f, err := fileOpts.Parse(filepath.Join(root, rel), src, 0)
		if err != nil {
			return nil, fmt.Errorf("ext %s %s: %w", l.Identifier, rel, err)
		}
		mod.files[rel] = f
	}
	// Ops must resolve to top-level functions in main.star.
	defs := map[string]bool{}
	for _, st := range mod.files["main.star"].Stmts {
		if d, ok := st.(*syntax.DefStmt); ok {
			defs[d.Name.Name] = true
		}
	}
	for _, op := range manifest.Ops {
		if !defs[op.Name] {
			return nil, fmt.Errorf("ext %s: op %q is not a function in main.star", l.Identifier, op.Name)
		}
	}
	return mod, nil
}

// resolveLoad maps a load() module argument to a file in the extension
// tree: relative .star paths only, cleaned, no escape, no absolute.
func (m *Module) resolveLoad(name string) (string, error) {
	if filepath.IsAbs(name) {
		return "", fmt.Errorf("ext %s: load(%q): absolute paths not allowed", m.ID, name)
	}
	clean := filepath.ToSlash(filepath.Clean(name))
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("ext %s: load(%q): escapes extension root", m.ID, name)
	}
	if !strings.HasSuffix(clean, ".star") {
		return "", fmt.Errorf("ext %s: load(%q): only .star files can be loaded", m.ID, name)
	}
	if _, ok := m.files[clean]; !ok {
		return "", fmt.Errorf("ext %s: load(%q): no such file", m.ID, name)
	}
	return clean, nil
}
