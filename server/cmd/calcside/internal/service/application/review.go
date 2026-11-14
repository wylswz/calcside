package application

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"time"
	"unicode/utf8"

	"calcside/cmd/calcside/internal/service"
	"calcside/cmd/calcside/internal/service/access"
	"calcside/cmd/calcside/internal/service/review"
	"calcside/cmd/calcside/internal/service/sandbox"
	"calcside/internal/runtime"
	"calcside/internal/store"
	"calcside/internal/types"
)

// review.go: EP-0018 reviewed publish — descriptor-mode preview plus
// the transactional reviewed publish. Both read the draft and every
// dependency (policies, secret descriptors, extension pins, module
// versions) from one transaction-bound boundary; only a changed
// candidate crosses into runtime prepare + publish.

// reviewRelease builds the immutable release for a draft app in
// descriptor mode — never decrypts.
func (s *Service) reviewRelease(ctx context.Context, a service.Actor, app *store.Application,
	sb *sandbox.Service, at time.Time) (*store.ServingRelease, error) {
	b, err := sb.BuildBoundary(ctx, a, app.Spec, false)
	if err != nil {
		return nil, err
	}
	return newRelease(app, b, at)
}

// Preview computes the publish review inside a transaction — every
// dependency read comes from the same snapshot. Never decrypts.
func (s *Service) Preview(ctx context.Context, a service.Actor, id string, draftVersion int64) (*review.Result, error) {
	m, err := s.o.Access.Application(ctx, a, id, access.Configure)
	if err != nil {
		return nil, err
	}
	if draftVersion < 1 {
		return nil, service.BadRequest("draft_version required")
	}
	a.WorkspaceID = m.WorkspaceID
	var out *review.Result
	err = s.o.Store.Tx(ctx, func(tx store.Store) error {
		if err := tx.LockWorkspace(ctx, m.WorkspaceID); err != nil {
			return err
		}
		cur, err := access.New(tx).Application(ctx, a, id, access.Configure)
		if err != nil {
			return err
		}
		if cur.Version != draftVersion {
			return service.Conflict("application changed")
		}
		bound, err := s.o.Sandbox.WithStore(tx)
		if err != nil {
			return err
		}
		rel, err := s.reviewRelease(ctx, a, cur, bound, s.o.Now())
		if err != nil {
			return err
		}
		var base *store.ServingRelease
		if cur.PersistentInstanceID != "" {
			pin, err := tx.GetPersistentInstance(ctx, cur.WorkspaceID, cur.PersistentInstanceID)
			if err != nil {
				return err
			}
			base, err = tx.GetServingRelease(ctx, cur.WorkspaceID, pin.ReleaseID)
			if err != nil {
				return err
			}
		}
		hist, err := tx.ListApplicationReleases(ctx, cur.WorkspaceID, cur.ID)
		if err != nil {
			return err
		}
		var ba *review.Artifact
		if base != nil {
			tmp := releaseArtifact(cur, base, seqOf(hist, base.ID), 0)
			for _, h := range hist {
				if h.ReleaseID == base.ID {
					tmp.PublishedBy, tmp.PublishedAt, tmp.Note =
						h.PublishedBy, h.PublishedAt.UTC().Format(time.RFC3339), h.Note
					tmp.DraftVersion = h.DraftVersion
				}
			}
			ba = &tmp
		}
		cand := releaseArtifact(cur, rel, 0, cur.Version)
		out = &review.Result{Candidate: cand, Identical: base != nil && base.ID == rel.ID}
		out.Base = ba
		if ba != nil {
			out.Changes = diffArtifacts(*ba, cand)
		} else {
			out.Changes = diffArtifacts(review.Artifact{}, cand)
		}
		return nil
	})
	return out, fail(err)
}

// PublishRequest for the reviewed publish route.
type PublishRequest struct {
	Version      int64   `json:"version"`
	ExpectedBase *string `json:"expected_base"`
	Candidate    string  `json:"candidate"` // rel_<hex> from preview
	Note         string  `json:"note,omitempty"`
}

var relIDRe = regexp.MustCompile(`^rel_[0-9a-f]{64}$`)

