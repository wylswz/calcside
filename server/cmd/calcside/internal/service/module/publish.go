package module

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"time"
	"unicode/utf8"

	"calcside/cmd/calcside/internal/service"
	"calcside/cmd/calcside/internal/service/access"
	"calcside/cmd/calcside/internal/service/review"
	"calcside/internal/extartifact"
	mod "calcside/internal/module"
	"calcside/internal/store"
	"calcside/internal/types"

	"go.yaml.in/yaml/v3"
)

// publish.go: candidate pinning, review diff, preview and the atomic
// publish path. Preview and publish never touch the vault — extension
// dependencies are source+sum only and pinning needs no credentials.

// pinCandidate resolves missing/changed ext pins in module.yaml via
// the artifact repository: a source-only dep gets its sum pinned; an
// already-pinned dep that matches stays untouched (bytes preserved so
// comments/format do not drift). Returns the files to publish.
func (s *Service) pinCandidate(ctx context.Context, files map[string][]byte) (map[string][]byte, error) {
	if err := s.checkSources(files); err != nil {
		return nil, err
	}
	return s.pinDependencies(ctx, files)
}

// pinDependencies resolves manifest ext pins only — no compile — so
// debug saves work against incomplete sources.
func (s *Service) pinDependencies(ctx context.Context, files map[string][]byte) (map[string][]byte, error) {
	if err := s.checkShape(files); err != nil {
		return nil, err
	}
	out, err := s.pinDeps(ctx, files)
	if err != nil {
		return nil, err
	}
	if err := s.checkShape(out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) pinDeps(ctx context.Context, files map[string][]byte) (map[string][]byte, error) {
	data, ok := files[mod.ManifestFile]
	if !ok {
		return files, nil
	}
	mf, err := mod.ParseManifest(data)
	if err != nil {
		return nil, service.Errf(types.ErrCodeBadSpec, "%s", err)
	}
	if len(mf.Requires.Ext) == 0 {
		return files, nil
	}
	if s.o.SpecPinner == nil {
		return nil, service.Errf(types.ErrCodeBadSpec,
			"manifest declares ext dependencies but artifact resolution is unavailable")
	}
	// Minimal spec carrying just the ext deps — pinning them returns
	// the resolved sums without touching anything else.
	minimal, _ := json.Marshal(map[string]any{"capabilities": map[string]any{"ext": mf.Requires.Ext}})
	pinned, err := s.o.SpecPinner.PinSpec(ctx, minimal)
	if err != nil {
		return nil, fail(err)
	}
	var out struct {
		Capabilities struct {
			Ext map[string]mod.ExtRequirement `json:"ext"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(pinned, &out); err != nil {
		return nil, service.Internal(err)
	}
	changed := false
	for alias, req := range mf.Requires.Ext {
		p, ok := out.Capabilities.Ext[alias]
		if !ok || p.Source == "" || p.Source != req.Source || !mod.ValidSum(p.Sum) {
			return nil, service.Errf(types.ErrCodeBadSpec,
				"manifest ext %q could not be pinned to a canonical sum", alias)
		}
		if req.Sum != "" && p.Sum != req.Sum {
			return nil, service.Errf(types.ErrCodeBadSpec,
				"manifest ext %q pins %q but source resolves %q", alias, req.Sum, p.Sum)
		}
		if p.Sum != req.Sum {
			changed = true
		}
	}
	if !changed {
		return files, nil // bytes preserved: nothing to pin
	}
	// Ext pins changed: re-emit the manifest (whole-document rewrite;
	// formatting may normalize) then re-validate the final candidate —
	// the rewritten manifest must still satisfy tree bounds and compile.
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, service.Errf(types.ErrCodeBadSpec, "manifest: %s", err)
	}
	reqs, _ := doc["requires"].(map[string]any)
	exts, _ := reqs["ext"].(map[string]any)
	for alias, p := range out.Capabilities.Ext {
		e, _ := exts[alias].(map[string]any)
		if e == nil {
			e = map[string]any{}
			exts[alias] = e
		}
		e["source"] = p.Source
		e["sum"] = p.Sum
	}
	nb, err := yaml.Marshal(doc)
	if err != nil {
		return nil, service.Internal(err)
	}
	next := maps.Clone(files)
	next[mod.ManifestFile] = nb
	if err := s.checkShape(next); err != nil {
		return nil, err
	}
	return next, nil
}

// artifact builds the review Artifact for a file set + metadata.
func artifact(files map[string][]byte, digest string, ver, draft int64, by, note, at string) review.Artifact {
	a := review.Artifact{
		Digest: digest, Version: ver, DraftVersion: draft,
		PublishedBy: by, Note: note, PublishedAt: at,
		Files: strFiles(files),
	}
	if r := requirements(files); r != nil {
		a.Requirements, _ = json.Marshal(r)
	}
	return a
}

// diffFiles produces source-category changes between two file maps.
func diffFiles(base, cand map[string][]byte) []review.Change {
	var changes []review.Change
	for name := range base {
		if _, ok := cand[name]; !ok {
			b, _ := json.Marshal(string(base[name]))
			changes = append(changes, review.Change{Category: review.CatSources, Path: name, Kind: review.Removed, Before: b})
		}
	}
	for name := range cand {
		cb := cand[name]
		if bb, ok := base[name]; !ok {
			a, _ := json.Marshal(string(cb))
			changes = append(changes, review.Change{Category: review.CatSources, Path: name, Kind: review.Added, After: a})
		} else if !slices.Equal(bb, cb) {
			b, _ := json.Marshal(string(bb))
			a, _ := json.Marshal(string(cb))
			changes = append(changes, review.Change{Category: review.CatSources, Path: name, Kind: review.Modified, Before: b, After: a})
		}
	}
	return changes
}

// diffRequirements produces requirements-category changes from two
// manifests' requirements — capability set and ext pins.
func diffRequirements(base, cand *RequirementsView) []review.Change {
	var changes []review.Change
	bc := map[string]bool{}
	if base != nil {
		for _, c := range base.Capabilities {
			bc[c] = true
		}
	}
	cc := map[string]bool{}
	if cand != nil {
		for _, c := range cand.Capabilities {
			cc[c] = true
		}
	}
	for _, c := range slices.Sorted(maps.Keys(cc)) {
		if !bc[c] {
			v, _ := json.Marshal(c)
			changes = append(changes, review.Change{Category: review.CatRequirements, Path: "capabilities/" + c, Kind: review.Added, Direction: review.Widening, After: v})
		}
	}
	for _, c := range slices.Sorted(maps.Keys(bc)) {
		if !cc[c] {
			v, _ := json.Marshal(c)
			changes = append(changes, review.Change{Category: review.CatRequirements, Path: "capabilities/" + c, Kind: review.Removed, Direction: review.Narrowing, Before: v})
		}
	}
	be, ce := map[string]mod.ExtRequirement{}, map[string]mod.ExtRequirement{}
	if base != nil {
		be = base.Ext
	}
	if cand != nil {
		ce = cand.Ext
	}
	for _, alias := range slices.Sorted(maps.Keys(ce)) {
		cur, ok := be[alias]
		v, _ := json.Marshal(ce[alias])
		if !ok {
			changes = append(changes, review.Change{Category: review.CatRequirements, Path: "ext/" + alias, Kind: review.Added, Direction: review.Widening, After: v})
		} else if cur != ce[alias] {
			b, _ := json.Marshal(cur)
			changes = append(changes, review.Change{Category: review.CatRequirements, Path: "ext/" + alias, Kind: review.Modified, Direction: review.Changed, Before: b, After: v})
		}
	}
	for _, alias := range slices.Sorted(maps.Keys(be)) {
		if _, ok := ce[alias]; !ok {
			v, _ := json.Marshal(be[alias])
			changes = append(changes, review.Change{Category: review.CatRequirements, Path: "ext/" + alias, Kind: review.Removed, Direction: review.Narrowing, Before: v})
		}
	}
	return changes
}

func diffArtifacts(base, cand review.Artifact) []review.Change {
	changes := diffFiles(strToBytes(base.Files), strToBytes(cand.Files))
	var br, cr *RequirementsView
	if base.Requirements != nil {
		var r RequirementsView
		_ = json.Unmarshal(base.Requirements, &r)
		br = &r
	}
	if cand.Requirements != nil {
		var r RequirementsView
		_ = json.Unmarshal(cand.Requirements, &r)
		cr = &r
	}
	changes = append(changes, diffRequirements(br, cr)...)
	review.Sort(changes)
	return changes
}

func strToBytes(in map[string]string) map[string][]byte {
	out := make(map[string][]byte, len(in))
	for k, v := range in {
		out[k] = []byte(v)
	}
	return out
}

// Preview computes the publish review for a draft: base = latest
// published version (or null), candidate = the current draft with
// manifest ext pins resolved. Configure-scoped per EP-0018 — preview
// is the publish front door, not a read. No vault or credentials.
func (s *Service) Preview(ctx context.Context, a service.Actor, id string, draftVersion int64) (*Review, error) {
	m, err := s.o.Access.Module(ctx, a, id, access.Configure)
	if err != nil {
		return nil, err
	}
	if draftVersion < 1 {
		return nil, service.BadRequest("draft_version required")
	}
	if draftVersion != m.DraftVersion {
		return nil, fail(store.ErrModuleDraftConflict)
	}
	pinned, err := s.pinCandidate(ctx, m.Files)
	if err != nil {
		return nil, err
	}
	sum, err := extartifact.TreeHash(pinned)
	if err != nil {
		return nil, service.Internal(err)
	}
	cand := artifact(pinned, sum, 0, m.DraftVersion, "", "", "")
	var baseArt *review.Artifact
	latest, err := s.latest(ctx, id)
	if err != nil {
		return nil, err
	}
	var changes []review.Change
	if latest != nil {
		b := artifact(latest.Files, latest.Sum, latest.Version, 0,
			latest.PublishedBy, latest.Note, latest.CreatedAt.UTC().Format(time.RFC3339))
		baseArt = &b
		changes = diffArtifacts(b, cand)
	} else {
		changes = diffArtifacts(review.Artifact{}, cand)
	}
	return &Review{Base: baseArt, Candidate: cand, Changes: changes,
		Identical: latest != nil && latest.Sum == sum}, nil
}

// Publish creates an immutable version from the draft. The caller
// supplies the draft_version it reviewed, the expected base sum
// ("" = no prior version, presence required), and the candidate
// digest preview returned — the service re-pins, compares digests,
// and publishes under both CAS checks.
func (s *Service) Publish(ctx context.Context, a service.Actor, id string, r PublishRequest) (*VersionView, error) {
	m, err := s.o.Access.Module(ctx, a, id, access.Configure)
	if err != nil {
		return nil, err
	}
	if r.DraftVersion < 1 {
		return nil, service.BadRequest("draft_version required")
	}
	if r.ExpectedBase == nil {
		return nil, service.BadRequest(`expected_base required (use "" for no prior version)`)
	}
	if r.Candidate == "" || !mod.ValidSum(r.Candidate) {
		return nil, service.BadRequest("candidate must be the preview digest (h1: sum)")
	}
	if !utf8.ValidString(r.Note) || utf8.RuneCountInString(r.Note) > 500 {
		return nil, service.BadRequest("note exceeds 500 runes or is not UTF-8")
	}
	// Stale draft is reported deterministically before any pin work.
	if m.DraftVersion != r.DraftVersion {
		return nil, fail(store.ErrModuleDraftConflict)
	}
	pinned, err := s.pinCandidate(ctx, m.Files)
	if err != nil {
		return nil, err
	}
	sum, err := extartifact.TreeHash(pinned)
	if err != nil {
		return nil, service.Internal(err)
	}
	// The digest the caller reviewed must equal the rebuilt one — a
	// draft changed between preview and publish.
	if r.Candidate != sum {
		return nil, service.Conflict("candidate changed — re-run preview")
	}
	mv, err := s.o.Store.PublishModule(ctx, m.WorkspaceID, id, r.DraftVersion, *r.ExpectedBase,
		&store.ModuleVersion{Sum: sum, Files: pinned, PublishedBy: a.UserID, Note: r.Note})
	if err != nil {
		return nil, fail(err)
	}
	return toVersionView(mv), nil
}
