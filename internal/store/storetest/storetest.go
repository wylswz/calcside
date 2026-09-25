// Package storetest is a conformance suite every store.Store
// implementation must pass.
package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"calcside/internal/store"
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
		if u.ID == "" || u.Email != "a@x.com" {
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

	t.Run("api_keys", func(t *testing.T) {
		s := newStore(t)
		u, _ := s.UpsertUserByEmail(ctx, "k@x.com", "", "")
		k := &store.APIKey{UserID: u.ID, Name: "n", Prefix: "cs_abcdefg", Hash: "h1"}
		if err := s.CreateAPIKey(ctx, k); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetAPIKeyByHash(ctx, "h1")
		if err != nil || got.ID != k.ID {
			t.Fatalf("GetAPIKeyByHash: %v %+v", err, got)
		}
		keys, err := s.ListAPIKeys(ctx, u.ID)
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
			UserID: u.ID, Spec: []byte(`{"ttl_seconds":60}`),
			Labels: map[string]string{"team": "x"}, Status: store.StatusRunning,
			CreatedAt: now, LastActiveAt: now, ExpiresAt: now.Add(time.Minute),
		}
		if err := s.CreateInstance(ctx, in); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetInstance(ctx, in.ID)
		if err != nil || got.Labels["team"] != "x" {
			t.Fatalf("GetInstance: %v %+v", err, got)
		}
		lst, err := s.ListInstances(ctx, u.ID, store.StatusRunning)
		if err != nil || len(lst) != 1 {
			t.Fatalf("ListInstances: %v %d", err, len(lst))
		}
		in.Status = store.StatusExpired
		end := now.Add(time.Hour)
		in.EndedAt = &end
		if err := s.UpdateInstance(ctx, in); err != nil {
			t.Fatal(err)
		}
		lst, _ = s.ListInstances(ctx, u.ID, store.StatusRunning)
		if len(lst) != 0 {
			t.Fatalf("expected 0 running, got %d", len(lst))
		}
		lst, _ = s.ListInstances(ctx, u.ID, "")
		if len(lst) != 1 {
			t.Fatalf("expected 1 total, got %d", len(lst))
		}
		in2 := &store.Instance{UserID: u.ID, Spec: []byte(`{}`), Status: store.StatusRunning,
			CreatedAt: now, LastActiveAt: now, ExpiresAt: now.Add(time.Minute)}
		if err := s.CreateInstance(ctx, in2); err != nil {
			t.Fatal(err)
		}
		n, err := s.MarkRunningAsLost(ctx)
		if err != nil || n != 1 {
			t.Fatalf("MarkRunningAsLost: %v n=%d", err, n)
		}
		got, _ = s.GetInstance(ctx, in2.ID)
		if got.Status != store.StatusLost || got.EndedAt == nil {
			t.Fatalf("expected lost: %+v", got)
		}
	})

	t.Run("executions", func(t *testing.T) {
		s := newStore(t)
		u, _ := s.UpsertUserByEmail(ctx, "e@x.com", "", "")
		now := time.Now().UTC()
		in := &store.Instance{UserID: u.ID, Spec: []byte(`{}`), Status: store.StatusRunning,
			CreatedAt: now, LastActiveAt: now, ExpiresAt: now.Add(time.Minute)}
		if err := s.CreateInstance(ctx, in); err != nil {
			t.Fatal(err)
		}
		e := &store.Execution{InstanceID: in.ID, UserID: u.ID, CodeSHA256: "abc",
			CodeSnippet: "print(1)", Status: store.ExecOK, DurationMs: 5, Steps: 10, OutputBytes: 2}
		if err := s.CreateExecution(ctx, e); err != nil {
			t.Fatal(err)
		}
		lst, err := s.ListExecutions(ctx, in.ID, 10)
		if err != nil || len(lst) != 1 || lst[0].Steps != 10 {
			t.Fatalf("ListExecutions: %v %+v", err, lst)
		}
	})

	t.Run("audit", func(t *testing.T) {
		s := newStore(t)
		now := time.Now().UTC()
		evs := []store.AuditEvent{
			{Ts: now, UserID: "u1", InstanceID: "i1", ExecID: "e1", Capability: "fs", Op: "read", Args: `{"path":"/work/a"}`, Decision: "allow"},
			{Ts: now.Add(time.Second), UserID: "u1", InstanceID: "i1", ExecID: "e2", Capability: "net", Op: "get", Args: `{}`, Decision: "deny", Phase: "before", Reason: "nope"},
		}
		if err := s.InsertAuditEvents(ctx, evs); err != nil {
			t.Fatal(err)
		}
		lst, err := s.ListAuditEvents(ctx, store.AuditFilter{UserID: "u1", Limit: 10})
		if err != nil || len(lst) != 2 {
			t.Fatalf("ListAuditEvents: %v %d", err, len(lst))
		}
		lst, _ = s.ListAuditEvents(ctx, store.AuditFilter{UserID: "u1", ExecID: "e1"})
		if len(lst) != 1 || lst[0].Op != "read" {
			t.Fatalf("exec filter: %+v", lst)
		}
		before := now.Add(500 * time.Millisecond)
		lst, _ = s.ListAuditEvents(ctx, store.AuditFilter{UserID: "u1", Before: &before})
		if len(lst) != 1 {
			t.Fatalf("before filter: %d", len(lst))
		}
	})

	t.Run("policies", func(t *testing.T) {
		s := newStore(t)
		u, _ := s.UpsertUserByEmail(ctx, "p@x.com", "", "")
		p := &store.Policy{UserID: u.ID, Name: "pol", Rego: "package calcside.hooks", Enabled: true}
		if err := s.CreatePolicy(ctx, p); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetPolicy(ctx, p.ID)
		if err != nil || !got.Enabled {
			t.Fatalf("GetPolicy: %v %+v", err, got)
		}
		p.Enabled = false
		p.Rego = "package calcside.hooks\nx := 1"
		if err := s.UpdatePolicy(ctx, p); err != nil {
			t.Fatal(err)
		}
		got, _ = s.GetPolicy(ctx, p.ID)
		if got.Enabled || got.Rego != p.Rego {
			t.Fatalf("UpdatePolicy: %+v", got)
		}
		lst, _ := s.ListPolicies(ctx, u.ID)
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

	t.Run("secrets", func(t *testing.T) {
		s := newStore(t)
		u, _ := s.UpsertUserByEmail(ctx, "sec@x.com", "", "")
		u2, _ := s.UpsertUserByEmail(ctx, "sec2@x.com", "", "")
		sec := &store.Secret{UserID: u.ID, Name: "TOKEN", Ciphertext: []byte("sealed"), AllowedDomains: []string{"api.x.com"}}
		if err := s.CreateSecret(ctx, sec); err != nil {
			t.Fatal(err)
		}
		if sec.ID == "" {
			t.Fatal("expected generated id")
		}
		// duplicate name for same user -> ErrConflict
		dup := &store.Secret{UserID: u.ID, Name: "TOKEN", Ciphertext: []byte("x"), AllowedDomains: []string{"a.com"}}
		if err := s.CreateSecret(ctx, dup); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("expected ErrConflict, got %v", err)
		}
		// same name for another user is fine
		other := &store.Secret{UserID: u2.ID, Name: "TOKEN", Ciphertext: []byte("y"), AllowedDomains: []string{"b.com"}}
		if err := s.CreateSecret(ctx, other); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetSecret(ctx, sec.ID)
		if err != nil || got.Name != "TOKEN" || string(got.Ciphertext) != "sealed" {
			t.Fatalf("GetSecret: %v %+v", err, got)
		}
		got, err = s.GetSecretByName(ctx, u.ID, "TOKEN")
		if err != nil || got.ID != sec.ID {
			t.Fatalf("GetSecretByName: %v %+v", err, got)
		}
		if _, err := s.GetSecretByName(ctx, u2.ID, "TOKEN"); err != nil {
			t.Fatalf("per-user GetSecretByName: %v", err)
		}
		lst, _ := s.ListSecrets(ctx, u.ID)
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
}
