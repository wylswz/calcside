package module

import (
	"context"
	"strings"

	"calcside/cmd/calcside/internal/service"
	"calcside/cmd/calcside/internal/service/access"
	"calcside/internal/store"
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
	if err := s.checkSources(next.Files); err != nil {
		return nil, err
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
