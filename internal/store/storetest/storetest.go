// Package storetest is a conformance suite every store.Store
// implementation must pass.
package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"calcside/internal/store"
	"calcside/internal/types"
)

// Run exercises the whole Store contract against a fresh implementation.
func Run(t *testing.T, newStore func(t *testing.T) store.Store) {
	t.Helper()
	ctx := context.Background()

	t.Run("users", func(t *testing.T) {
		s := newStore(t)
		u, err := s.UpsertUserByEmail(ctx, "a@x.com", "A", "sub1")
		if err != nil {
			t.Fatal(err)
		}
		if u.ID == "" || u.Email != "a@x.com" || u.IsAdmin {
			t.Fatalf("bad user %+v", u)
		}
		u2, err := s.UpsertUserByEmail(ctx, "a@x.com", "A2", "")
		if err != nil {
			t.Fatal(err)
		}
		if u2.ID != u.ID {
			t.Fatalf("upsert created new user: %s vs %s", u2.ID, u.ID)
		}
		got, err := s.GetUser(ctx, u.ID)
		if err != nil || got.Email != "a@x.com" {
			t.Fatalf("GetUser: %v %+v", err, got)
		}
		if _, err := s.GetUser(ctx, "usr_missing"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("expected ErrNotFound, got %v", err)
		}
	})

	t.Run("admin_users", func(t *testing.T) {
		s := newStore(t)
		u, err := s.BootstrapAdminUser(ctx, "admin", "admin@basic.localhost", "hash1")
		if err != nil || u.ID == "" || !u.IsAdmin {
			t.Fatalf("create admin: %v %+v", err, u)
		}
		got, err := s.GetUser(ctx, u.ID)
		if err != nil || !got.IsAdmin {
			t.Fatalf("admin flag not persisted: %v %+v", err, got)
		}
		again, err := s.BootstrapAdminUser(ctx, "admin", u.Email, "must-not-overwrite")
		if err != nil || again.ID != u.ID || !again.IsAdmin || !again.CreatedAt.Equal(u.CreatedAt) || again.Name != "admin" || again.PasswordHash != "hash1" {
			t.Fatalf("repeat admin login: %v %+v", err, again)
		}
		ordinary, err := s.UpsertUserByEmail(ctx, u.Email, "profile update", "")
		if err != nil || !ordinary.IsAdmin || ordinary.PasswordHash != "hash1" || ordinary.Username != "admin" {
			t.Fatalf("normal profile update demoted admin: %v %+v", err, ordinary)
		}
		legacy, err := s.UpsertUserByEmail(ctx, "legacy@basic.localhost", "legacy", "sub1")
		if err != nil || legacy.IsAdmin {
			t.Fatalf("create ordinary user: %v %+v", err, legacy)
		}
		upgraded, err := s.BootstrapAdminUser(ctx, "legacy", legacy.Email, "hash2")
		if err != nil || upgraded.ID != legacy.ID || !upgraded.IsAdmin || upgraded.Name != legacy.Name || upgraded.GoogleSub != legacy.GoogleSub || !upgraded.CreatedAt.Equal(legacy.CreatedAt) {
			t.Fatalf("upgrade existing user: %v %+v", err, upgraded)
		}
		other, err := s.UpsertUserByEmail(ctx, "google@example.com", "Google user", "sub2")
		if err != nil || other.IsAdmin {
			t.Fatalf("unrelated user became admin: %v %+v", err, other)
		}
	})

	t.Run("concurrent_admin_creation", func(t *testing.T) {
		s := newStore(t)
		type result struct {
			user *store.User
			err  error
		}
		results := make(chan result, 8)
		var wg sync.WaitGroup
		for range cap(results) {
			wg.Go(func() {
				u, err := s.BootstrapAdminUser(ctx, "admin", "admin@basic.localhost", "hash1")
				results <- result{u, err}
			})
		}
		wg.Wait()
		close(results)
		var id string
		for r := range results {
			if r.err != nil || r.user == nil || !r.user.IsAdmin {
				t.Fatalf("concurrent create: %v %+v", r.err, r.user)
			}
			if id != "" && r.user.ID != id {
				t.Fatal("concurrent logins created different users")
			}
			id = r.user.ID
		}
	})

	t.Run("passwords", func(t *testing.T) {
		s := newStore(t)
		if enabled, err := s.HasPasswordUsers(ctx); err != nil || enabled {
			t.Fatalf("empty database local login: %t %v", enabled, err)
		}
		if _, err := s.GetUserByUsername(ctx, "missing"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("missing username: %v", err)
		}
		u, err := s.BootstrapAdminUser(ctx, "admin", "admin@basic.localhost", "old-hash")
		if err != nil {
			t.Fatal(err)
		}
		if enabled, err := s.HasPasswordUsers(ctx); err != nil || !enabled {
			t.Fatalf("local login not enabled: %t %v", enabled, err)
		}
		got, err := s.GetUserByUsername(ctx, "admin")
		if err != nil || got.ID != u.ID || got.PasswordHash != "old-hash" {
			t.Fatalf("lookup local credentials: %v", err)
		}
		other, err := s.UpsertUserByEmail(ctx, "google@example.com", "Google", "sub1")
		if err != nil {
			t.Fatal(err)
		}
		for _, session := range []*store.Session{
			{Hash: "one", UserID: u.ID}, {Hash: "two", UserID: u.ID}, {Hash: "other", UserID: other.ID},
		} {
			if err := s.CreateSession(ctx, session); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.UpdateUserPassword(ctx, u.ID, "wrong-hash", "new-hash"); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("password compare-and-swap accepted stale hash: %v", err)
		}
		if _, err := s.GetSession(ctx, "one"); err != nil {
			t.Fatal("failed password update revoked sessions")
		}
		if err := s.UpdateUserPassword(ctx, u.ID, "old-hash", "new-hash"); err != nil {
			t.Fatal(err)
		}
		for _, hash := range []string{"one", "two"} {
			if _, err := s.GetSession(ctx, hash); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("password change did not revoke session: %v", err)
			}
		}
		if _, err := s.GetSession(ctx, "other"); err != nil {
			t.Fatal("password change revoked another user's session")
		}
		session := &store.Session{Hash: "new", UserID: u.ID, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
		if err := s.CreatePasswordSession(ctx, session, "old-hash"); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("stale password minted a session: %v", err)
		}
		if err := s.CreatePasswordSession(ctx, session, "new-hash"); err != nil {
			t.Fatal(err)
		}
		after, err := s.GetUser(ctx, u.ID)
		if err != nil || !after.IsAdmin || after.PasswordHash != "new-hash" {
			t.Fatalf("password or admin status not persisted: %v", err)
		}
		if err := s.UpdateUserPassword(ctx, other.ID, "", "new-hash"); !errors.Is(err, store.ErrConflict) {
			t.Fatal("passwordless Google account acquired local credentials")
		}
	})

	t.Run("api_keys", func(t *testing.T) {
		s := newStore(t)
		u, _ := s.UpsertUserByEmail(ctx, "k@x.com", "", "")
		k := &store.APIKey{UserID: u.ID, Name: "n", Prefix: "cs_abcdefg", Hash: "h1",
			Spec: []byte(`{"capabilities":{"fs":{}}}`), MaxInstances: 2,
			WorkspaceID: store.PersonalWorkspaceID(u.ID)}
		if err := s.CreateAPIKey(ctx, k); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetAPIKeyByHash(ctx, "h1")
		if err != nil || got.ID != k.ID {
			t.Fatalf("GetAPIKeyByHash: %v %+v", err, got)
		}
		if string(got.Spec) != `{"capabilities":{"fs":{}}}` || got.MaxInstances != 2 {
			t.Fatalf("key spec round-trip: %+v", got)
		}
		keys, err := s.ListAPIKeys(ctx, store.PersonalWorkspaceID(u.ID))
		if err != nil || len(keys) != 1 {
			t.Fatalf("ListAPIKeys: %v %d", err, len(keys))
		}
		if err := s.TouchAPIKey(ctx, k.ID, time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := s.RevokeAPIKey(ctx, "other-user", k.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("revoke as other user: %v", err)
		}
		if err := s.RevokeAPIKey(ctx, u.ID, k.ID); err != nil {
			t.Fatal(err)
		}
		got, _ = s.GetAPIKeyByHash(ctx, "h1")
		if got.RevokedAt == nil {
			t.Fatal("expected RevokedAt set")
		}
		// bound key round-trip + bulk revoke on resource death
		bk := &store.APIKey{UserID: u.ID, Name: "b", Prefix: "cs_boundk", Hash: "h2",
			WorkspaceID: store.PersonalWorkspaceID(u.ID), BoundID: "ins_x"}
		if err := s.CreateAPIKey(ctx, bk); err != nil {
			t.Fatal(err)
		}
		got, err = s.GetAPIKeyByHash(ctx, "h2")
		if err != nil || got.BoundID != "ins_x" {
			t.Fatalf("bound id round-trip: %v %+v", err, got)
		}
		if err := s.RevokeBoundKeys(ctx, "ins_other"); err != nil {
			t.Fatal(err)
		}
		if err := s.RevokeBoundKeys(ctx, "ins_x"); err != nil {
			t.Fatal(err)
		}
		got, _ = s.GetAPIKeyByHash(ctx, "h2")
		if got.RevokedAt == nil {
			t.Fatal("bound key not revoked")
		}
	})

	t.Run("sessions", func(t *testing.T) {
		s := newStore(t)
		u, _ := s.UpsertUserByEmail(ctx, "s@x.com", "", "")
		sess := &store.Session{Hash: "hh", UserID: u.ID, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
		if err := s.CreateSession(ctx, sess); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetSession(ctx, "hh")
		if err != nil || got.UserID != u.ID {
			t.Fatalf("GetSession: %v %+v", err, got)
		}
		old := &store.Session{Hash: "old", UserID: u.ID, CreatedAt: time.Now().Add(-2 * time.Hour), ExpiresAt: time.Now().Add(-time.Hour)}
		if err := s.CreateSession(ctx, old); err != nil {
			t.Fatal(err)
		}
		n, err := s.DeleteExpiredSessions(ctx)
		if err != nil || n != 1 {
			t.Fatalf("DeleteExpiredSessions: %v n=%d", err, n)
		}
		if err := s.DeleteSession(ctx, "hh"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.GetSession(ctx, "hh"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("expected ErrNotFound, got %v", err)
		}
	})

	t.Run("instances", func(t *testing.T) {
		s := newStore(t)
		u, _ := s.UpsertUserByEmail(ctx, "i@x.com", "", "")
		now := time.Now().UTC()
		in := &store.Instance{
			UserID: u.ID, WorkspaceID: store.PersonalWorkspaceID(u.ID), Spec: []byte(`{"ttl_seconds":60}`),
			Labels: map[string]string{"team": "x"}, Status: types.InstanceRunning,
			CreatedAt: now, LastActiveAt: now, ExpiresAt: now.Add(time.Minute),
		}
		if err := s.CreateInstance(ctx, in); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetInstance(ctx, in.ID)
		if err != nil || got.Labels["team"] != "x" {
			t.Fatalf("GetInstance: %v %+v", err, got)
		}
		lst, err := s.ListInstances(ctx, store.InstanceFilter{WorkspaceID: store.PersonalWorkspaceID(u.ID), Status: types.InstanceRunning})
		if err != nil || len(lst) != 1 {
			t.Fatalf("ListInstances: %v %d", err, len(lst))
		}
		in.Status = types.InstanceExpired
		end := now.Add(time.Hour)
		in.EndedAt = &end
		if err := s.UpdateInstance(ctx, in); err != nil {
			t.Fatal(err)
		}
		lst, _ = s.ListInstances(ctx, store.InstanceFilter{WorkspaceID: store.PersonalWorkspaceID(u.ID), Status: types.InstanceRunning})
		if len(lst) != 0 {
			t.Fatalf("expected 0 running, got %d", len(lst))
		}
		lst, _ = s.ListInstances(ctx, store.InstanceFilter{WorkspaceID: store.PersonalWorkspaceID(u.ID)})
		if len(lst) != 1 {
			t.Fatalf("expected 1 total, got %d", len(lst))
		}
		keyed := &store.Instance{UserID: u.ID, WorkspaceID: store.PersonalWorkspaceID(u.ID), Spec: []byte(`{}`), Status: types.InstanceExpired,
			KeyID: "key_abc", CreatedAt: now, LastActiveAt: now, ExpiresAt: now.Add(time.Minute)}
		if err := s.CreateInstance(ctx, keyed); err != nil {
			t.Fatal(err)
		}
		kg, _ := s.GetInstance(ctx, keyed.ID)
		if kg.KeyID != "key_abc" {
			t.Fatalf("KeyID round-trip: %+v", kg)
		}
		lst, _ = s.ListInstances(ctx, store.InstanceFilter{WorkspaceID: store.PersonalWorkspaceID(u.ID), KeyID: "key_abc"})
		if len(lst) != 1 || lst[0].ID != keyed.ID {
			t.Fatalf("ListInstances KeyID: %+v", lst)
		}
		lst, _ = s.ListInstances(ctx, store.InstanceFilter{WorkspaceID: store.PersonalWorkspaceID(u.ID), KeyID: "key_other"})
		if len(lst) != 0 {
			t.Fatalf("ListInstances other KeyID: %+v", lst)
		}
		in2 := &store.Instance{UserID: u.ID, WorkspaceID: store.PersonalWorkspaceID(u.ID), Spec: []byte(`{}`), Status: types.InstanceRunning,
			CreatedAt: now, LastActiveAt: now, ExpiresAt: now.Add(time.Minute)}
		if err := s.CreateInstance(ctx, in2); err != nil {
			t.Fatal(err)
		}
		// Only running rows past their deadline are due; the already
		// expired in and the still-live in2 must both be excluded.
		due, err := s.ListExpiredInstances(ctx, now.Add(30*time.Second), 10)
		if err != nil || len(due) != 0 {
			t.Fatalf("ListExpiredInstances early: %v %d", err, len(due))
		}
		due, err = s.ListExpiredInstances(ctx, now.Add(2*time.Minute), 10)
		if err != nil || len(due) != 1 || due[0].ID != in2.ID {
			t.Fatalf("ListExpiredInstances due: %v %+v", err, due)
		}
		// A binding belongs to node A; sweeping node B must leave it alone.
		epoch, ok, err := s.BindInstance(ctx, in2.ID, "node-a")
		if err != nil || !ok || epoch != 1 {
			t.Fatalf("BindInstance: %v ok=%v epoch=%d", err, ok, epoch)
		}
		if n, err := s.MarkRunningAsLostForNode(ctx, "node-b"); err != nil || n != 0 {
			t.Fatalf("MarkRunningAsLostForNode other node: %v n=%d", err, n)
		}
		// A stale epoch cannot release a newer binding.
		if ok, err := s.ReleaseInstance(ctx, in2.ID, "node-a", epoch+9); err != nil || ok {
			t.Fatalf("ReleaseInstance stale epoch: %v ok=%v", err, ok)
		}
		if ok, err := s.ReleaseInstance(ctx, in2.ID, "node-a", epoch); err != nil || !ok {
			t.Fatalf("ReleaseInstance: %v ok=%v", err, ok)
		}
		// Unbound again: an orphan any node may sweep.
		n, err := s.MarkRunningAsLostForNode(ctx, "node-b")
		if err != nil || n != 1 {
			t.Fatalf("MarkRunningAsLostForNode orphan: %v n=%d", err, n)
		}
		got, _ = s.GetInstance(ctx, in2.ID)
		if got.Status != types.InstanceLost || got.EndedAt == nil {
			t.Fatalf("expected lost: %+v", got)
		}
		// Binding is a claim: a second node loses, a lost row binds nobody.
		in3 := &store.Instance{UserID: u.ID, WorkspaceID: store.PersonalWorkspaceID(u.ID), Spec: []byte(`{}`), Status: types.InstanceRunning,
			CreatedAt: now, LastActiveAt: now, ExpiresAt: now.Add(time.Minute)}
		if err := s.CreateInstance(ctx, in3); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := s.BindInstance(ctx, in3.ID, "node-a"); err != nil || !ok {
			t.Fatalf("BindInstance in3: %v ok=%v", err, ok)
		}
		if _, ok, err := s.BindInstance(ctx, in3.ID, "node-b"); err != nil || ok {
			t.Fatalf("BindInstance conflict: %v ok=%v", err, ok)
		}
	})

	t.Run("notebook", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		u, _ := s.UpsertUserByEmail(ctx, "nb@x.com", "", "")
		// Truncate to micros: PostgreSQL timestamps are microsecond
		// precision, so a nanosecond `now` can never round-trip through
		// TouchInstanceLive's ExpiresAt.Equal check (see the audit
		// subtest's convention below).
		now := time.Now().UTC().Truncate(time.Microsecond)
		in := &store.Instance{UserID: u.ID, WorkspaceID: store.PersonalWorkspaceID(u.ID),
			Spec: []byte(`{}`), Status: types.InstanceRunning,
			CreatedAt: now, LastActiveAt: now, ExpiresAt: now.Add(time.Hour)}
		if err := s.CreateInstance(ctx, in); err != nil {
			t.Fatal(err)
		}
		gi, _ := s.GetInstance(ctx, in.ID)
		if gi.Incarnation != 1 || gi.NotebookRev != 0 {
			t.Fatalf("defaults: %+v", gi)
		}
		// Op lease: claim, conflict, refresh, release.
		claimed, err := s.ClaimInstanceOp(ctx, in.ID, "tok1", time.Minute, types.InstanceRunning)
		if err != nil || claimed.OpToken != "tok1" {
			t.Fatalf("claim: %v %+v", err, claimed)
		}
		if _, err = s.ClaimInstanceOp(ctx, in.ID, "tok2", time.Minute, types.InstanceRunning); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("double claim: %v", err)
		}
		if ok, err := s.RefreshInstanceOp(ctx, in.ID, "tok1", time.Minute); err != nil || !ok {
			t.Fatalf("refresh: %v ok=%v", err, ok)
		}
		if ok, err := s.TouchInstanceLive(ctx, in.ID, "tok1", now, now.Add(2*time.Hour), 1); err != nil || !ok {
			t.Fatalf("touch: %v ok=%v", err, ok)
		}
		gi, _ = s.GetInstance(ctx, in.ID)
		if gi.Drift != 1 || !gi.ExpiresAt.Equal(now.Add(2*time.Hour)) {
			t.Fatalf("touch fields: %+v", gi)
		}
		if seq, err := s.BumpRunSeq(ctx, in.ID, "tok1"); err != nil || seq != 1 {
			t.Fatalf("bump: %v seq=%d", err, seq)
		}
		// Section saves under revision CAS.
		sec := func(name, code string) *store.NotebookSection {
			return &store.NotebookSection{InstanceID: in.ID, Name: name, Code: code,
				CodeSHA256: "sum-" + name, Rerun: store.RerunSafe,
				Defines: []string{"x"}, Uses: []string{}, Effects: []string{},
				DependsOn: []string{},
				CreatedBy: u.ID, UpdatedBy: u.ID, EditedAt: now}
		}
		sa, rev1, err := s.SaveNotebookSection(ctx, in.ID, 0, sec("alpha", "x=1"), "")
		if err != nil || rev1 != 1 || sa.Position != 0 {
			t.Fatalf("save a: %v rev=%d pos=%d", err, rev1, sa.Position)
		}
		_, rev2, err := s.SaveNotebookSection(ctx, in.ID, rev1, sec("beta", "y=2"), "")
		if err != nil || rev2 != 2 {
			t.Fatalf("save b: %v rev=%d", err, rev2)
		}
		// Stale revision conflicts.
		if _, _, err = s.SaveNotebookSection(ctx, in.ID, rev1, sec("gamma", "z=3"), ""); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("stale rev: %v", err)
		}
		// Insert gamma after alpha → order alpha, gamma, beta.
		sc, rev3, err := s.SaveNotebookSection(ctx, in.ID, rev2, sec("gamma", "z=3"), "alpha")
		if err != nil || rev3 != 3 {
			t.Fatalf("save c: %v rev=%d", err, rev3)
		}
		_ = sc
		lst, _ := s.ListNotebookSections(ctx, in.ID)
		var order []string
		for _, x := range lst {
			order = append(order, x.Name)
		}
		if strings.Join(order, ",") != "alpha,gamma,beta" {
			t.Fatalf("order: %v", order)
		}
		// Move beta after alpha → alpha, beta, gamma.
		mv := sec("beta", "y=2")
		mv.Position = -2 // sentinel ignored: store keeps stored position unless after set
		if _, _, err = s.SaveNotebookSection(ctx, in.ID, rev3, mv, "alpha"); err != nil {
			t.Fatalf("move: %v", err)
		}
		lst, _ = s.ListNotebookSections(ctx, in.ID)
		order = order[:0]
		for _, x := range lst {
			order = append(order, x.Name)
		}
		if strings.Join(order, ",") != "alpha,beta,gamma" {
			t.Fatalf("order after move: %v", order)
		}
		// Update preserves created_at.
		ga, _ := s.GetNotebookSection(ctx, in.ID, "alpha")
		ua := sec("alpha", "x=1\nx=2")
		if _, _, err = s.SaveNotebookSection(ctx, in.ID, 4, ua, ""); err != nil {
			t.Fatalf("update: %v", err)
		}
		ga2, _ := s.GetNotebookSection(ctx, in.ID, "alpha")
		if !ga2.CreatedAt.Equal(ga.CreatedAt) || ga2.Code != "x=1\nx=2" {
			t.Fatalf("update fields: %+v", ga2)
		}
		// Delete under CAS reports dependents.
		dep := sec("delta", "w=z")
		dep.Uses = []string{"x"}
		dep.DependsOn = []string{"alpha"}
		if _, _, err = s.SaveNotebookSection(ctx, in.ID, 5, dep, ""); err != nil {
			t.Fatalf("save dep: %v", err)
		}
		if gd, _ := s.GetNotebookSection(ctx, in.ID, "delta"); len(gd.DependsOn) != 1 || gd.DependsOn[0] != "alpha" {
			t.Fatalf("depends_on round-trip: %+v", gd.DependsOn)
		}
		affected, revD, err := s.DeleteNotebookSection(ctx, in.ID, "alpha", 6)
		if err != nil {
			t.Fatalf("delete: %v", err)
		}
		found := false
		for _, a := range affected {
			if a == "delta" {
				found = true
			}
		}
		if !found {
			t.Fatalf("affected: %v", affected)
		}
		if _, _, err = s.DeleteNotebookSection(ctx, in.ID, "alpha", revD); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("re-delete: %v", err)
		}
		// Stamp a run.
		if nrev, err := s.StampSectionRun(ctx, in.ID, "beta", "exe_1", "ok", 1, 7, now, []string{"fs.write"}, true, "sum-beta", "tok1"); err != nil || nrev == 0 {
			t.Fatalf("stamp: %v rev=%d", err, nrev)
		}
		gb, _ := s.GetNotebookSection(ctx, in.ID, "beta")
		if gb.LastExecID != "exe_1" || gb.LastStatus != "ok" || !gb.SucceededBefore || len(gb.Effects) != 1 {
			t.Fatalf("stamp fields: %+v", gb)
		}
		// A stamp under the wrong token writes nothing.
		if _, err := s.StampSectionRun(ctx, in.ID, "beta", "exe_x", "ok", 1, 8, now, nil, true, "sum-beta", "tok2"); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("stamp wrong token: %v", err)
		}
		if gb2, _ := s.GetNotebookSection(ctx, in.ID, "beta"); gb2.LastExecID != "exe_1" {
			t.Fatalf("conflict stamp mutated section: %+v", gb2)
		}
		// Lost → restore lifecycle.
		if ok, err := s.MarkInstanceLost(ctx, in.ID, "tok1", now); err != nil || !ok {
			t.Fatalf("mark lost: %v ok=%v", err, ok)
		}
		gi, _ = s.GetInstance(ctx, in.ID)
		if gi.Status != types.InstanceLost || gi.EndedAt == nil {
			t.Fatalf("lost: %+v", gi)
		}
		// Restore commit: wrong token rejected, right token binds.
		if ok, err := s.CommitRestore(ctx, in.ID, "bad", "node-a", 2, 2, now); err != nil || ok {
			t.Fatalf("commit bad token: %v ok=%v", err, ok)
		}
		if ok, err := s.CommitRestore(ctx, in.ID, "tok1", "node-a", 2, 2, now); err != nil || !ok {
			t.Fatalf("commit: %v ok=%v", err, ok)
		}
		gi, _ = s.GetInstance(ctx, in.ID)
		if gi.Status != types.InstanceRunning || gi.Incarnation != 2 || gi.RestoredAt == nil || gi.Drift != 0 || gi.OpToken != "" {
			t.Fatalf("restored: %+v", gi)
		}
		// ClearRestoreBinding only fires while still lost.
		if err := s.ClearRestoreBinding(ctx, in.ID, "tok1"); err != nil {
			t.Fatal(err)
		}
		// A late stamp from the dead incarnation cannot write onto the
		// restored row — token and incarnation predicates both fail.
		if _, err := s.StampSectionRun(ctx, in.ID, "beta", "exe_late", "ok", 1, 9, now, nil, true, "sum-beta", "tok1"); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("late stamp old incarnation: %v", err)
		}
		// SettleInstance lands the terminal status and clears the op
		// lease in one write — no stale claim can outlive it.
		in3 := &store.Instance{UserID: u.ID, WorkspaceID: store.PersonalWorkspaceID(u.ID),
			Spec: []byte(`{}`), Status: types.InstanceRunning,
			CreatedAt: now, LastActiveAt: now, ExpiresAt: now.Add(time.Hour)}
		if err := s.CreateInstance(ctx, in3); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ClaimInstanceOp(ctx, in3.ID, "tok3", time.Minute, types.InstanceRunning); err != nil {
			t.Fatal(err)
		}
		if err := s.SettleInstance(ctx, in3.ID, types.InstanceExpired, now); err != nil {
			t.Fatal(err)
		}
		g3, _ := s.GetInstance(ctx, in3.ID)
		if g3.Status != types.InstanceExpired || g3.OpToken != "" || g3.OpExpiresAt != nil {
			t.Fatalf("settled row keeps lease: %+v", g3)
		}
		if _, err := s.ClaimInstanceOp(ctx, in3.ID, "tok4", time.Minute, types.InstanceRunning); !errors.Is(err, store.ErrNotClaimable) {
			t.Fatalf("claim settled row: %v", err)
		}
		// An elapsed TTL can never be claimed — no op may renew an
		// expired row back to life.
		past := now.Add(-time.Minute)
		in2 := &store.Instance{UserID: u.ID, WorkspaceID: store.PersonalWorkspaceID(u.ID),
			Spec: []byte(`{}`), Status: types.InstanceRunning,
			CreatedAt: now, LastActiveAt: now, ExpiresAt: past}
		if err := s.CreateInstance(ctx, in2); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ClaimInstanceOp(ctx, in2.ID, "tokx", time.Minute, types.InstanceRunning); !errors.Is(err, store.ErrNotClaimable) {
			t.Fatalf("expired claim: %v", err)
		}
	})

	t.Run("executions", func(t *testing.T) {
		s := newStore(t)
		u, _ := s.UpsertUserByEmail(ctx, "e@x.com", "", "")
		now := time.Now().UTC()
		in := &store.Instance{UserID: u.ID, WorkspaceID: store.PersonalWorkspaceID(u.ID),
			Spec: []byte(`{}`), Status: types.InstanceRunning,
			CreatedAt: now, LastActiveAt: now, ExpiresAt: now.Add(time.Minute)}
		if err := s.CreateInstance(ctx, in); err != nil {
			t.Fatal(err)
		}
		e := &store.Execution{InstanceID: in.ID, UserID: u.ID, CodeSHA256: "abc",
			CodeSnippet: "print(1)", Code: "print(1)\nprint(2)",
			Status: types.ExecOK, DurationMs: 5, Steps: 10, OutputBytes: 2,
			Via: types.AuthAPIKey, Output: "hello\n", OutputTruncated: true,
			ErrorMessage: "boom", ErrorBacktrace: "trace\nback",
			DisplayCount: 1, Displays: []byte(`[{"kind":"vega-lite","title":"T","spec":{"mark":"point"}}]`)}
		if err := s.CreateExecution(ctx, e); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetExecution(ctx, e.ID)
		if err != nil || got.Code != e.Code {
			t.Fatalf("GetExecution: %v %+v", err, got)
		}
		if got.Via != e.Via || got.Output != e.Output || got.OutputTruncated != e.OutputTruncated ||
			got.ErrorMessage != e.ErrorMessage || got.ErrorBacktrace != e.ErrorBacktrace {
			t.Fatalf("detail fields: %+v", got)
		}
		if got.DisplayCount != 1 || string(got.Displays) != string(e.Displays) {
			t.Fatalf("display fields: %+v", got)
		}
		if _, err := s.GetExecution(ctx, "exe_missing"); err == nil {
			t.Fatal("GetExecution: expected ErrNotFound")
		}
		lst, err := s.ListExecutions(ctx, in.ID, 10)
		if err != nil || len(lst) != 1 || lst[0].Steps != 10 || lst[0].DisplayCount != 1 {
			t.Fatalf("ListExecutions: %v %+v", err, lst)
		}

		// ExecutionActivity: one grouped query per listed instance set.
		in2 := &store.Instance{UserID: u.ID, WorkspaceID: in.WorkspaceID,
			Spec: []byte(`{}`), Status: types.InstanceRunning,
			CreatedAt: time.Now(), LastActiveAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
		if err := s.CreateInstance(ctx, in2); err != nil {
			t.Fatal(err)
		}
		// The fixture exec above already sits at time.Now(); keep the
		// error exec newest.
		t1 := time.Now().UTC().Add(3 * time.Minute).Truncate(time.Microsecond)
		t2 := t1.Add(time.Minute)
		t3 := t1.Add(2 * time.Minute)
		for i, e := range []*store.Execution{
			{InstanceID: in.ID, UserID: u.ID, Status: types.ExecOK, CreatedAt: t1},
			{InstanceID: in.ID, UserID: u.ID, Status: types.ExecError, ErrorType: types.ErrPolicyDenied, CreatedAt: t2},
			{InstanceID: in.ID, UserID: u.ID, Status: types.ExecError, ErrorType: types.ErrRuntime, CreatedAt: t3},
			{InstanceID: in2.ID, UserID: u.ID, Status: types.ExecOK, CreatedAt: t1},
		} {
			e.ID = "exe_act" + string(rune(0x30+i))
			e.CodeSHA256, e.Code = "s", "print(1)"
			if err := s.CreateExecution(ctx, e); err != nil {
				t.Fatalf("exec %d: %v", i, err)
			}
		}
		if got, err := s.ExecutionActivity(ctx, nil); err != nil || len(got) != 0 {
			t.Fatalf("empty ids: %v %v", got, err)
		}
		act, err := s.ExecutionActivity(ctx, []string{in.ID, in2.ID})
		if err != nil {
			t.Fatal(err)
		}
		a := act[in.ID]
		if a == nil || a.Runs != 4 || a.DeniedRuns != 1 || a.FailedRuns != 1 {
			t.Fatalf("activity: %+v", a)
		}
		if a.LastStatus != string(types.ExecError) || a.LastRunAt == nil || !a.LastRunAt.Equal(t3) {
			t.Fatalf("last: %+v", a)
		}
		if act[in2.ID].Runs != 1 || act["ins_absent"] != nil {
			t.Fatalf("selectivity: %+v", act)
		}
	})

	t.Run("audit", func(t *testing.T) {
		s := newStore(t)
		now := time.Now().UTC()
		// Workspace scoping goes through the instance's workspace, so
		// the events need a real instance row to match.
		u, _ := s.UpsertUserByEmail(ctx, "a@x.com", "", "")
		ws := store.PersonalWorkspaceID(u.ID)
		in := &store.Instance{UserID: u.ID, WorkspaceID: ws, Spec: []byte(`{}`), Status: types.InstanceRunning,
			CreatedAt: now, LastActiveAt: now, ExpiresAt: now.Add(time.Minute)}
		if err := s.CreateInstance(ctx, in); err != nil {
			t.Fatal(err)
		}
		evs := []store.AuditEvent{
			{Ts: now, UserID: "u1", InstanceID: in.ID, WorkspaceID: ws, ExecID: "e1", Capability: types.CapFS, Op: "read", Args: `{"path":"/work/a"}`, Decision: types.DecisionAllow},
			{Ts: now.Add(time.Second), UserID: "u1", InstanceID: in.ID, WorkspaceID: ws, ExecID: "e2", Capability: types.CapNet, Op: "get", Args: `{}`, Decision: types.DecisionDeny, Phase: types.PhaseBefore, Reason: "nope"},
		}
		if err := s.InsertAuditEvents(ctx, evs); err != nil {
			t.Fatal(err)
		}
		if err := s.InsertAuditEvents(ctx, []store.AuditEvent{{Ts: time.Now(), UserID: u.ID, InstanceID: in.ID, Phase: types.PhaseBefore, Decision: types.DecisionAllow, Capability: types.CapFS}}); err == nil {
			t.Fatal("audit event without workspace_id accepted")
		}
		lst, err := s.ListAuditEvents(ctx, store.AuditFilter{WorkspaceID: ws, Limit: 10})
		if err != nil || len(lst) != 2 {
			t.Fatalf("ListAuditEvents: %v %d", err, len(lst))
		}
		lst, _ = s.ListAuditEvents(ctx, store.AuditFilter{WorkspaceID: "ws_other"})
		if len(lst) != 0 {
			t.Fatalf("other workspace: %d", len(lst))
		}
		lst, _ = s.ListAuditEvents(ctx, store.AuditFilter{WorkspaceID: ws, ExecID: "e1"})
		if len(lst) != 1 || lst[0].Op != "read" {
			t.Fatalf("exec filter: %+v", lst)
		}
		before := now.Add(500 * time.Millisecond)
		lst, _ = s.ListAuditEvents(ctx, store.AuditFilter{WorkspaceID: ws, Before: &before})
		if len(lst) != 1 {
			t.Fatalf("before filter: %d", len(lst))
		}
	})

	t.Run("workspaces", func(t *testing.T) {
		s := newStore(t)
		u, _ := s.UpsertUserByEmail(ctx, "ws1@x.com", "", "")
		u2, _ := s.UpsertUserByEmail(ctx, "ws2@x.com", "", "")
		ws := &store.Workspace{ID: "ws_team1", Name: "team"}
		if err := s.CreateWorkspace(ctx, ws); err != nil {
			t.Fatal(err)
		}
		ws.Name = "renamed"
		if err := s.UpdateWorkspace(ctx, ws); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetWorkspace(ctx, ws.ID)
		if err != nil || got.Name != "renamed" {
			t.Fatalf("GetWorkspace: %v %+v", err, got)
		}
		if err := s.AddMember(ctx, &store.Member{WorkspaceID: ws.ID, UserID: u.ID, Role: store.RoleOwner}); err != nil {
			t.Fatal(err)
		}
		if err := s.AddMember(ctx, &store.Member{WorkspaceID: ws.ID, UserID: u2.ID, Role: store.RoleViewer}); err != nil {
			t.Fatal(err)
		}
		ms, err := s.ListMembers(ctx, ws.ID)
		if err != nil || len(ms) != 2 || ms[0].Email == "" {
			t.Fatalf("ListMembers: %v %+v", err, ms)
		}
		if err := s.UpdateMember(ctx, &store.Member{WorkspaceID: ws.ID, UserID: u2.ID, Role: store.RoleEditor}); err != nil {
			t.Fatal(err)
		}
		m, _ := s.GetMember(ctx, ws.ID, u2.ID)
		if m.Role != store.RoleEditor {
			t.Fatalf("UpdateMember: %v", m.Role)
		}
		if n, _ := s.CountMembers(ctx, ws.ID, store.RoleOwner); n != 1 {
			t.Fatalf("CountMembers owner: %d", n)
		}
		// changes
		if err := s.InsertChange(ctx, &store.WorkspaceChange{WorkspaceID: ws.ID, ByUserID: u.ID,
			Change: store.ChangeMemberAdded, UserID: u2.ID, Role: store.RoleViewer}); err != nil {
			t.Fatal(err)
		}
		cs, err := s.ListChanges(ctx, ws.ID, 10)
		if err != nil || len(cs) != 1 || cs[0].Change != store.ChangeMemberAdded {
			t.Fatalf("ListChanges: %v %+v", err, cs)
		}
		// member removal + key revoke in Tx
		k := &store.APIKey{UserID: u2.ID, WorkspaceID: ws.ID, Name: "k", Prefix: "cs_rev", Hash: "rev"}
		if err := s.CreateAPIKey(ctx, k); err != nil {
			t.Fatal(err)
		}
		if err := s.Tx(ctx, func(tx store.Store) error {
			if err := tx.RevokeUserKeys(ctx, ws.ID, u2.ID); err != nil {
				return err
			}
			return tx.DeleteMember(ctx, ws.ID, u2.ID)
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.GetMember(ctx, ws.ID, u2.ID); err == nil {
			t.Fatal("member still present")
		}
		gotKey, _ := s.GetAPIKey(ctx, k.ID)
		if gotKey.RevokedAt == nil {
			t.Fatal("key not revoked")
		}
		if err := s.DeleteWorkspace(ctx, ws.ID); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("shares", func(t *testing.T) {
		s := newStore(t)
		u, _ := s.UpsertUserByEmail(ctx, "s1@x.com", "", "")
		u2, _ := s.UpsertUserByEmail(ctx, "s2@x.com", "", "")
		now := time.Now().UTC()
		in := &store.Instance{UserID: u.ID, WorkspaceID: store.PersonalWorkspaceID(u.ID), Spec: []byte(`{}`),
			Status: types.InstanceRunning, CreatedAt: now, LastActiveAt: now, ExpiresAt: now.Add(time.Minute)}
		if err := s.CreateInstance(ctx, in); err != nil {
			t.Fatal(err)
		}
		sh := &store.Share{InstanceID: in.ID, UserID: u2.ID, Role: store.RoleViewer, CreatedBy: u.ID}
		if err := s.CreateShare(ctx, sh); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetShare(ctx, in.ID, u2.ID)
		if err != nil || got.Role != store.RoleViewer {
			t.Fatalf("GetShare: %v %+v", err, got)
		}
		lst, err := s.ListShares(ctx, in.ID)
		if err != nil || len(lst) != 1 || lst[0].Email != u2.Email {
			t.Fatalf("ListShares: %v %+v", err, lst)
		}
		shared, err := s.ListSharedInstances(ctx, u2.ID)
		if err != nil || len(shared) != 1 || shared[0].InstanceID != in.ID {
			t.Fatalf("ListSharedInstances: %v %+v", err, shared)
		}
		// upsert role
		sh.Role = store.RoleEditor
		if err := s.CreateShare(ctx, sh); err != nil {
			t.Fatal(err)
		}
		got, _ = s.GetShare(ctx, in.ID, u2.ID)
		if got.Role != store.RoleEditor {
			t.Fatalf("share upsert: %v", got.Role)
		}
		if err := s.DeleteShare(ctx, in.ID, u2.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.GetShare(ctx, in.ID, u2.ID); err == nil {
			t.Fatal("share still present")
		}
	})

	t.Run("workspace id required", func(t *testing.T) {
		s := newStore(t)
		u, _ := s.UpsertUserByEmail(ctx, "w@x.com", "", "")
		for name, fn := range map[string]func() error{
			"instance": func() error { return s.CreateInstance(ctx, &store.Instance{UserID: u.ID}) },
			"policy":   func() error { return s.CreatePolicy(ctx, &store.Policy{UserID: u.ID, Name: "p"}) },
			"llm provider": func() error {
				return s.CreateLLMProvider(ctx, &store.LLMProvider{UserID: u.ID, Name: "lp"})
			},
			"secret": func() error { return s.CreateSecret(ctx, &store.Secret{UserID: u.ID, Name: "s"}) },
			"key":    func() error { return s.CreateAPIKey(ctx, &store.APIKey{UserID: u.ID, Prefix: "cs_x", Hash: "h"}) },
		} {
			if err := fn(); err == nil {
				t.Fatalf("%s without workspace: expected error", name)
			}
		}
	})

	t.Run("invalid enums rejected", func(t *testing.T) {
		s := newStore(t)
		u, _ := s.UpsertUserByEmail(ctx, "v@x.com", "", "")
		now := time.Now().UTC()
		bad := &store.Instance{UserID: u.ID, WorkspaceID: store.PersonalWorkspaceID(u.ID), Spec: []byte(`{}`), Status: types.InstanceStatus("bogus"),
			CreatedAt: now, LastActiveAt: now, ExpiresAt: now.Add(time.Minute)}
		if err := s.CreateInstance(ctx, bad); err == nil {
			t.Fatal("CreateInstance accepted invalid status")
		}
		bad.Status = types.InstanceRunning
		if err := s.CreateInstance(ctx, bad); err != nil {
			t.Fatal(err)
		}
		bad.Status = "bogus"
		if err := s.UpdateInstance(ctx, bad); err == nil {
			t.Fatal("UpdateInstance accepted invalid status")
		}
		e := &store.Execution{InstanceID: bad.ID, UserID: u.ID, Status: types.ExecStatus("weird")}
		if err := s.CreateExecution(ctx, e); err == nil {
			t.Fatal("CreateExecution accepted invalid status")
		}
		e.Status = types.ExecError
		e.ErrorType = types.ExecErrorType("nonsense")
		if err := s.CreateExecution(ctx, e); err == nil {
			t.Fatal("CreateExecution accepted invalid error_type")
		}
		evs := []store.AuditEvent{
			{Ts: now, UserID: "u1", Capability: types.CapFS, Op: "read", Decision: types.Decision("maybe")},
		}
		if err := s.InsertAuditEvents(ctx, evs); err == nil {
			t.Fatal("InsertAuditEvents accepted invalid decision")
		}
		evs[0].Decision = types.DecisionDeny
		evs[0].Phase = types.Phase("middle")
		if err := s.InsertAuditEvents(ctx, evs); err == nil {
			t.Fatal("InsertAuditEvents accepted invalid phase")
		}
	})

	t.Run("policies", func(t *testing.T) {
		s := newStore(t)
		u, _ := s.UpsertUserByEmail(ctx, "p@x.com", "", "")
		p := &store.Policy{UserID: u.ID, WorkspaceID: store.PersonalWorkspaceID(u.ID), Name: "pol", Rego: "package calcside.hooks"}
		if err := s.CreatePolicy(ctx, p); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetPolicy(ctx, p.ID)
		if err != nil || got.Name != "pol" {
			t.Fatalf("GetPolicy: %v %+v", err, got)
		}
		dup := &store.Policy{UserID: u.ID, WorkspaceID: store.PersonalWorkspaceID(u.ID), Name: "pol", Rego: "package calcside.hooks"}
		if err := s.CreatePolicy(ctx, dup); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("duplicate name: want ErrConflict, got %v", err)
		}
		other, _ := s.UpsertUserByEmail(ctx, "p2@x.com", "", "")
		if err := s.CreatePolicy(ctx, &store.Policy{UserID: other.ID, WorkspaceID: store.PersonalWorkspaceID(other.ID), Name: "pol", Rego: "package calcside.hooks"}); err != nil {
			t.Fatalf("same name for another user: %v", err)
		}
		p2 := &store.Policy{UserID: u.ID, WorkspaceID: store.PersonalWorkspaceID(u.ID), Name: "pol2", Rego: "package calcside.hooks"}
		if err := s.CreatePolicy(ctx, p2); err != nil {
			t.Fatal(err)
		}
		p2.Name = "pol"
		if err := s.UpdatePolicy(ctx, p2); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("rename onto existing name: want ErrConflict, got %v", err)
		}
		if err := s.DeletePolicy(ctx, p2.ID); err != nil {
			t.Fatal(err)
		}
		p.Rego = "package calcside.hooks\nx := 1"
		if err := s.UpdatePolicy(ctx, p); err != nil {
			t.Fatal(err)
		}
		got, _ = s.GetPolicy(ctx, p.ID)
		if got.Rego != p.Rego {
			t.Fatalf("UpdatePolicy: %+v", got)
		}
		lst, _ := s.ListPolicies(ctx, store.PersonalWorkspaceID(u.ID))
		if len(lst) != 1 {
			t.Fatalf("ListPolicies: %d", len(lst))
		}
		if err := s.DeletePolicy(ctx, p.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.GetPolicy(ctx, p.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("expected ErrNotFound, got %v", err)
		}
	})

	t.Run("llm providers", func(t *testing.T) {
		s := newStore(t)
		u, _ := s.UpsertUserByEmail(ctx, "lp@x.com", "", "")
		p := &store.LLMProvider{UserID: u.ID, WorkspaceID: store.PersonalWorkspaceID(u.ID),
			Name: "main", Source: "contrib/llm-openai", Models: []string{"gpt-5-mini"},
			Config: json.RawMessage(`{}`)}
		if err := s.CreateLLMProvider(ctx, p); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetLLMProvider(ctx, p.ID)
		if err != nil || got.Name != "main" || len(got.Models) != 1 || got.Models[0] != "gpt-5-mini" {
			t.Fatalf("GetLLMProvider: %v %+v", err, got)
		}
		dup := &store.LLMProvider{UserID: u.ID, WorkspaceID: store.PersonalWorkspaceID(u.ID),
			Name: "main", Source: "contrib/llm-openai", Models: []string{"m"}, Config: json.RawMessage(`{}`)}
		if err := s.CreateLLMProvider(ctx, dup); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("duplicate name: want ErrConflict, got %v", err)
		}
		other, _ := s.UpsertUserByEmail(ctx, "lp2@x.com", "", "")
		if err := s.CreateLLMProvider(ctx, &store.LLMProvider{UserID: other.ID,
			WorkspaceID: store.PersonalWorkspaceID(other.ID), Name: "main",
			Source: "contrib/llm-openai", Models: []string{"m"}, Config: json.RawMessage(`{}`)}); err != nil {
			t.Fatalf("same name for another user: %v", err)
		}
		p.Models = []string{"gpt-5"}
		p.Config = json.RawMessage(`{}`)
		if err := s.UpdateLLMProvider(ctx, p); err != nil {
			t.Fatal(err)
		}
		got, _ = s.GetLLMProvider(ctx, p.ID)
		if len(got.Models) != 1 || got.Models[0] != "gpt-5" {
			t.Fatalf("UpdateLLMProvider: %+v", got)
		}
		lst, _ := s.ListLLMProviders(ctx, store.PersonalWorkspaceID(u.ID))
		if len(lst) != 1 {
			t.Fatalf("ListLLMProviders: %d", len(lst))
		}
		if err := s.DeleteLLMProvider(ctx, p.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.GetLLMProvider(ctx, p.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("expected ErrNotFound, got %v", err)
		}
	})

	t.Run("secrets", func(t *testing.T) {
		s := newStore(t)
		u, _ := s.UpsertUserByEmail(ctx, "sec@x.com", "", "")
		u2, _ := s.UpsertUserByEmail(ctx, "sec2@x.com", "", "")
		sec := &store.Secret{UserID: u.ID, WorkspaceID: store.PersonalWorkspaceID(u.ID), Name: "TOKEN", Ciphertext: []byte("sealed"), AllowedDomains: []string{"api.x.com"}}
		if err := s.CreateSecret(ctx, sec); err != nil {
			t.Fatal(err)
		}
		if sec.ID == "" {
			t.Fatal("expected generated id")
		}
		// duplicate name for same user -> ErrConflict
		dup := &store.Secret{UserID: u.ID, WorkspaceID: store.PersonalWorkspaceID(u.ID), Name: "TOKEN", Ciphertext: []byte("x"), AllowedDomains: []string{"a.com"}}
		if err := s.CreateSecret(ctx, dup); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("expected ErrConflict, got %v", err)
		}
		// same name for another user is fine
		other := &store.Secret{UserID: u2.ID, WorkspaceID: store.PersonalWorkspaceID(u2.ID), Name: "TOKEN", Ciphertext: []byte("y"), AllowedDomains: []string{"b.com"}}
		if err := s.CreateSecret(ctx, other); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetSecret(ctx, sec.ID)
		if err != nil || got.Name != "TOKEN" || string(got.Ciphertext) != "sealed" {
			t.Fatalf("GetSecret: %v %+v", err, got)
		}
		got, err = s.GetSecretByName(ctx, store.PersonalWorkspaceID(u.ID), "TOKEN")
		if err != nil || got.ID != sec.ID {
			t.Fatalf("GetSecretByName: %v %+v", err, got)
		}
		if _, err := s.GetSecretByName(ctx, store.PersonalWorkspaceID(u2.ID), "TOKEN"); err != nil {
			t.Fatalf("per-user GetSecretByName: %v", err)
		}
		lst, _ := s.ListSecrets(ctx, store.PersonalWorkspaceID(u.ID))
		if len(lst) != 1 || lst[0].Name != "TOKEN" {
			t.Fatalf("ListSecrets: %+v", lst)
		}
		sec.AllowedDomains = []string{"api.x.com", "*.x.com"}
		sec.Ciphertext = []byte("rotated")
		if err := s.UpdateSecret(ctx, sec); err != nil {
			t.Fatal(err)
		}
		got, _ = s.GetSecret(ctx, sec.ID)
		if string(got.Ciphertext) != "rotated" || len(got.AllowedDomains) != 2 {
			t.Fatalf("UpdateSecret: %+v", got)
		}
		if err := s.DeleteSecret(ctx, sec.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.GetSecret(ctx, sec.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("expected ErrNotFound, got %v", err)
		}
	})

	t.Run("serving", func(t *testing.T) {
		serving(t, newStore(t))
	})

	t.Run("applications", func(t *testing.T) {
		RunApplications(t, newStore(t))
	})
	t.Run("runs", func(t *testing.T) {
		RunRuns(t, newStore(t))
	})
	t.Run("extension_artifacts", func(t *testing.T) {
		RunExtensionArtifacts(t, newStore(t))
	})
	t.Run("extension_versions", func(t *testing.T) {
		RunExtensionVersions(t, newStore(t))
	})
	t.Run("workspace_write_lock", func(t *testing.T) {
		workspaceWriteLockSuite(t, newStore(t))
	})
	t.Run("modules", func(t *testing.T) {
		RunModules(t, newStore(t))
	})
}

// Workspace write-lock contention: a held workspace transaction blocks
// another connection's workspace-scoped mutation until released — the
// serialization point the reviewed publish depends on.
func workspaceWriteLockSuite(t *testing.T, s store.Store) {
	ctx := context.Background()
	u, err := s.UpsertUserByEmail(ctx, "wwl@x.com", "", "")
	if err != nil {
		t.Fatal(err)
	}
	ws := store.PersonalWorkspaceID(u.ID)
	if err := s.CreatePolicy(ctx, &store.Policy{UserID: u.ID, WorkspaceID: ws, Name: "p", Rego: "package t"}); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListPolicies(ctx, ws)
	if err != nil || len(list) != 1 {
		t.Fatalf("seed policy: %v", err)
	}
	pol := list[0]

	// Hold the workspace lock in a tx; concurrent UpdatePolicy must
	// block until the holder commits.
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- s.Tx(ctx, func(tx store.Store) error {
			if err := tx.LockWorkspace(ctx, ws); err != nil {
				return err
			}
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered

	blocked := make(chan error, 1)
	go func() {
		p := *pol
		p.Rego = "package t2"
		blocked <- s.UpdatePolicy(ctx, &p)
	}()
	// The update must NOT finish while the lock is held.
	select {
	case err := <-blocked:
		t.Fatalf("update finished under held lock: %v", err)
	case <-time.After(500 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("lock tx: %v", err)
	}
	select {
	case err := <-blocked:
		if err != nil {
			t.Fatalf("blocked update: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("update never completed after lock release")
	}
}
