// Package module defines the workspace-module contract shared by the
// API tier, the runtime transport and the engine (EP-0017): immutable,
// content-pinned trees of .star files mounted under a consumer-chosen
// alias and reached through load("@alias/path.star").
//
// This is a leaf package: plain data plus tree/manifest validation only.
// It must not import the engine, the store, or the Starlark runtime;
// compiling and executing trees is the engine's job.
package module

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"

	"calcside/internal/extartifact"
	"calcside/internal/types"
)

// ManifestFile is the only non-.star file a tree may contain, at its root.
const ManifestFile = "module.yaml"

// Attachment bounds: the mounted trees ride the create/prepare
// request bodies, so the aggregate stays well under the transport
// ceilings (4 MiB create, 16 MiB prepare).
const (
	MaxMounts          = 32
	MaxAggregateBytes  = 1 << 20
	maxManifestBytes   = 64 << 10
	maxDescriptionRune = 300
)

var (
	// nameRe is the module-name grammar (shared with policy names).
	nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
	// MountRe is the mount-alias grammar.
	MountRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	// identRe is the ext-alias grammar (a Starlark identifier, ≤64).
	identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
)

// ValidSum reports whether s is a well-formed h1: tree hash — the
// prefix plus a canonical strict-base64 32-byte digest (padding bits
// rejected, matching ValidExtensionSum).
func ValidSum(s string) bool {
	if !strings.HasPrefix(s, "h1:") {
		return false
	}
	b, err := base64.StdEncoding.Strict().DecodeString(strings.TrimPrefix(s, "h1:"))
	return err == nil && len(b) == 32
}

// ValidName reports whether name may identify a module in a workspace
// library or in spec.modules[].name.
func ValidName(name string) bool { return nameRe.MatchString(name) }

// ValidMount reports whether alias may mount a module: it becomes the
// load() namespace @alias and must not collide with source paths.
func ValidMount(alias string) bool { return MountRe.MatchString(alias) }

// Reference is the spec.modules[mount] entry: which published module
// the mount resolves to. Version 0 means "latest published" — the API
// tier writes the concrete version and sum during resolution, so the
// stored spec is fully pinned.
type Reference struct {
	// Name defaults to the mount alias when empty.
	Name string `json:"name,omitempty"`
	// Version is the pinned module version; 0 = latest published.
	Version int64 `json:"version,omitempty"`
	// Sum is the pinned h1: tree hash, filled at resolution.
	Sum string `json:"sum,omitempty"`
	// Ext lists the extension aliases this module's manifest
	// contributed to the effective spec (provenance, recomputed
	// server-side — never trusted from input).
	Ext []string `json:"ext,omitempty"`
}

// Tree is one attached module's content-pinned snapshot traveling the
// API→worker contract inline, like PolicyBundle. Version 0 marks an
// unpublished draft snapshot (debug flows only).
type Tree struct {
	Name    string            `json:"name"`
	Version int64             `json:"version"`
	Sum     string            `json:"sum"`
	Files   map[string][]byte `json:"files"`
}

// ExtRequirement is one requires.ext manifest entry: the extension the
// module's code calls as ext.<alias>.*, pinned to a source and — at
// publish — an h1: sum. Modules never carry extension config or
// credentials; the consumer supplies them.
type ExtRequirement struct {
	Source string `yaml:"source" json:"source"`
	Sum    string `yaml:"sum,omitempty" json:"sum,omitempty"`
}

// Requires is the manifest's dependency section. Capabilities lists
// base capabilities the module's code needs — an informational
// declaration enforced at attach, never a grant.
type Requires struct {
	Capabilities []string                  `yaml:"capabilities,omitempty" json:"capabilities,omitempty"`
	Ext          map[string]ExtRequirement `yaml:"ext,omitempty" json:"ext,omitempty"`
}

// Manifest is a parsed module.yaml. Unknown fields are rejected.
type Manifest struct {
	Description string   `yaml:"description" json:"description,omitempty"`
	Requires    Requires `yaml:"requires" json:"requires"`
}

