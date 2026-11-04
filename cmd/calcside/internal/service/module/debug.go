package module

import (
	"context"
	"encoding/json"
	"sort"
	"time"
	"unicode/utf8"

	"calcside/cmd/calcside/internal/service"
	"calcside/cmd/calcside/internal/service/access"
	"calcside/cmd/calcside/internal/service/sandbox"
	"calcside/internal/extartifact"
	mod "calcside/internal/module"
	"calcside/internal/store"
	"calcside/internal/types"
)

// debug.go: per-module debug settings (editor state) plus scratch-run
// orchestration. The @self mount is always the module's CURRENT draft,
// pinned in memory — never persisted in settings and never visible to
// ordinary create. All ops are Configure-scoped: settings hold
// environment/vault references an editor may edit.

// DebugView is the GET/PUT response for debug settings.
type DebugView struct {
	Version   int64           `json:"version"`
	Script    string          `json:"script"`
	Spec      json.RawMessage `json:"spec"`
	SpecJSON  string          `json:"spec_json"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// SaveDebugRequest is the settings write.
type SaveDebugRequest struct {
	Version int64           `json:"version"`
	Script  string          `json:"script"`
	Spec    json.RawMessage `json:"spec"`
}

// RunDebugRequest identifies the frozen pair to run.
type RunDebugRequest struct {
	DraftVersion int64 `json:"draft_version"`
	DebugVersion int64 `json:"debug_version"`
}

// DebugRunView is a completed scratch-run.
type DebugRunView struct {
	Result       *sandbox.ExecView `json:"result"`
	InstanceID   string            `json:"instance_id"`
	DraftVersion int64             `json:"draft_version"`
	DebugVersion int64             `json:"debug_version"`
	ModuleSum    string            `json:"module_sum"`
}

func (s *Service) debugUnavailable() error {
	return service.Errf(types.ErrCodeInternal, "debug service unavailable")
}

func debugView(d *store.ModuleDebugSettings) *DebugView {
	spec := d.Spec
	if len(spec) == 0 {
		spec = json.RawMessage("{}")
	}
	return &DebugView{Version: d.Version, Script: d.Script,
		Spec: spec, SpecJSON: string(spec), UpdatedAt: d.UpdatedAt}
}

// defaultScript scaffolds the editor: a commented load of the first
// .star file (sorted) plus a self help call; empty trees get a bare
// comment.
func defaultScript(files map[string][]byte) string {
	var names []string
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	var first string
	for _, n := range names {
		if n != mod.ManifestFile {
			first = n
			break
		}
	}
	if first == "" {
		return "# Call exported helpers here.\nhelp(\"@self\")\n"
	}
	return "# load(\"@self/" + first + "\", \"helper\")\n# Call exported helpers here.\nhelp(\"@self\")\n"
}

// GetDebug returns the debug settings, scaffolding a default editor
// script when the row has never been written.
func (s *Service) GetDebug(ctx context.Context, a service.Actor, id string) (*DebugView, error) {
	m, err := s.o.Access.Module(ctx, a, id, access.Configure)
	if err != nil {
		return nil, err
	}
	if s.o.Sandbox == nil {
		return nil, s.debugUnavailable()
	}
	d, err := s.o.Store.GetModuleDebugSettings(ctx, m.WorkspaceID, m.ID)
	if err != nil {
		return nil, service.Internal(err)
	}
	if d.Version == 0 && d.Script == "" {
		d.Script = defaultScript(m.Files)
	}
	return debugView(d), nil
}

// SaveDebug CAS-writes the settings after descriptor-validating the
// spec boundary with the current draft injected as @self. The
// generated self mount is stripped before persisting.
func (s *Service) SaveDebug(ctx context.Context, a service.Actor, id string, r SaveDebugRequest) (*DebugView, error) {
	m, err := s.o.Access.Module(ctx, a, id, access.Configure)
	if err != nil {
		return nil, err
	}
	a.WorkspaceID = m.WorkspaceID
	if s.o.Sandbox == nil {
		return nil, s.debugUnavailable()
	}
	if r.Version < 0 {
		return nil, service.BadRequest("version must be >= 0")
	}
	if !utf8.ValidString(r.Script) || len(r.Script) > 256<<10 {
		return nil, service.BadRequest("script exceeds 256 KiB or is not UTF-8")
	}
	// Pin the current draft as the in-memory self tree — Version 0,
	// hash of the pinned candidate.
	pinned, err := s.pinCandidate(ctx, m.Files)
	if err != nil {
		return nil, err
	}
	sum, err := extartifact.TreeHash(pinned)
	if err != nil {
		return nil, service.Internal(err)
	}
	tree := mod.Tree{Name: m.Name, Version: 0, Sum: sum, Files: pinned}
	spec := r.Spec
	if len(spec) == 0 {
		spec = json.RawMessage("{}")
	}
	// Descriptor-only boundary: validates environment binding refs and
	// declared-secret requirements without opening the vault.
	b, err := s.o.Sandbox.DebugBoundary(ctx, a, spec, tree, false)
	if err != nil {
		return nil, err
	}
	// Strip the internal self mount from the normalized spec before
	// persisting — it is never stored user state.
	var normalized map[string]json.RawMessage
	_ = json.Unmarshal(b.Spec, &normalized)
	if mods, ok := normalized["modules"]; ok {
		var mm map[string]json.RawMessage
		_ = json.Unmarshal(mods, &mm)
		delete(mm, "self")
		if len(mm) == 0 {
			normalized["modules"] = json.RawMessage("null")
		} else {
			normalized["modules"], _ = json.Marshal(mm)
		}
	}
	stored, _ := json.Marshal(normalized)
	// Drop generated secrets/config placeholders so a re-edit can't
	// collide with the derived EXT_BIND_* entries (repeat-save bug).
	if stripped, err := sandbox.StripEnvironmentDerived(stored); err != nil {
		return nil, fail(err)
	} else {
		stored = stripped
	}
	row := &store.ModuleDebugSettings{
		ModuleID: m.ID, WorkspaceID: m.WorkspaceID, Version: r.Version + 1,
		Script: r.Script, Spec: stored, UpdatedAt: s.o.Now().UTC(),
	}
	ok, err := s.o.Store.PutModuleDebugSettings(ctx, row, r.Version)
	if err != nil {
		return nil, fail(err)
	}
	if !ok {
		return nil, fail(store.ErrModuleDebugConflict)
	}
	return debugView(row), nil
}

// RunDebug freezes (draft, settings) via the store snapshot, runs the
// script against a fresh scratch instance that mounts @self = the
// pinned draft, then ALWAYS deletes the instance — success, runtime
// error, or cancellation alike.
func (s *Service) RunDebug(ctx context.Context, a service.Actor, id string, r RunDebugRequest) (*DebugRunView, error) {
	m, err := s.o.Access.Module(ctx, a, id, access.Configure)
	if err != nil {
		return nil, err
	}
	a.WorkspaceID = m.WorkspaceID
	if s.o.Sandbox == nil {
		return nil, s.debugUnavailable()
	}
	if r.DraftVersion < 1 || r.DebugVersion < 1 {
		return nil, service.BadRequest("draft_version and debug_version required")
	}
	frozen, dbg, err := s.o.Store.GetModuleDebugSnapshot(ctx, m.WorkspaceID, m.ID, r.DraftVersion, r.DebugVersion)
	if err != nil {
		return nil, fail(err)
	}
	pinned, err := s.pinCandidate(ctx, frozen.Files)
	if err != nil {
		return nil, err
	}
	sum, err := extartifact.TreeHash(pinned)
	if err != nil {
		return nil, service.Internal(err)
	}
	tree := mod.Tree{Name: frozen.Name, Version: 0, Sum: sum, Files: pinned}
	spec := dbg.Spec
	if len(spec) == 0 {
		spec = json.RawMessage("{}")
	}
	in, err := s.o.Sandbox.CreateDebug(ctx, a, spec, tree)
	if err != nil {
		return nil, err
	}
	// Always tear down — independent context so cancellation of the
	// run cannot leak a live process.
	defer func() {
		cctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = s.o.Sandbox.Delete(cctx, a, in.ID)
	}()
	res, err := s.o.Sandbox.Exec(ctx, a, in.ID, dbg.Script, 30*time.Second)
	if err != nil {
		return nil, err
	}
	return &DebugRunView{
		Result: res, InstanceID: in.ID,
		DraftVersion: frozen.DraftVersion, DebugVersion: dbg.Version,
		ModuleSum: sum,
	}, nil
}
