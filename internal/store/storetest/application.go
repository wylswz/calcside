package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"calcside/internal/runtime"
	"calcside/internal/store"
)

// RunApplications exercises the application-draft persistence contract
// (EP-0014): CAS drafts, publish pointer, soft delete.
func RunApplications(t *testing.T, s store.Store) {
	t.Helper()
	ctx := context.Background()
	u, err := s.UpsertUserByEmail(ctx, "app@x.com", "App", "sub-app")
	if err != nil {
		t.Fatal(err)
	}
	spec := json.RawMessage(`{"ttl_seconds":300}`)
	prog := runtime.ProgramBundle{Sources: map[string][]byte{"main.star": []byte("def main(input): return input")},
		InputSchema:  json.RawMessage(`{"type":"object","required":["name"],"properties":{"name":{"type":"string"}}}`),
		OutputSchema: json.RawMessage(`{"type":"object"}`)}

	t.Run("create_get_list", func(t *testing.T) {
		a, err := store.NewApplication("ws_1", u.ID, "svc", "def main(i): return i", spec, nil, nil, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if err := s.CreateApplication(ctx, a); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetApplication(ctx, a.ID)
		if err != nil || got.Name != "svc" || got.Version != 1 || got.PublishedVersion != 0 {
			t.Fatalf("get: %v %+v", err, got)
		}
		if got.InputSchema != nil || got.OutputSchema != nil {
			t.Fatal("optional schemas should stay nil until publish")
		}
		list, err := s.ListApplications(ctx, store.ApplicationFilter{WorkspaceID: "ws_1"})
		if err != nil || len(list) != 1 {
			t.Fatalf("list: %v %d", err, len(list))
		}
	})

	t.Run("cross_workspace_isolation", func(t *testing.T) {
		a, _ := store.NewApplication("ws_2", u.ID, "svc2", "", spec, nil, nil, time.Now())
		if err := s.CreateApplication(ctx, a); err != nil {
			t.Fatal(err)
		}
		l1, _ := s.ListApplications(ctx, store.ApplicationFilter{WorkspaceID: "ws_1"})
		l2, _ := s.ListApplications(ctx, store.ApplicationFilter{WorkspaceID: "ws_2"})
		if len(l1) == 0 || len(l2) == 0 || l1[0].WorkspaceID == l2[0].WorkspaceID {
			t.Fatalf("isolation: %d %d", len(l1), len(l2))
		}
		n, _ := s.CountApplications(ctx, "ws_1", u.ID)
		if n != 1 {
			t.Fatalf("count: %d", n)
		}
	})

	t.Run("draft_cas", func(t *testing.T) {
		a, _ := store.NewApplication("ws_1", u.ID, "cas", "v1", spec, nil, nil, time.Now())
		s.CreateApplication(ctx, a)
		a.Code = "v2"
		ok, err := s.UpdateApplicationDraft(ctx, a, 1)
		if err != nil || !ok {
			t.Fatalf("cas ok: %v %v", ok, err)
		}
		got, _ := s.GetApplication(ctx, a.ID)
		if got.Version != 2 || got.Code != "v2" {
			t.Fatalf("version: %+v", got)
		}
		// Stale version is rejected without writing.
		a.Code = "v3"
		ok, _ = s.UpdateApplicationDraft(ctx, a, 1)
		if ok {
			t.Fatal("stale CAS accepted")
		}
		got, _ = s.GetApplication(ctx, a.ID)
		if got.Code != "v2" {
			t.Fatal("stale write leaked")
		}
	})

	t.Run("schema_validation", func(t *testing.T) {
		// $ref is rejected at the constructor and at the store layer.
		if _, err := store.NewApplication("ws_1", u.ID, "bad", "", spec,
			json.RawMessage(`{"$ref":"x"}`), nil, time.Now()); err == nil {
			t.Fatal("$ref schema accepted at constructor")
		}
		bad := &store.Application{WorkspaceID: "ws_1", UserID: u.ID, Name: "bad",
			Spec: spec, InputSchema: json.RawMessage(`{"$ref":"x"}`)}
		if err := s.CreateApplication(ctx, bad); err == nil {
			t.Fatal("$ref schema accepted at store")
		}
		big, _ := store.NewApplication("ws_1", u.ID, "big", "", spec,
			json.RawMessage(`{"type":"object","minimum":9007199254740993}`), nil, time.Now())
		if err := s.CreateApplication(ctx, big); err != nil {
			t.Fatalf("bigint schema rejected: %v", err)
		}
		got, _ := s.GetApplication(ctx, big.ID)
		if !jsonBytesContain(got.InputSchema, "9007199254740993") {
			t.Fatal("bigint literal rounded")
		}
	})

	t.Run("publish_and_delete", func(t *testing.T) {
		a, _ := store.NewApplication("ws_1", u.ID, "pub", "def main(i): return i", spec, nil, nil, time.Now())
		if err := s.CreateApplication(ctx, a); err != nil {
			t.Fatal(err)
		}
		before, err := s.CountApplications(ctx, "ws_1", u.ID)
		if err != nil {
			t.Fatal(err)
		}
		rel, err := store.NewServingRelease("ws_1", u.ID, spec, runtime.PolicyBundle{}, prog, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		pin, err := s.PublishApplication(ctx, "ws_1", a.ID, 1, rel, time.Now())
		if err != nil || pin == nil || !pin.Enabled {
			t.Fatalf("publish: %v %+v", err, pin)
		}
		got, _ := s.GetApplication(ctx, a.ID)
		if got.PublishedVersion != 1 || got.PersistentInstanceID != pin.ID {
			t.Fatalf("pointer: %+v", got)
		}
		// Same-version republish returns the same pin, no generation bump.
		pin2, err := s.PublishApplication(ctx, "ws_1", a.ID, 1, rel, time.Now())
		if err != nil || pin2.ID != pin.ID || pin2.Generation != pin.Generation {
			t.Fatalf("republish bumped: %+v", pin2)
		}
		// Stale version is rejected.
		if _, err := s.PublishApplication(ctx, "ws_1", a.ID, 9, rel, time.Now()); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("stale publish: %v", err)
		}
		now := time.Now().UTC()
		node, err := s.RegisterServingNode(ctx, &store.ServingNode{
			NodeID: "app-node", BootID: "app-boot", Addr: "local", Capacity: 1, HeartbeatUntil: now.Add(time.Minute),
		}, now)
		if err != nil {
			t.Fatal(err)
		}
		rep, err := s.ClaimServingReplica(ctx, store.ServingReplicaClaim{
			InstanceID: pin.ID, ReleaseID: rel.ID, NodeID: node.NodeID, BootID: node.BootID,
			NodeIncarnation: node.Incarnation, ReplicaID: store.NewID(store.PrefixServingReplica),
			Generation: pin.Generation, ExpectedEpoch: pin.LastEpoch, Now: now, LeaseUntil: now.Add(30 * time.Second),
		})
		if err != nil {
			t.Fatal(err)
		}
		// Draft update bumps version, then publish records it.
		a.Code = "v2"
		if ok, err := s.UpdateApplicationDraft(ctx, a, 1); err != nil || !ok {
			t.Fatalf("draft update: %v, %v", ok, err)
		}
		prog2 := prog
		prog2.Sources = map[string][]byte{"main.star": []byte("def main(input): return {\"changed\": True}")}
		rel2, err := store.NewServingRelease("ws_1", u.ID, spec, runtime.PolicyBundle{}, prog2, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.PublishApplication(ctx, "ws_1", a.ID, 1, rel2, time.Now()); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("stale replacement publish: %v", err)
		}
		if _, err := s.GetServingRelease(ctx, "ws_1", rel2.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("stale publish persisted a release: %v", err)
		}
		pin3, err := s.PublishApplication(ctx, "ws_1", a.ID, 2, rel2, time.Now())
		if err != nil || pin3.ID != pin.ID || pin3.ReleaseID != rel2.ID ||
			pin3.Generation != pin.Generation+1 || pin3.LastEpoch != rep.Epoch {
			t.Fatalf("republish: %v %+v", err, pin3)
		}
		old, err := s.GetServingReplica(ctx, rep.ID)
		if err != nil || !old.LeaseUntil.Equal(rep.LeaseUntil) {
			t.Fatalf("republish shortened the old lease: %+v, %v", old, err)
		}
		got, _ = s.GetApplication(ctx, a.ID)
		if got.PublishedVersion != 2 {
			t.Fatalf("published_version: %d", got.PublishedVersion)
		}
		// Soft delete hides the row, disables the instance.
		ok, err := s.DeleteApplication(ctx, "ws_1", a.ID, 2, time.Now())
		if err != nil || !ok {
			t.Fatalf("delete: %v %v", ok, err)
		}
		if _, err := s.GetApplication(ctx, a.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatal("deleted row still visible")
		}
		n, err := s.CountApplications(ctx, "ws_1", u.ID)
		if err != nil || n != before-1 {
			t.Fatalf("deleted app must release exactly one quota slot: before=%d after=%d err=%v", before, n, err)
		}
		pinRow, _ := s.GetPersistentInstance(ctx, "ws_1", pin.ID)
		if pinRow == nil || pinRow.Enabled {
			t.Fatal("delete did not disable the persistent instance")
		}
	})
}

func jsonBytesContain(r json.RawMessage, sub string) bool {
	return r != nil && strings.Contains(string(r), sub)
}