// ParseManifest decodes and validates module.yaml strictly.
func ParseManifest(data []byte) (*Manifest, error) {
	if len(data) > maxManifestBytes {
		return nil, fmt.Errorf("module: %s exceeds %d bytes", ManifestFile, maxManifestBytes)
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("module: %s: %w", ManifestFile, err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("module: %s must contain a single document", ManifestFile)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Validate enforces manifest content rules.
func (m *Manifest) Validate() error {
	if m == nil {
		return nil
	}
	if !utf8.ValidString(m.Description) || utf8.RuneCountInString(m.Description) > maxDescriptionRune {
		return errors.New("module: description exceeds 300 runes or is not UTF-8")
	}
	seen := map[string]bool{}
	for _, c := range m.Requires.Capabilities {
		n := types.CapabilityName(c)
		if !n.Valid() || n == types.CapExt {
			return fmt.Errorf("module: invalid base capability requirement %q", c)
		}
		if seen[c] {
			return fmt.Errorf("module: duplicate capability requirement %q", c)
		}
		seen[c] = true
	}
	for alias, req := range m.Requires.Ext {
		if !identRe.MatchString(alias) {
			return fmt.Errorf("module: invalid ext alias %q", alias)
		}
		if err := validateExtSource(req.Source); err != nil {
			return fmt.Errorf("module: ext %s: %w", alias, err)
		}
		if req.Sum != "" && !ValidSum(req.Sum) {
			return fmt.Errorf("module: ext %s: sum must be an h1: hash", alias)
		}
	}
	return nil
}

// validateExtSource checks an extension source identifier's portable
// shape without resolving it: the same lexical rules as local and
// remote identifiers share (no absolute paths, no traversal, no
// whitespace). Semantic resolution happens at publish/attach.
func validateExtSource(s string) error {
	if s == "" || len(s) > 512 {
		return errors.New("empty or oversized source")
	}
	if strings.HasPrefix(s, "/") || strings.ContainsAny(s, "\\ \t\r\n\x00:") {
		return fmt.Errorf("non-portable source %q", s)
	}
	for _, part := range strings.Split(s, "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("bad path element in source %q", s)
		}
	}
	return nil
}

// ValidateTree enforces every rule a stored or attached module tree
// must satisfy, independent of how it was produced:
//   - name and version shape;
//   - file paths portable (extartifact.ValidatePath) and bounded
//     (extartifact.Bounds);
//   - every file ends in .star except a single root module.yaml;
//   - no path element may start with '@' (the mount namespace);
//   - module.yaml, when present, parses strictly;
//   - a non-empty Sum must equal the tree's h1: hash (fail closed).
func ValidateTree(t *Tree) error {
	if t == nil {
		return errors.New("module: nil tree")
	}
	if !ValidName(t.Name) {
		return fmt.Errorf("module: invalid name %q", t.Name)
	}
	if t.Version < 0 {
		return fmt.Errorf("module: invalid version %d", t.Version)
	}
	if t.Sum == "" {
		return errors.New("module: runtime tree requires a pinned sum")
	}
	if err := ValidateFiles(t.Files, t.Sum); err != nil {
		return err
	}
	return nil
}

// ValidateFiles enforces the file-tree rules (paths, bounds, .star-only,
// manifest, UTF-8 sources) plus an optional sum pin: a non-empty Sum
// must be a well-formed h1: hash equal to the tree's digest. Sum "" is
// accepted for draft-write validation only — runtime attach paths go
// through ValidateTree, which always requires the pin.
func ValidateFiles(files map[string][]byte, sum string) error {
	if err := extartifact.Bounds(files); err != nil {
		return err
	}
	for name, data := range files {
		parts := strings.Split(name, "/")
		for _, p := range parts {
			if strings.HasPrefix(p, "@") {
				return fmt.Errorf("module: path %q may not contain @ elements", name)
			}
		}
		if name == ManifestFile {
			if err := validateManifestFile(data); err != nil {
				return err
			}
			continue
		}
		if !strings.HasSuffix(name, ".star") {
			return fmt.Errorf("module: file %q is not a .star source", name)
		}
		if !utf8.Valid(data) {
			return fmt.Errorf("module: file %q is not valid UTF-8", name)
		}
	}
	if sum != "" {
		if !ValidSum(sum) {
			return fmt.Errorf("module: sum %q is not an h1: hash", sum)
		}
		got, err := extartifact.TreeHash(files)
		if err != nil {
			return err
		}
		if got != sum {
			return fmt.Errorf("module: tree hash %s != pinned %s", got, sum)
		}
	}
	return nil
}

func validateManifestFile(data []byte) error {
	_, err := ParseManifest(data)
	return err
}

// ValidateMounts checks a full mount map (spec.modules resolved to
// trees): mount grammar, per-tree rules and the aggregate size bound
// that keeps attachments inside the transport ceilings.
func ValidateMounts(m map[string]Tree) error {
	if len(m) > MaxMounts {
		return fmt.Errorf("module: %d mounts exceeds %d", len(m), MaxMounts)
	}
	var total int64
	for alias, t := range m {
		if !ValidMount(alias) {
			return fmt.Errorf("module: invalid mount alias %q", alias)
		}
		tree := t
		if err := ValidateTree(&tree); err != nil {
			return fmt.Errorf("module: mount %s: %w", alias, err)
		}
		for _, b := range t.Files {
			total += int64(len(b))
		}
	}
	if total > MaxAggregateBytes {
		return fmt.Errorf("module: mounted trees %d bytes exceeds %d", total, MaxAggregateBytes)
	}
	return nil
}