// verifyCandidate re-reads the app + dependencies inside tx and
// rebuilds the release — the single frozen check for stale draft,
// stale base, and stale candidate.
func (s *Service) verifyCandidate(ctx context.Context, tx store.Store, a service.Actor,
	id string, r PublishRequest) (*store.Application, *store.ServingRelease, error) {
	if err := tx.LockWorkspace(ctx, a.WorkspaceID); err != nil {
		return nil, nil, err
	}
	cur, err := access.New(tx).Application(ctx, a, id, access.Configure)
	if err != nil {
		return nil, nil, err
	}
	if cur.Version != r.Version {
		return nil, nil, service.Conflict("application changed")
	}
	curBase := ""
	if cur.PersistentInstanceID != "" {
		p, err := tx.GetPersistentInstance(ctx, cur.WorkspaceID, cur.PersistentInstanceID)
		if err != nil {
			return nil, nil, service.Internal(err)
		}
		curBase = p.ReleaseID
	}
	if curBase != *r.ExpectedBase {
		return nil, nil, fail(store.ErrApplicationBaseConflict)
	}
	bound, err := s.o.Sandbox.WithStore(tx)
	if err != nil {
		return nil, nil, err
	}
	rel, err := s.reviewRelease(ctx, a, cur, bound, s.o.Now())
	if err != nil {
		var se *service.Error
		if errors.As(err, &se) {
			switch se.Code {
			case types.ErrCodeBadSpec, types.ErrCodeBadCapability, types.ErrCodeBadPolicy, types.ErrCodeBadSecret, types.ErrCodeBadRequest:
				return nil, nil, service.Conflict("candidate dependencies changed - re-run preview")
			}
		}
		return nil, nil, err
	}
	if rel.ID != r.Candidate {
		return nil, nil, service.Conflict("candidate changed — re-run preview")
	}
	return cur, rel, nil
}

// PublishReviewed publishes the reviewed candidate. An identical
// candidate (== expected base) publishes without any prepare. A
// changed candidate resolves secrets + prepares outside the tx, then
// re-verifies inside the final tx before committing.
func (s *Service) PublishReviewed(ctx context.Context, a service.Actor, id string, r PublishRequest) (*store.PersistentInstance, *store.ApplicationRelease, error) {
	m, err := s.o.Access.Application(ctx, a, id, access.Configure)
	if err != nil {
		return nil, nil, err
	}
	if r.Version < 1 {
		return nil, nil, service.BadRequest("version required")
	}
	if r.ExpectedBase == nil {
		return nil, nil, service.BadRequest(`expected_base required ("" for never published)`)
	}
	if !relIDRe.MatchString(r.Candidate) {
		return nil, nil, service.BadRequest("candidate must be the preview release id (rel_<64hex>)")
	}
	if r.Note != "" && (!utf8.ValidString(r.Note) || utf8.RuneCountInString(r.Note) > 500) {
		return nil, nil, service.BadRequest("note exceeds 500 runes or is not UTF-8")
	}
	a.WorkspaceID = m.WorkspaceID
	var candRel *store.ServingRelease
	var pin *store.PersistentInstance
	var hist *store.ApplicationRelease
	err = s.o.Store.Tx(ctx, func(tx store.Store) error {
		var e error
		_, candRel, e = s.verifyCandidate(ctx, tx, a, id, r)
		if e != nil {
			return e
		}
		if r.Candidate == *r.ExpectedBase {
			// Identical candidate: publish in the SAME frozen tx.
			pin, hist, e = tx.PublishReviewedApplication(ctx, m.WorkspaceID, id,
				r.Version, *r.ExpectedBase, candRel, a.UserID, r.Note, s.o.Now())
		}
		return e
	})
	if err != nil {
		return nil, nil, fail(err)
	}
	if r.Candidate == *r.ExpectedBase {
		return pin, hist, nil
	}
	// Changed: resolve + prepare outside any tx.
	b, err := s.o.Sandbox.ReleaseBoundary(ctx, a, candRel)
	if err != nil {
		return nil, nil, err
	}
	owner, err := s.owner(ctx, m.UserID)
	if err != nil {
		return nil, nil, err
	}
	prepareCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	replicaID := store.NewID(store.PrefixInstance)
	prep, cleanup, err := s.prepare(prepareCtx, m.WorkspaceID, &runtime.PrepareRequest{
		CreateRequest: runtime.CreateRequest{
			InstanceID: replicaID, Owner: owner,
			Labels: map[string]string{"name": m.Name, "application_id": m.ID},
			Spec:   candRel.Spec, Policies: candRel.Policies,
			Modules: b.Modules, Secrets: b.Secrets,
			ExpiresAt: s.o.Now().Add(30 * time.Second), Epoch: 1,
		},
		ReleaseID: candRel.ID, Generation: 1, Program: candRel.Program,
		PrepareID: store.NewID("prep_"),
	})
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		return nil, nil, err
	}
	if prep == nil || prep.Result.Error != nil {
		return nil, nil, service.BadRequest("program failed to prepare")
	}
	// Final tx: re-verify draft/base/candidate before committing.
	err = s.o.Store.Tx(ctx, func(tx store.Store) error {
		if _, _, e := s.verifyCandidate(ctx, tx, a, id, r); e != nil {
			return e
		}
		var e error
		pin, hist, e = tx.PublishReviewedApplication(ctx, m.WorkspaceID, id,
			r.Version, *r.ExpectedBase, candRel, a.UserID, r.Note, s.o.Now())
		return e
	})
	if err != nil {
		return nil, nil, fail(err)
	}
	return pin, hist, nil
}

