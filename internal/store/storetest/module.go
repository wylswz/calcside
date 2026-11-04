package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"calcside/internal/extartifact"
	"calcside/internal/store"
)

// RunModules exercises the module-draft/publish persistence contract
// (EP-0017): workspace isolation, name uniqueness, draft CAS, the
// reviewed-publish base sentinel, identical-digest no-op, the
// per-module version counter, defensive copies, and history deletion.
func seedWS(t *testing.T, s store.Store, ids ...string) {
	ctx := context.Background()
	for _, id := range ids {
		if err := s.CreateWorkspace(ctx, &store.Workspace{ID: id, Name: id}); err != nil && !errors.Is(err, store.ErrConflict) {
			t.Fatal(err)
		}
	}
}

func RunModules(t *testing.T, s store.Store) {
	t.Helper()
	ctx := context.Background()
	seedWS(t, s, "ws_a", "ws_b", "ws_dbg", "ws_1")
	u, err := s.UpsertUserByEmail(ctx, "mod@x.com", "Mod", "sub-mod")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()

	files := map[string][]byte{"u.star": []byte("x = 1")}
	sum, err := extartifact.TreeHash(files)
	if err != nil {
		t.Fatal(err)
	}
	version := func(vsum string, fs map[string][]byte) *store.ModuleVersion {
		return &store.ModuleVersion{Sum: vsum, Files: fs, PublishedBy: u.ID}
	}

	t.Run("create_get_list", func(t *testing.T) {
		m, err := store.NewModule("ws_1", u.ID, "strkit", "helpers", files, now)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.CreateModule(ctx, m); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetModule(ctx, m.ID)
		if err != nil || got.Name != "strkit" || got.DraftVersion != 1 {
			t.Fatalf("get: %v %+v", err, got)
		}
		byName, err := s.GetModuleByName(ctx, "ws_1", "strkit")
		if err != nil || byName.ID != m.ID {
			t.Fatalf("by name: %v %+v", err, byName)
		}
		lst, err := s.ListModules(ctx, store.ModuleFilter{WorkspaceID: "ws_1"})
		if err != nil || len(lst) != 1 || lst[0].ID != m.ID {
			t.Fatalf("list: %v %+v", err, lst)
		}
		if _, err := s.GetModule(ctx, "mod_missing"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("expected ErrNotFound, got %v", err)
		}
	})

	t.Run("workspace_isolation", func(t *testing.T) {
		a, _ := store.NewModule("ws_a", u.ID, "shared", "", files, now)
		b, _ := store.NewModule("ws_b", u.ID, "shared", "", files, now)
		if err := s.CreateModule(ctx, a); err != nil {
			t.Fatal(err)
		}
		// Same name in another workspace is allowed.
		if err := s.CreateModule(ctx, b); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetModuleByName(ctx, "ws_b", "shared")
		if err != nil || got.ID != b.ID {
			t.Fatalf("cross-workspace name lookup: %v %+v", err, got)
		}
		lst, _ := s.ListModules(ctx, store.ModuleFilter{WorkspaceID: "ws_a"})
		if len(lst) != 1 || lst[0].ID != a.ID {
			t.Fatalf("workspace leak: %+v", lst)
		}
		// Duplicate name in the same workspace conflicts.
		dup, _ := store.NewModule("ws_a", u.ID, "shared", "", files, now)
		if err := s.CreateModule(ctx, dup); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("expected ErrConflict, got %v", err)
		}
		// Deleting is workspace-scoped: ws_b cannot delete ws_a's module.
		if err := s.DeleteModule(ctx, "ws_b", a.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("expected ErrNotFound, got %v", err)
		}
	})

	t.Run("draft_cas", func(t *testing.T) {
		m, _ := store.NewModule("ws_1", u.ID, "cas", "", files, now)
		if err := s.CreateModule(ctx, m); err != nil {
			t.Fatal(err)
		}
		m.Description = "v2"
		ok, err := s.UpdateModuleDraft(ctx, m, 1)
		if err != nil || !ok {
			t.Fatalf("update: %v %v", err, ok)
		}
		got, _ := s.GetModule(ctx, m.ID)
		if got.DraftVersion != 2 || got.Description != "v2" {
			t.Fatalf("after update: %+v", got)
		}
		// Stale expected version performs no write.
		m.Description = "v3"
		ok, err = s.UpdateModuleDraft(ctx, m, 1)
		if err != nil || ok {
			t.Fatalf("stale write applied: %v %v", err, ok)
		}
		got, _ = s.GetModule(ctx, m.ID)
		if got.Description != "v2" {
			t.Fatalf("stale write landed: %+v", got)
		}
	})

	t.Run("publish_flow", func(t *testing.T) {
		m, _ := store.NewModule("ws_1", u.ID, "flow", "", files, now)
		if err := s.CreateModule(ctx, m); err != nil {
			t.Fatal(err)
		}
		// First publish allocates version 1 with no base.
		v1, err := s.PublishModule(ctx, "ws_1", m.ID, 1, "", version(sum, files))
		if err != nil || v1.Version != 1 || v1.BaseVersion != nil {
			t.Fatalf("first publish: %v %+v", err, v1)
		}
		// Identical digest is a no-op returning the existing version.
		again, err := s.PublishModule(ctx, "ws_1", m.ID, 1, sum, version(sum, files))
		if err != nil || again.Version != 1 {
			t.Fatalf("identical publish: %v %+v", err, again)
		}
		vers, _ := s.ListModuleVersions(ctx, m.ID)
		if len(vers) != 1 {
			t.Fatalf("no-op inserted a row: %+v", vers)
		}
		// Stale base fails.
		f2 := map[string][]byte{"u.star": []byte("x = 2")}
		sum2, _ := extartifact.TreeHash(f2)
		if _, err := s.PublishModule(ctx, "ws_1", m.ID, 1, "h1:wrong", version(sum2, f2)); !errors.Is(err, store.ErrModuleBaseConflict) {
			t.Fatalf("expected base conflict, got %v", err)
		}
		// Stale draft fails with the distinct sentinel.
		if _, err := s.PublishModule(ctx, "ws_1", m.ID, 99, sum, version(sum2, f2)); !errors.Is(err, store.ErrModuleDraftConflict) {
			t.Fatalf("expected draft conflict, got %v", err)
		}
		if errors.Is(store.ErrModuleDraftConflict, store.ErrModuleBaseConflict) {
			t.Fatal("sentinels must stay distinct")
		}
		// Second publish allocates version 2 chained on base 1.
		v2, err := s.PublishModule(ctx, "ws_1", m.ID, 1, sum, version(sum2, f2))
		if err != nil || v2.Version != 2 || v2.BaseVersion == nil || *v2.BaseVersion != 1 {
			t.Fatalf("second publish: %v %+v", err, v2)
		}
		got, err := s.GetModuleVersion(ctx, m.ID, 2)
		if err != nil || got.Sum != sum2 || got.PublishedBy != u.ID {
			t.Fatalf("get version: %v %+v", err, got)
		}
		vers, _ = s.ListModuleVersions(ctx, m.ID)
		if len(vers) != 2 || vers[0].Version != 1 || vers[1].Version != 2 {
			t.Fatalf("versions: %+v", vers)
		}
		// Versions are immutable snapshots: editing the draft later
		// does not move them (covered by the files blob being stored).
	})

	t.Run("counter_per_module", func(t *testing.T) {
		m1, _ := store.NewModule("ws_1", u.ID, "count-a", "", files, now)
		m2, _ := store.NewModule("ws_1", u.ID, "count-b", "", files, now)
		_ = s.CreateModule(ctx, m1)
		_ = s.CreateModule(ctx, m2)
		if _, err := s.PublishModule(ctx, "ws_1", m1.ID, 1, "", version(sum, files)); err != nil {
			t.Fatal(err)
		}
		v, err := s.PublishModule(ctx, "ws_1", m2.ID, 1, "", version(sum, files))
		if err != nil || v.Version != 1 {
			t.Fatalf("counter shared across modules: %v %+v", err, v)
		}
	})

	t.Run("defensive_copies", func(t *testing.T) {
		m, _ := store.NewModule("ws_1", u.ID, "copy", "", files, now)
		if err := s.CreateModule(ctx, m); err != nil {
			t.Fatal(err)
		}
		got, _ := s.GetModule(ctx, m.ID)
		got.Files["u.star"][0] = 'z'
		got.Files["injected.star"] = []byte("evil")
		back, _ := s.GetModule(ctx, m.ID)
		if string(back.Files["u.star"]) != "x = 1" || len(back.Files) != 1 {
			t.Fatalf("mutated read reached the store: %+v", back.Files)
		}
		v, err := s.PublishModule(ctx, "ws_1", m.ID, 1, "", version(sum, files))
		if err != nil {
			t.Fatal(err)
		}
		v.Files["u.star"][0] = 'z'
		vv, _ := s.GetModuleVersion(ctx, m.ID, 1)
		if string(vv.Files["u.star"]) != "x = 1" {
			t.Fatalf("version not defensive: %+v", vv.Files)
		}
	})

	t.Run("concurrent_publish_and_draft", func(t *testing.T) {
		m, _ := store.NewModule("ws_1", u.ID, "race", "", files, now)
		if err := s.CreateModule(ctx, m); err != nil {
			t.Fatal(err)
		}
		f2 := map[string][]byte{"u.star": []byte("x = 2")}
		sum2, _ := extartifact.TreeHash(f2)

		// Two publishes with different candidates race: exactly one may
		// insert version 1; the other must fail cleanly (base moved).
		type res struct {
			v   *store.ModuleVersion
			err error
		}
		ch := make(chan res, 2)
		go func() {
			v, err := s.PublishModule(ctx, "ws_1", m.ID, 1, "", version(sum, files))
			ch <- res{v, err}
		}()
		go func() {
			v, err := s.PublishModule(ctx, "ws_1", m.ID, 1, "", version(sum2, f2))
			ch <- res{v, err}
		}()
		a, b := <-ch, <-ch
		if a.err == nil && b.err == nil && a.v.Version != b.v.Version {
			t.Fatalf("both racers inserted: %+v %+v", a.v, b.v)
		}
		if a.err != nil && !errors.Is(a.err, store.ErrModuleBaseConflict) {
			t.Fatalf("unexpected publish error: %v", a.err)
		}
		if b.err != nil && !errors.Is(b.err, store.ErrModuleBaseConflict) {
			t.Fatalf("unexpected publish error: %v", b.err)
		}
		vers, err := s.ListModuleVersions(ctx, m.ID)
		if err != nil || len(vers) != 1 || vers[0].Version != 1 {
			t.Fatalf("racing publishes produced %+v (err %v)", vers, err)
		}

		// A draft update racing a publish must never corrupt either:
		// the draft CAS decides deterministically; no BUSY/DB errors.
		ch2 := make(chan error, 2)
		go func() {
			d, _ := s.GetModule(ctx, m.ID)
			d.Description = "raced"
			ok, err := s.UpdateModuleDraft(ctx, d, d.DraftVersion)
			if err != nil {
				ch2 <- err
			} else if !ok {
				ch2 <- nil // lost the race legitimately
			} else {
				ch2 <- nil
			}
		}()
		go func() {
			f3 := map[string][]byte{"u.star": []byte("x = 3")}
			sum3, _ := extartifact.TreeHash(f3)
			latest, _ := s.GetModuleVersion(ctx, m.ID, 1)
			_, err := s.PublishModule(ctx, "ws_1", m.ID, 1, latest.Sum, version(sum3, f3))
			if err != nil && !errors.Is(err, store.ErrModuleDraftConflict) && !errors.Is(err, store.ErrModuleBaseConflict) {
				ch2 <- err
			} else {
				ch2 <- nil
			}
		}()
		if err := <-ch2; err != nil {
			t.Fatalf("draft race error: %v", err)
		}
		if err := <-ch2; err != nil {
			t.Fatalf("publish race error: %v", err)
		}
	})

	t.Run("debug_settings", func(t *testing.T) {
		moduleDebugSuite(t, s)
	})
	t.Run("delete_drops_history", func(t *testing.T) {
		m, _ := store.NewModule("ws_1", u.ID, "gone", "", files, now)
		if err := s.CreateModule(ctx, m); err != nil {
			t.Fatal(err)
		}
		if _, err := s.PublishModule(ctx, "ws_1", m.ID, 1, "", version(sum, files)); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteModule(ctx, "ws_1", m.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.GetModule(ctx, m.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("expected ErrNotFound, got %v", err)
		}
		vers, err := s.ListModuleVersions(ctx, m.ID)
		if err != nil || len(vers) != 0 {
			t.Fatalf("history survived delete: %v %+v", err, vers)
		}
		// Name is free again in the same workspace.
		m2, _ := store.NewModule("ws_1", u.ID, "gone", "", files, now)
		if err := s.CreateModule(ctx, m2); err != nil {
			t.Fatalf("name not freed: %v", err)
		}
	})
}

