package application

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	_ "calcside/cmd/calcside/internal/gormstore"
	"calcside/cmd/calcside/internal/service"
	"calcside/cmd/calcside/internal/service/access"
	"calcside/cmd/calcside/internal/service/sandbox"
	"calcside/internal/capability"
	capfs "calcside/internal/capability/fs"
	"calcside/internal/engine"
	"calcside/internal/runtime"
	"calcside/internal/runtime/instance"
	"calcside/internal/store"
	"calcside/internal/store/storetest"
	"calcside/internal/types"
)

// newSvc wires the service to a real Manager so publish/test flows
// exercise real prepares and invokes.
func newSvc(t *testing.T) (*Service, store.Store) {
	t.Helper()
	st, err := store.Open(context.Background(), types.DriverSQLite, storetest.SQLite(t))
	if err != nil {
		t.Fatal(err)
	}
	reg := capability.NewRegistry()
	reg.Register(capfs.Factory())
	rt := instance.New(instance.Options{
		Engine: engine.New(8), Registry: reg,
		Limits: capability.ServerLimits{DefaultTTL: time.Minute, MaxTTL: time.Hour, MaxFSQuotaBytes: 256 << 20},
	})
	t.Cleanup(rt.StopReaper)
	sb := sandbox.New(sandbox.Options{Store: st, Runtime: rt, Secrets: nopSecrets{}})
	svc, err := New(Options{
		Store: st, Runtime: rt, Sandbox: sb,
		Access: access.New(st), MaxPerUser: 4, Now: time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc, st
}

func ownerActor(st store.Store) service.Actor {
	u, _ := st.UpsertUserByEmail(context.Background(), "o@x.com", "O", "o1")
	return service.Actor{UserID: u.ID, WorkspaceID: store.PersonalWorkspaceID(u.ID), Kind: types.AuthSession}
}

func TestCreateDefaults(t *testing.T) {
	svc, st := newSvc(t)
	a := ownerActor(st)
	v, err := svc.Create(context.Background(), a, CreateRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if v.Application.Name != "Untitled application" || v.Application.Code != scaffold ||
		v.Status != "draft" || v.Application.Version != 1 {
		t.Fatalf("defaults: %+v", v)
	}
	if v.Application.InputSchema != nil || v.Application.OutputSchema != nil {
		t.Fatal("nil schemas should stay nil until publish")
	}
}

func TestUpdateCASAndValidation(t *testing.T) {
	svc, st := newSvc(t)
	a := ownerActor(st)
	v, _ := svc.Create(context.Background(), a, CreateRequest{Name: "x"})
	id := v.Application.ID
	// stale version
	if _, err := svc.Update(context.Background(), a, id, UpdateRequest{Version: 9, Code: "x"}); err == nil {
		t.Fatal("stale version accepted")
	}
	// plaintext secret in spec is rejected
	bad := json.RawMessage(`{"secrets":{"tok":{"value":"plaintext","source":"inline"}}}`)
	if _, err := svc.Update(context.Background(), a, id,
		UpdateRequest{Version: 1, Spec: bad}); err == nil {
		t.Fatal("plaintext secret accepted")
	}
	// vault ref is normalized and accepted
	ref := json.RawMessage(`{"ttl_seconds":300}`)
	v2, err := svc.Update(context.Background(), a, id, UpdateRequest{Version: 1, Spec: ref, Code: "def main(i): return i"})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if v2.Application.Version != 2 {
		t.Fatalf("version: %+v", v2.Application)
	}
}

func TestPublishAndInvoke(t *testing.T) {
	svc, st := newSvc(t)
	a := ownerActor(st)
	code := "def main(input): return input"
	v, _ := svc.Create(context.Background(), a, CreateRequest{Name: "svc", Code: &code})
	id := v.Application.ID
	if _, err := svc.Publish(context.Background(), a, id, 9); err == nil {
		t.Fatal("stale publish accepted")
	}
	pv, err := svc.Publish(context.Background(), a, id, 1)
	if err != nil {
		t.Fatal(err)
	}
	if pv.Application.PublishedVersion != 1 || pv.Application.PersistentInstanceID == "" {
		t.Fatalf("publish: %+v", pv.Application)
	}
	// default object schema materialized at publish, not draft
	got, _ := st.GetApplication(context.Background(), id)
	if got.InputSchema != nil {
		t.Fatal("schema persisted before publish")
	}
	// invoke routes to the replica publish prepared
	rv, err := svc.Test(context.Background(), a, id, 1, json.RawMessage(`{"name":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if rv.OutputJSON == "" {
		t.Fatal("empty output")
	}
}

func TestQuota(t *testing.T) {
	svc, st := newSvc(t)
	a := ownerActor(st)
	for i := 0; i < 4; i++ {
		if _, err := svc.Create(context.Background(), a, CreateRequest{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.Create(context.Background(), a, CreateRequest{}); err == nil {
		t.Fatal("quota not enforced")
	}
}

func TestDelete(t *testing.T) {
	svc, st := newSvc(t)
	a := ownerActor(st)
	v, _ := svc.Create(context.Background(), a, CreateRequest{})
	id := v.Application.ID
	if err := svc.Delete(context.Background(), a, id, 9); err == nil {
		t.Fatal("stale delete accepted")
	}
	if err := svc.Delete(context.Background(), a, id, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Get(context.Background(), a, id); err == nil {
		t.Fatal("deleted app still visible")
	}
}

type nopSecrets struct{}

func (nopSecrets) Resolve(ctx context.Context, ws string, in map[string]runtime.SecretSpec) ([]runtime.Secret, map[string]runtime.SecretSpec, error) {
	return nil, in, nil
}
