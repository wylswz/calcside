// Package review defines the shared structured-diff vocabulary used by
// publish previews and history compares across resources (modules now,
// applications later). The diff is semantic: categories are compared
// from normalized content, never raw byte strings, and never carry
// plaintext credentials — refs and effective domains only.
package review

import (
	"encoding/json"
	"sort"
)

// Change categories.
const (
	CatSources      = "sources"      // program/module source files
	CatContract     = "contract"     // input/output schemas
	CatGrants       = "grants"       // capability grants (capabilities map)
	CatRules        = "rules"        // policies
	CatInputs       = "inputs"       // secrets/env/limits/ttl/environment bindings
	CatRequirements = "requirements" // module requires
)

// Change kinds.
const (
	Added    = "added"
	Removed  = "removed"
	Modified = "modified"
)

// Directions describe how the change moves capability exposure.
const (
	Widening  = "widening"
	Narrowing = "narrowing"
	Changed   = "changed"
)

// Artifact is one side of a review — the immutable snapshot being
// compared. Fields are optional per resource; only the populated
// facets participate in the diff. Digest is the content address (for
// modules, the exact tree sum).
type Artifact struct {
	Modules      map[string]ModuleSnapshot `json:"modules,omitempty"`
	Digest       string                    `json:"digest"`
	Version      int64                     `json:"version,omitempty"`
	DraftVersion int64                     `json:"draft_version,omitempty"`
	PublishedAt  string                    `json:"published_at,omitempty"`
	PublishedBy  string                    `json:"published_by,omitempty"`
	Note         string                    `json:"note,omitempty"`
	Files        map[string]string         `json:"files,omitempty"`
	Spec         json.RawMessage           `json:"spec,omitempty"`
	InputSchema  string                    `json:"input_schema,omitempty"`
	OutputSchema string                    `json:"output_schema,omitempty"`
	Policies     *PolicyPair               `json:"policies,omitempty"`
	Requirements json.RawMessage           `json:"requirements,omitempty"`
}

// ModuleSnapshot exposes reviewable source text, not base64-encoded byte slices.
type ModuleSnapshot struct {
	Name    string            `json:"name"`
	Version int64             `json:"version"`
	Sum     string            `json:"sum"`
	Files   map[string]string `json:"files"`
}

// PolicyPair mirrors runtime.PolicyBundle at the DTO layer.
type PolicyPair struct {
	Global map[string]string `json:"global,omitempty"`
	User   map[string]string `json:"user,omitempty"`
}

// Change is one semantic difference between base and candidate.
// Before/After carry raw JSON values (never plaintext secrets);
// ViaModules names the mounts through which the change arrives.
type Change struct {
	Category   string          `json:"category"`
	Path       string          `json:"path"`
	Kind       string          `json:"kind"`
	Direction  string          `json:"direction,omitempty"`
	Before     json.RawMessage `json:"before,omitempty"`
	After      json.RawMessage `json:"after,omitempty"`
	ViaModules []string        `json:"via_modules,omitempty"`
}

// Sort orders changes deterministically: widening first, then
// narrowing, then plain changes, then category/path/kind.
func Sort(changes []Change) {
	rank := func(d string) int {
		switch d {
		case Widening:
			return 0
		case Narrowing:
			return 1
		default:
			return 2
		}
	}
	sort.SliceStable(changes, func(i, j int) bool {
		if ri, rj := rank(changes[i].Direction), rank(changes[j].Direction); ri != rj {
			return ri < rj
		}
		if changes[i].Category != changes[j].Category {
			return changes[i].Category < changes[j].Category
		}
		if changes[i].Path != changes[j].Path {
			return changes[i].Path < changes[j].Path
		}
		return changes[i].Kind < changes[j].Kind
	})
}

// Result is the preview/compare response envelope.
type Result struct {
	Base      *Artifact `json:"base"`
	Candidate Artifact  `json:"candidate"`
	Changes   []Change  `json:"changes"`
	Identical bool      `json:"identical"`
}
