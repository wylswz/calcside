package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
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
	seedWS(t, s, "ws_1", "ws_2")
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
		list, err = s.ListApplications(ctx, store.ApplicationFilter{WorkspaceID: "ws_1", KeyApplicationID: a.ID})
		if err != nil || len(list) != 1 {
			t.Fatalf("bound list: %v %d", err, len(list))
		}
		list, err = s.ListApplications(ctx, store.ApplicationFilter{WorkspaceID: "ws_1", KeyApplicationID: "app_other"})
		if err != nil || len(list) != 0 {
			t.Fatalf("bound list other: %v %d", err, len(list))
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

	t.Run("reviewed_publish", func(t *testing.T) { applicationReviewedSuite(t, s) })
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
		// A non-microsecond fraction makes the PG precision regression
		// deterministic: timestamptz stores microseconds, so the claim's
		// in-memory LeaseUntil can never Equal the round-tripped row.
		now := time.Now().UTC().Truncate(time.Second).Add(123456789 * time.Nanosecond)
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
		// Compare stored timestamps on both sides of the publish: PG
		// stores microseconds while the claim's returned copy keeps
		// nanoseconds, so the old-lease equality must check the
		// round-tripped value.
		persisted, err := s.GetServingReplica(ctx, rep.ID)
		if err != nil {
			t.Fatal(err)
		}
		leaseBefore := persisted.LeaseUntil
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
		if err != nil || !old.LeaseUntil.Equal(leaseBefore) {
			t.Fatalf("republish shortened the old lease: before=%s old=%+v, %v", leaseBefore.Format(time.RFC3339Nano), old, err)
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

// Reviewed publish: base-pointer CAS, immutable seq history, no-op
// stability, soft-delete retention.
func applicationReviewedSuite(t *testing.T, s store.Store) {
	ctx := context.Background()
	u, err := s.UpsertUserByEmail(ctx, "rp@x.com", "", "")
	if err != nil {
		t.Fatal(err)
	}
	seedWS(t, s, "other")
	mkRel := func(code string) *store.ServingRelease {
		prog := runtime.ProgramBundle{Sources: map[string][]byte{"main.star": []byte(code)},
			InputSchema: json.RawMessage(`{"type":"object"}`), OutputSchema: json.RawMessage(`{"type":"object"}`)}
		r, err := store.NewServingReleaseWithModules("ws_1", u.ID, json.RawMessage(`{"ttl_seconds":300}`),
			runtime.PolicyBundle{}, prog, nil, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	a, err := store.NewApplication("ws_1", u.ID, "svc", "", json.RawMessage(`{"ttl_seconds":300}`), nil, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateApplication(ctx, a); err != nil {
		t.Fatal(err)
	}
	rel := mkRel("def main(i): return 1")
	pin, hist, err := s.PublishReviewedApplication(ctx, "ws_1", a.ID, 1, "", rel, u.ID, "first", time.Now())
	if err != nil || pin == nil || hist == nil || hist.Seq != 1 {
		t.Fatalf("first publish: %v %v", hist, err)
	}
	// Same draft + same candidate -> no-op, no history, no generation bump.
	pin2, hist2, err := s.PublishReviewedApplication(ctx, "ws_1", a.ID, 1, rel.ID, rel, u.ID, "", time.Now())
	if err != nil || hist2 != nil || pin2.Generation != pin.Generation {
		t.Fatalf("no-op: hist=%v gen=%d err=%v", hist2, pin2.Generation, err)
	}
	// Stale base -> base sentinel; stale draft -> draft sentinel.
	if _, _, err := s.PublishReviewedApplication(ctx, "ws_1", a.ID, 1, "bogus", rel, u.ID, "", time.Now()); !errors.Is(err, store.ErrApplicationBaseConflict) {
		t.Fatalf("stale base: %v", err)
	}
	if _, _, err := s.PublishReviewedApplication(ctx, "ws_1", a.ID, 99, rel.ID, rel, u.ID, "", time.Now()); !errors.Is(err, store.ErrApplicationDraftConflict) {
		t.Fatalf("stale draft: %v", err)
	}
	// Same draft, changed content -> seq 2 + generation bump.
	rel2 := mkRel("def main(i): return 2")
	pin3, hist3, err := s.PublishReviewedApplication(ctx, "ws_1", a.ID, 1, rel.ID, rel2, u.ID, "v2", time.Now())
	if err != nil || hist3 == nil || hist3.Seq != 2 || pin3.Generation != pin.Generation+1 {
		t.Fatalf("second publish: %v %v", hist3, err)
	}
	// History: oldest first, workspace-isolated, tampered candidate rejected.
	rels, err := s.ListApplicationReleases(ctx, "ws_1", a.ID)
	if err != nil || len(rels) != 2 || rels[0].Seq != 1 || rels[1].Seq != 2 {
		t.Fatalf("history: %v %v", rels, err)
	}
	if _, err := s.GetApplicationRelease(ctx, "ws_1", a.ID, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetApplicationRelease(ctx, "other", a.ID, 1); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ws isolation: %v", err)
	}
	forged := *rel2
	forged.Spec = json.RawMessage(`{"ttl_seconds":1}`)
	if _, _, err := s.PublishReviewedApplication(ctx, "ws_1", a.ID, 1, rel2.ID, &forged, u.ID, "", time.Now()); !errors.Is(err, store.ErrInvalidServingRecord) {
		t.Fatalf("tampered accepted: %v", err)
	}
	// Concurrent same-base publishes: exactly one writes history.
	var wg sync.WaitGroup
	one, two := make(chan error, 1), make(chan error, 1)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _, e := s.PublishReviewedApplication(ctx, "ws_1", a.ID, 1, rel2.ID, rel, u.ID, "race", time.Now())
		one <- e
	}()
	go func() {
		defer wg.Done()
		_, _, e := s.PublishReviewedApplication(ctx, "ws_1", a.ID, 1, rel2.ID, rel, u.ID, "race", time.Now())
		two <- e
	}()
	wg.Wait()
	e1, e2 := <-one, <-two
	if (e1 == nil) == (e2 == nil) {
		t.Fatalf("both raced publishes got same outcome: %v %v", e1, e2)
	}
	if e1 != nil && !errors.Is(e1, store.ErrApplicationBaseConflict) {
		t.Fatalf("loser error %v", e1)
	}
	if e2 != nil && !errors.Is(e2, store.ErrApplicationBaseConflict) {
		t.Fatalf("loser error %v", e2)
	}
	if rels, err := s.ListApplicationReleases(ctx, "ws_1", a.ID); err != nil || len(rels) != 3 {
		t.Fatalf("race produced %d history rows", len(rels))
	}

	// Identical candidate republish does not bump generation even when
	// the persistent instance is disabled by delete + republish.
	// (DeleteApplication soft-deletes the app too, so re-create an app
	// publishing the same release, then soft-delete it.)
	a2, err := store.NewApplication("ws_1", u.ID, "svc2", "", json.RawMessage(`{"ttl_seconds":300}`), nil, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateApplication(ctx, a2); err != nil {
		t.Fatal(err)
	}
	p1, _, err := s.PublishReviewedApplication(ctx, "ws_1", a2.ID, 1, "", rel, u.ID, "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// Disable the persistent instance, then identical republish must
	// stay disabled + same generation + no history row.
	if ok, err := s.SetPersistentEnabled(ctx, "ws_1", p1.ID, p1.Generation, false, time.Now()); err != nil || !ok {
		t.Fatalf("disable: %v %v", ok, err)
	}
	disabled, err := s.GetPersistentInstance(ctx, "ws_1", p1.ID)
	if err != nil || disabled.Enabled {
		t.Fatalf("disable did not stick: %+v err=%v", disabled, err)
	}
	p2, h2, err := s.PublishReviewedApplication(ctx, "ws_1", a2.ID, 1, rel.ID, rel, u.ID, "", time.Now())
	if err != nil || h2 != nil || p2.Generation != disabled.Generation || p2.Enabled {
		t.Fatalf("disabled no-op: %+v hist=%v err=%v", p2, h2, err)
	}

	// Soft delete retains history.
	if ok, err := s.DeleteApplication(ctx, "ws_1", a.ID, a.Version, time.Now()); err != nil || !ok {
		t.Fatalf("delete: %v %v", ok, err)
	}
	if rels, err := s.ListApplicationReleases(ctx, "ws_1", a.ID); err != nil || len(rels) != 3 {
		t.Fatalf("history lost: %v %v", rels, err)
	}

}