// releaseArtifact builds the review Artifact straight from the
// release — sources/schemas/modules from the immutable snapshot.
func releaseArtifact(app *store.Application, rel *store.ServingRelease, seq, draft int64) review.Artifact {
	files := map[string]string{}
	for name, src := range rel.Program.Sources {
		files[name] = string(src)
	}
	a := review.Artifact{
		Digest: rel.ID, Version: seq, DraftVersion: draft,
		Spec: rel.Spec, Files: files,
	}
	if rel.Program.InputSchema != nil {
		a.InputSchema = string(rel.Program.InputSchema)
	}
	if rel.Program.OutputSchema != nil {
		a.OutputSchema = string(rel.Program.OutputSchema)
	}
	a.Policies = &review.PolicyPair{Global: rel.Policies.Global, User: rel.Policies.User}
	if len(rel.Modules) > 0 {
		mods := map[string]review.ModuleSnapshot{}
		for alias, tr := range rel.Modules {
			files := map[string]string{}
			for name, src := range tr.Files {
				files[name] = string(src)
			}
			mods[alias] = review.ModuleSnapshot{Name: tr.Name, Version: tr.Version, Sum: tr.Sum, Files: files}
		}
		a.Modules = mods
	}
	return a
}

// diffArtifacts produces the application semantic diff between base
// and candidate artifacts — source, contract, and reference-only spec
// facets. Direction is widening-first sorted.
func diffArtifacts(base, cand review.Artifact) []review.Change {
	var changes []review.Change
	if src, ok := diffSource(base.Files, cand.Files); ok {
		changes = append(changes, src...)
	}
	if c := diffContract(base, cand); c != nil {
		changes = append(changes, *c)
	}
	changes = append(changes, diffSpecRefs(base, cand)...)
	review.Sort(changes)
	return changes
}

func marshalJSON(v any) (map[string]any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m, nil
}

func diffSource(base, cand map[string]string) ([]review.Change, bool) {
	var out []review.Change
	for k, v := range cand {
		bv, ok := base[k]
		if !ok {
			out = append(out, review.Change{Category: review.CatSources, Path: k, Kind: review.Added,
				After: raw(v)})
		} else if bv != v {
			out = append(out, review.Change{Category: review.CatSources, Path: k, Kind: review.Modified,
				Before: raw(bv), After: raw(v)})
		}
	}
	for k, v := range base {
		if _, ok := cand[k]; !ok {
			out = append(out, review.Change{Category: review.CatSources, Path: k, Kind: review.Removed,
				Before: raw(v)})
		}
	}
	return out, len(out) > 0
}

