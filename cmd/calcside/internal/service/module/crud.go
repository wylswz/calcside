package module

import (
	"context"
	"strings"

	"go.yaml.in/yaml/v3"

	"calcside/cmd/calcside/internal/service"
	"calcside/cmd/calcside/internal/service/access"
	mod "calcside/internal/module"
	"calcside/internal/store"
	"calcside/internal/types"
)

// crud.go: draft lifecycle — create, read, list, update (draft CAS),
// delete. All reads are View-scoped, all writes Configure-scoped, via
// the centralized access checker.

// Create stores a new draft after validating the source tree.
func (s *Service) Create(ctx context.Context, a service.Actor, r CreateRequest) (*View, error) {
	if err := s.o.Access.ModuleScope(ctx, a, access.Configure); err != nil {
		return nil, err
	}
	files := utf8Files(r.Files)
	if err := s.checkSources(files); err != nil {
		return nil, err
	}
	m, err := store.NewModule(a.WorkspaceID, a.UserID, r.Name, r.Description, files, s.o.Now())
	if err != nil {
		return nil, fail(err)
	}
	if err := s.o.Store.CreateModule(ctx, m); err != nil {
		return nil, fail(err)
	}
	return s.toView(ctx, m, nil), nil
}

// Get returns the draft view with latest-version metadata.
func (s *Service) Get(ctx context.Context, a service.Actor, id string) (*View, error) {
	m, err := s.o.Access.Module(ctx, a, id, access.View)
	if err != nil {
		return nil, err
	}
	latest, err := s.latest(ctx, m.ID)
	if err != nil {
		return nil, err
	}
	return s.toView(ctx, m, latest), nil
}

func (s *Service) latest(ctx context.Context, id string) (*store.ModuleVersion, error) {
	vers, err := s.o.Store.ListModuleVersions(ctx, id)
	if err != nil {
		return nil, service.Internal(err)
	}
	if len(vers) == 0 {
		return nil, nil
	}
	return vers[len(vers)-1], nil
}

// List returns workspace-visible drafts.
func (s *Service) List(ctx context.Context, a service.Actor) ([]*View, error) {
	f, err := s.o.Access.Modules(ctx, a)
	if err != nil {
		return nil, err
	}
	rows, err := s.o.Store.ListModules(ctx, f)
	if err != nil {
		return nil, service.Internal(err)
	}
	out := make([]*View, 0, len(rows))
	for _, m := range rows {
		latest, err := s.latest(ctx, m.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, s.toView(ctx, m, latest))
	}
	return out, nil
}

// Update applies a draft CAS patch — name/description/files — after
// validating the resulting source tree.
func (s *Service) Update(ctx context.Context, a service.Actor, id string, r UpdateRequest) (*View, error) {
	m, err := s.o.Access.Module(ctx, a, id, access.Configure)
	if err != nil {
		return nil, err
	}
	next := m.Clone()
	if r.Name != nil {
		next.Name = strings.TrimSpace(*r.Name)
	}
	if r.Description != nil {
		next.Description = *r.Description
	}
	if r.Files != nil {
		next.Files = utf8Files(r.Files)
	}
	// Shape checks only — autosave must accept incomplete .star drafts.
	if err := s.checkShape(next.Files); err != nil {
		return nil, err
	}
	if r.Requirements != nil {
		files, err := s.applyRequirements(next.Files, m.Files[mod.ManifestFile], *r.Requirements)
		if err != nil {
			return nil, err
		}
		next.Files = files
		// Pin structured deps before store so autosaved requirements
		// carry h1 sums.
		pinned, err := s.pinDependencies(ctx, next.Files)
		if err != nil {
			return nil, err
		}
		next.Files = pinned
	}
	next.UpdatedAt = s.o.Now().UTC()
	if err := store.ValidateModule(next); err != nil {
		return nil, fail(err)
	}
	ok, err := s.o.Store.UpdateModuleDraft(ctx, next, r.DraftVersion)
	if err != nil {
		return nil, fail(err)
	}
	if !ok {
		return nil, fail(store.ErrModuleDraftConflict)
	}
	next.DraftVersion = m.DraftVersion + 1 // the store incremented it
	latest, err := s.latest(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.toView(ctx, next, latest), nil
}

// Delete removes the draft and all versions (snapshots embedded in
// releases/instances are unaffected).
func (s *Service) Delete(ctx context.Context, a service.Actor, id string) error {
	m, err := s.o.Access.Module(ctx, a, id, access.Configure)
	if err != nil {
		return err
	}
	return fail(s.o.Store.DeleteModule(ctx, m.WorkspaceID, m.ID))
}

// applyRequirements rewrites module.yaml's requires block from the
// structured edit. Values are strictly {source,sum} — no config or
// environment is allowed through. When the patch lacks module.yaml,
// prior manifest metadata (description/capabilities) is preserved.
func (s *Service) applyRequirements(files map[string][]byte, prior []byte, req RequirementsView) (map[string][]byte, error) {
	mf := &mod.Manifest{}
	cur := files[mod.ManifestFile]
	if cur == nil {
		cur = prior // patch omitted module.yaml — keep prior metadata
	}
	if cur != nil {
		parsed, err := mod.ParseManifest(cur)
		if err != nil {
			return nil, service.Errf(types.ErrCodeBadSpec, "%s", err)
		}
		mf = parsed
	}
	for a, r := range req.Ext {
		if r.Source == "" || (r.Sum != "" && !mod.ValidSum(r.Sum)) {
			return nil, service.Errf(types.ErrCodeBadSpec, "requirements ext %q: source+sum only, valid h1: sum required", a)
		}
	}
	mf.Requires.Capabilities = req.Capabilities
	mf.Requires.Ext = req.Ext
	if err := mf.Validate(); err != nil {
		return nil, service.Errf(types.ErrCodeBadSpec, "%s", err)
	}
	out, err := yaml.Marshal(mf)
	if err != nil {
		return nil, fail(err)
	}
	next := map[string][]byte{}
	for k, v := range files {
		next[k] = v
	}
	next[mod.ManifestFile] = out
	return next, nil
}