// Debug settings: independent CAS, frozen snapshot, delete cleanup.
func moduleDebugSuite(t *testing.T, s store.Store) {
	ctx := context.Background()
	m, err := store.NewModule("ws_dbg", "u1", "dbg", "", map[string][]byte{"u.star": []byte("x = 1")}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateModule(ctx, m); err != nil {
		t.Fatal(err)
	}

	// Scaffold: absent row -> version 0.
	d, err := s.GetModuleDebugSettings(ctx, "ws_dbg", m.ID)
	if err != nil || d.Version != 0 || d.Script != "" {
		t.Fatalf("scaffold: %v %v", d, err)
	}

	// CAS 0 -> 1, stale 0 -> conflict.
	row := &store.ModuleDebugSettings{ModuleID: m.ID, WorkspaceID: "ws_dbg", Version: 1,
		Script: "print(x)", Spec: []byte(`{}`), UpdatedAt: time.Now()}
	if ok, err := s.PutModuleDebugSettings(ctx, row, 0); err != nil || !ok {
		t.Fatalf("put: %v %v", ok, err)
	}
	row.Version = 2
	if ok, err := s.PutModuleDebugSettings(ctx, row, 0); err != nil || ok {
		t.Fatalf("stale put accepted: %v %v", ok, err)
	}
	if ok, err := s.PutModuleDebugSettings(ctx, row, 1); err != nil || !ok {
		t.Fatalf("cas1: %v %v", ok, err)
	}

	// Snapshot: matching and stale expectations.
	m2, d2, err := s.GetModuleDebugSnapshot(ctx, "ws_dbg", m.ID, 1, 2)
	if err != nil || m2 == nil || d2 == nil || d2.Version != 2 {
		t.Fatalf("snapshot: %v", err)
	}
	if _, _, err := s.GetModuleDebugSnapshot(ctx, "ws_dbg", m.ID, 99, 2); !errors.Is(err, store.ErrModuleDraftConflict) {
		t.Fatalf("stale draft: %v", err)
	}
	if _, _, err := s.GetModuleDebugSnapshot(ctx, "ws_dbg", m.ID, 1, 99); !errors.Is(err, store.ErrModuleDebugConflict) {
		t.Fatalf("stale debug: %v", err)
	}

	// Validation: inline secret values and non-UTF8 scripts rejected.
	bad := &store.ModuleDebugSettings{ModuleID: m.ID, WorkspaceID: "ws_dbg", Version: 1,
		Script: "print(x)", Spec: []byte(`{"secrets":{"S":{"value":"shh"}}}`), UpdatedAt: time.Now()}
	if _, err := s.PutModuleDebugSettings(ctx, bad, 1); !errors.Is(err, store.ErrInvalidModule) {
		t.Fatalf("inline secret accepted: %v", err)
	}

	// Timestamp roundtrips (dialect time storage).
	gd, _ := s.GetModuleDebugSettings(ctx, "ws_dbg", m.ID)
	if gd.UpdatedAt.IsZero() {
		t.Fatal("updated_at not persisted")
	}

	// Defensive copy: mutating returned spec doesn't touch storage.
	d2.Spec[0] = '['
	d3, _ := s.GetModuleDebugSettings(ctx, "ws_dbg", m.ID)
	if string(d3.Spec) == "[}" {
		t.Fatal("spec not copied")
	}

	// Settings never touch draft_version.
	m3, _ := s.GetModule(ctx, m.ID)
	if m3.DraftVersion != 1 {
		t.Fatalf("settings changed draft_version to %d", m3.DraftVersion)
	}

	// Delete cleans the debug row.
	if err := s.DeleteModule(ctx, "ws_dbg", m.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetModuleDebugSettings(ctx, "ws_dbg", m.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("debug row survived delete: %v", err)
	}
}
