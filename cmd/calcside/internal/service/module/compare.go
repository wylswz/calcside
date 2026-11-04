package module

import (
	"context"
	"strconv"
	"time"

	"calcside/cmd/calcside/internal/service"
	"calcside/cmd/calcside/internal/service/access"
	"calcside/cmd/calcside/internal/service/review"
	"calcside/internal/extartifact"
	"calcside/internal/store"
)

// compare.go: version history listing, cross-version/draft diff and
// restore-to-draft.

// Versions lists immutable published versions, newest first.
func (s *Service) Versions(ctx context.Context, a service.Actor, id string) ([]*VersionView, error) {
	m, err := s.o.Access.Module(ctx, a, id, access.View)
	if err != nil {
		return nil, err
	}
	vers, err := s.o.Store.ListModuleVersions(ctx, m.ID)
	if err != nil {
		return nil, service.Internal(err)
	}
	out := make([]*VersionView, 0, len(vers))
	for i := len(vers) - 1; i >= 0; i-- {
		out = append(out, toVersionView(vers[i]))
	}
	return out, nil
}

// Version returns one immutable version.
func (s *Service) Version(ctx context.Context, a service.Actor, id string, version int64) (*VersionView, error) {
	m, err := s.o.Access.Module(ctx, a, id, access.View)
	if err != nil {
		return nil, err
	}
	v, err := s.o.Store.GetModuleVersion(ctx, m.ID, version)
	if err != nil {
		return nil, fail(err)
	}
	return toVersionView(v), nil
}

// Compare diffs two versions — base=0 means "no base" — or a version
// against the current draft (head="draft").
func (s *Service) Compare(ctx context.Context, a service.Actor, id string, base int64, head string) (*Review, error) {
	if base < 0 {
		return nil, service.BadRequest("base must be >= 0")
	}
	m, err := s.o.Access.Module(ctx, a, id, access.View)
	if err != nil {
		return nil, err
	}
	var baseArt *review.Artifact
	if base > 0 {
		bv, err := s.o.Store.GetModuleVersion(ctx, m.ID, base)
		if err != nil {
			return nil, fail(err)
		}
		b := artifact(bv.Files, bv.Sum, bv.Version, 0, bv.PublishedBy, bv.Note, bv.CreatedAt.UTC().Format(time.RFC3339))
		baseArt = &b
	}
	var cand review.Artifact
	var identical bool
	if head == "draft" {
		pinned, err := s.pinCandidate(ctx, m.Files)
		if err != nil {
			return nil, err
		}
		sum, err := extartifact.TreeHash(pinned)
		if err != nil {
			return nil, service.Internal(err)
		}
		cand = artifact(pinned, sum, 0, m.DraftVersion, "", "", "")
	} else {
		hv, perr := strconv.ParseInt(head, 10, 64)
		if perr != nil || hv < 1 {
			return nil, service.BadRequest(`head must be a version number or "draft"`)
		}
		hv2, err := s.o.Store.GetModuleVersion(ctx, m.ID, hv)
		if err != nil {
			return nil, fail(err)
		}
		cand = artifact(hv2.Files, hv2.Sum, hv2.Version, 0, hv2.PublishedBy, hv2.Note, hv2.CreatedAt.UTC().Format(time.RFC3339))
	}
	var changes []review.Change
	if baseArt != nil {
		changes = diffArtifacts(*baseArt, cand)
		identical = baseArt.Digest == cand.Digest
	} else {
		changes = diffArtifacts(review.Artifact{}, cand)
	}
	return &Review{Base: baseArt, Candidate: cand, Changes: changes, Identical: identical}, nil
}

// Restore copies a published version's files into the draft under CAS
// — files only; the draft's name/description stay.
func (s *Service) Restore(ctx context.Context, a service.Actor, id string, version int64, draftVersion int64) (*View, error) {
	m, err := s.o.Access.Module(ctx, a, id, access.Configure)
	if err != nil {
		return nil, err
	}
	v, err := s.o.Store.GetModuleVersion(ctx, m.ID, version)
	if err != nil {
		return nil, fail(err)
	}
	next := m.Clone()
	next.Files = v.Files
	next.UpdatedAt = s.o.Now().UTC()
	ok, err := s.o.Store.UpdateModuleDraft(ctx, next, draftVersion)
	if err != nil {
		return nil, service.Internal(err)
	}
	if !ok {
		return nil, fail(store.ErrModuleDraftConflict)
	}
	next.DraftVersion = m.DraftVersion + 1
	latest, err := s.latest(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.toView(ctx, next, latest), nil
}