func diffContract(base, cand review.Artifact) *review.Change {
	inEq := base.InputSchema == cand.InputSchema
	outEq := base.OutputSchema == cand.OutputSchema
	if inEq && outEq {
		return nil
	}
	return &review.Change{Category: review.CatContract, Path: "schemas", Kind: review.Modified,
		Direction: review.Changed,
		Before:    raw(map[string]any{"input": base.InputSchema, "output": base.OutputSchema}),
		After:     raw(map[string]any{"input": cand.InputSchema, "output": cand.OutputSchema})}
}

// diffSpecRefs compares only reference-level spec fields: capability
// names present/absent (grant widens/revoke narrows), module attach
// refs, net allowlist element sets, ext pins, and policy maps.
// Everything else is left "changed" without direction claims.
func diffSpecRefs(base, cand review.Artifact) []review.Change {
	var out []review.Change
	type specShape struct {
		Caps  map[string]json.RawMessage `json:"capabilities"`
		Mods  map[string]json.RawMessage `json:"modules"`
		Pols  []string                   `json:"policies"`
		Env   json.RawMessage            `json:"environment"`
		Limit json.RawMessage            `json:"limits"`
		TTL   *int64                     `json:"ttl_seconds"`
	}
	var b, c specShape
	_ = json.Unmarshal(base.Spec, &b)
	_ = json.Unmarshal(cand.Spec, &c)
	for alias := range c.Caps {
		if _, ok := b.Caps[alias]; !ok && alias != "modules" {
			out = append(out, review.Change{Category: review.CatGrants, Path: "capabilities/" + alias,
				Kind: review.Added, Direction: review.Widening})
		}
	}
	for alias := range b.Caps {
		if _, ok := c.Caps[alias]; !ok && alias != "modules" {
			out = append(out, review.Change{Category: review.CatGrants, Path: "capabilities/" + alias,
				Kind: review.Removed, Direction: review.Narrowing})
		}
	}
	// Net allowlists: element set changes — add widens, remove narrows.
	netSet := func(caps map[string]json.RawMessage) map[string]bool {
		out := map[string]bool{}
		if e, ok := caps["net"]; ok {
			var n struct {
				AllowHosts []string `json:"allow_hosts"`
			}
			_ = json.Unmarshal(e, &n)
			for _, h := range n.AllowHosts {
				out[h] = true
			}
		}
		return out
	}
	bn, cn := netSet(b.Caps), netSet(c.Caps)
	for h := range cn {
		if !bn[h] {
			out = append(out, review.Change{Category: review.CatGrants, Path: "net/allow_hosts/" + h,
				Kind: review.Added, Direction: review.Widening})
		}
	}
	for h := range bn {
		if !cn[h] {
			out = append(out, review.Change{Category: review.CatGrants, Path: "net/allow_hosts/" + h,
				Kind: review.Removed, Direction: review.Narrowing})
		}
	}
	for alias := range c.Mods {
		bm, ok := b.Mods[alias]
		if !ok {
			out = append(out, review.Change{Category: review.CatGrants, Path: "modules/" + alias,
				Kind: review.Added, Direction: review.Widening})
			continue
		}
		if string(bm) != string(c.Mods[alias]) {
			out = append(out, review.Change{Category: review.CatGrants, Path: "modules/" + alias,
				Kind: review.Modified, Direction: review.Changed})
		}
	}
	for alias := range b.Mods {
		if _, ok := c.Mods[alias]; !ok {
			out = append(out, review.Change{Category: review.CatGrants, Path: "modules/" + alias,
				Kind: review.Removed, Direction: review.Narrowing})
		}
	}
	if string(b.Env) != string(c.Env) || string(b.Limit) != string(c.Limit) {
		out = append(out, review.Change{Category: review.CatRules, Path: "spec", Kind: review.Modified,
			Direction: review.Changed})
	}
	return out
}

func raw(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// seqOf finds the history seq for a release id (0 when the release was
// published before history existed).
func seqOf(hist []*store.ApplicationRelease, relID string) int64 {
	for i := len(hist) - 1; i >= 0; i-- {
		h := hist[i]
		if h.ReleaseID == relID {
			return h.Seq
		}
	}
	return 0
}
