package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"calcside/internal/runtime"
	"calcside/internal/store"
)

var sNow = time.Now().UTC().Truncate(time.Second)

func sSpec() json.RawMessage {
	return json.RawMessage(`{"ttl_seconds":300,"capabilities":{"fs":{}},"secrets":{"T":{"source":"vault","ref":"v/t"}}}`)
}

func sBundle() runtime.ProgramBundle {
	return runtime.ProgramBundle{
		Sources:      map[string][]byte{"main.star": []byte(`def main(i): return {}`)},
		InputSchema:  json.RawMessage(`{"type":"object"}`),
		OutputSchema: json.RawMessage(`{"type":"object"}`),
	}
}

func mkRelease(t *testing.T, ws string) *store.ServingRelease {
	r, err := store.NewServingRelease(ws, "usr_1", sSpec(), runtime.PolicyBundle{}, sBundle(), sNow)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

var seedN atomic.Int64

// seed creates the shared release (content dedups — ErrConflict on a
// second call is expected) and one fresh persistent instance.
func seed(t *testing.T, s store.Store) (rel *store.ServingRelease, inst *store.PersistentInstance) {
	ctx := context.Background()
	rel = mkRelease(t, "ws_1")
	if err := s.CreateServingRelease(ctx, rel); err != nil && !errors.Is(err, store.ErrConflict) {
		t.Fatal(err)
	}
	inst = &store.PersistentInstance{ID: fmt.Sprintf("pin_seed%d", seedN.Add(1)), WorkspaceID: "ws_1",
		UserID: "usr_1", ReleaseID: rel.ID, Enabled: true, CreatedAt: sNow, UpdatedAt: sNow}
	if err := s.CreatePersistentInstance(ctx, inst); err != nil {
		t.Fatal(err)
	}
	return rel, inst
}

func node(id, boot string, inc int64) *store.ServingNode {
	return &store.ServingNode{NodeID: id, BootID: boot, Addr: "a:1", Incarnation: inc,
		Capacity: 4, HeartbeatUntil: sNow.Add(time.Hour)}
}

func serving(t *testing.T, s store.Store) {
	ctx := context.Background()

	t.Run("release_immutable", func(t *testing.T) {
		rel := mkRelease(t, "ws_1")
		// Mutation of caller slices after construction cannot leak in.
		if err := s.CreateServingRelease(ctx, rel); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetServingRelease(ctx, "ws_1", rel.ID)
		if err != nil || got.ID != rel.ID {
			t.Fatalf("get: %v", err)
		}
		// Same content dedups to the same id; duplicate row conflicts.
		dup, _ := store.NewServingRelease("ws_1", "usr_1", sSpec(), runtime.PolicyBundle{}, sBundle(), sNow.Add(time.Hour))
		if dup.ID != rel.ID {
			t.Fatal("content address unstable")
		}
		if err := s.CreateServingRelease(ctx, dup); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("dup: %v", err)
		}
		// Wrong workspace is invisible.
		if _, err := s.GetServingRelease(ctx, "ws_2", rel.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("ws isolation: %v", err)
		}
		// Forged id rejected on create.
		bad := *rel
		bad.ID = "rel_" + string(make([]byte, 64))
		if err := s.CreateServingRelease(ctx, &bad); !errors.Is(err, store.ErrInvalidServingRecord) {
			t.Fatalf("forged id: %v", err)
		}
	})

	t.Run("release_validation", func(t *testing.T) {
		// Inline plaintext rejected (map-shaped secrets).
		if _, err := store.NewServingRelease("ws_1", "usr_1",
			json.RawMessage(`{"secrets":{"T":{"source":"vault","ref":"v/t","value":"plain"}}}`),
			runtime.PolicyBundle{}, sBundle(), sNow); !errors.Is(err, store.ErrInvalidServingRecord) {
			t.Fatalf("plaintext: %v", err)
		}
		// Extension snapshot rejected.
		if _, err := store.NewServingRelease("ws_1", "usr_1",
			json.RawMessage(`{"capabilities":{"ext":{"local/x":"h1:abc"}}}`),
			runtime.PolicyBundle{}, sBundle(), sNow); !errors.Is(err, store.ErrInvalidServingRecord) {
			t.Fatalf("ext: %v", err)
		}
	})

	t.Run("instance", func(t *testing.T) {
		rel, inst := seed(t, s)
		got, err := s.GetPersistentInstance(ctx, "ws_1", inst.ID)
		if err != nil || got.Generation != 1 || got.LastEpoch != 0 || got.ActiveReplicaID != "" {
			t.Fatalf("initial: %v %+v", err, got)
		}
		// Cross-workspace release rejected.
		rel2 := mkRelease(t, "ws_2")
		if err := s.CreateServingRelease(ctx, rel2); err != nil {
			t.Fatal(err)
		}
		if err := s.CreatePersistentInstance(ctx, &store.PersistentInstance{ID: "pin_x", WorkspaceID: "ws_1",
			UserID: "usr_1", ReleaseID: rel2.ID}); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("cross-ws release: %v", err)
		}
		// Enable/disable CAS.
		if ok, err := s.SetPersistentEnabled(ctx, "ws_1", inst.ID, 1, false, sNow); err != nil || !ok {
			t.Fatalf("disable: %v %v", ok, err)
		}
		got, _ = s.GetPersistentInstance(ctx, "ws_1", inst.ID)
		if got.Enabled || got.Generation != 2 {
			t.Fatalf("disable gen: %+v", got)
		}
		// Same-call idempotent no bump.
		if ok, err := s.SetPersistentEnabled(ctx, "ws_1", inst.ID, 2, false, sNow); !ok || err != nil {
			t.Fatalf("idem: %v %v", ok, err)
		}
		got, _ = s.GetPersistentInstance(ctx, "ws_1", inst.ID)
		if got.Generation != 2 {
			t.Fatalf("idem bumped: %+v", got)
		}
		// Stale CAS fails.
		if ok, err := s.SetPersistentEnabled(ctx, "ws_1", inst.ID, 1, true, sNow); ok || err != nil {
			t.Fatalf("stale CAS: %v %v", ok, err)
		}
		if ok, err := s.SetPersistentEnabled(ctx, "ws_1", inst.ID, 2, true, sNow); !ok || err != nil {
			t.Fatalf("reenable: %v %v", ok, err)
		}
		got, _ = s.GetPersistentInstance(ctx, "ws_1", inst.ID)
		if !got.Enabled || got.Generation != 3 || got.ActiveReplicaID != "" {
			t.Fatalf("reenable: %+v", got)
		}
		_ = rel
	})

	t.Run("pagination", func(t *testing.T) {
		if _, err := s.ListPersistentInstances(ctx, "", 0); !errors.Is(err, store.ErrInvalidServingRecord) {
			t.Fatalf("limit 0: %v", err)
		}
		if _, err := s.ListPersistentInstances(ctx, "", 201); !errors.Is(err, store.ErrInvalidServingRecord) {
			t.Fatalf("limit 201: %v", err)
		}
		list, err := s.ListPersistentInstances(ctx, "", 10)
		if err != nil || len(list) == 0 {
			t.Fatalf("list: %v", err)
		}
		page, err := s.ListPersistentInstances(ctx, list[len(list)-1].ID, 10)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range page {
			if p.ID <= list[len(list)-1].ID {
				t.Fatal("pagination overlap")
			}
		}
	})

	t.Run("nodes", func(t *testing.T) {
		n, err := s.RegisterServingNode(ctx, node("n1", "b1", 0), sNow)
		if err != nil || n.Incarnation != 1 {
			t.Fatalf("register: %v %+v", err, n)
		}
		// Idempotent same boot.
		n2, err := s.RegisterServingNode(ctx, node("n1", "b1", 0), sNow.Add(time.Minute))
		if err != nil || n2.Incarnation != 1 {
			t.Fatalf("same boot: %v %+v", err, n2)
		}
		// New boot bumps incarnation.
		n3, err := s.RegisterServingNode(ctx, node("n1", "b2", 0), sNow)
		if err != nil || n3.Incarnation != 2 || n3.BootID != "b2" {
			t.Fatalf("new boot: %v %+v", err, n3)
		}
		// Old boot never returns.
		if _, err := s.RegisterServingNode(ctx, node("n1", "b1", 0), sNow); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("old boot: %v", err)
		}
		// Heartbeat only for the current tuple.
		f := store.ServingNodeFence{NodeID: "n1", BootID: "b2", Incarnation: 2}
		if ok, err := s.HeartbeatServingNode(ctx, f, sNow, sNow.Add(time.Hour), 8); !ok || err != nil {
			t.Fatalf("hb: %v %v", ok, err)
		}
		stale := store.ServingNodeFence{NodeID: "n1", BootID: "b1", Incarnation: 1}
		if ok, err := s.HeartbeatServingNode(ctx, stale, sNow, sNow.Add(time.Hour), 8); ok || err != nil {
			t.Fatalf("stale hb: %v %v", ok, err)
		}
		// Capacity bounds.
		if _, err := s.RegisterServingNode(ctx, &store.ServingNode{NodeID: "nx", BootID: "b", Addr: "a", Capacity: 0,
			HeartbeatUntil: sNow.Add(time.Hour)}, sNow); !errors.Is(err, store.ErrInvalidServingRecord) {
			t.Fatalf("cap 0: %v", err)
		}
		// List filters expired.
		dead := node("n2", "b", 0)
		dead.HeartbeatUntil = sNow.Add(time.Hour)
		if _, err := s.RegisterServingNode(ctx, dead, sNow); err != nil {
			t.Fatal(err)
		}
		live, err := s.ListServingNodes(ctx, sNow.Add(2*time.Hour))
		if err != nil || len(live) != 0 {
			t.Fatalf("live: %v %d", err, len(live))
		}
	})

	t.Run("replicas", func(t *testing.T) {
		_, inst := seed(t, s)
		n, err := s.RegisterServingNode(ctx, node("nr", "br", 0), sNow)
		if err != nil {
			t.Fatal(err)
		}
		claim := store.ServingReplicaClaim{
			InstanceID: inst.ID, ReleaseID: inst.ReleaseID, NodeID: n.NodeID, BootID: n.BootID,
			ReplicaID: "rep_c1", NodeIncarnation: n.Incarnation, Generation: inst.Generation,
			ExpectedEpoch: 0, Now: sNow, LeaseUntil: sNow.Add(time.Minute),
		}
		rep, err := s.ClaimServingReplica(ctx, claim)
		if err != nil || rep.Epoch != 1 || rep.State != store.ServingReplicaPreparing {
			t.Fatalf("claim: %v %+v", err, rep)
		}
		// A second claim while the first lease is live fails.
		claim.ReplicaID = "rep_c2"
		if _, err := s.ClaimServingReplica(ctx, claim); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("dup claim: %v", err)
		}
		// Ready report sets the active pointer.
		f := store.ServingReplicaFence{ReplicaID: rep.ID, InstanceID: inst.ID, ReleaseID: inst.ReleaseID,
			NodeID: n.NodeID, BootID: n.BootID, NodeIncarnation: n.Incarnation,
			Generation: inst.Generation, Epoch: rep.Epoch}
		if ok, err := s.ReportServingReplica(ctx, f, store.ServingReplicaReady, "", sNow); !ok || err != nil {
			t.Fatalf("ready: %v %v", ok, err)
		}
		got, _ := s.GetPersistentInstance(ctx, "ws_1", inst.ID)
		if got.ActiveReplicaID != rep.ID {
			t.Fatalf("active: %+v", got)
		}
		// Renew extends within the node heartbeat.
		if ok, err := s.RenewServingReplica(ctx, f, sNow, sNow.Add(30*time.Minute)); !ok || err != nil {
			t.Fatalf("renew: %v %v", ok, err)
		}
		// Stale node fence renew fails.
		bad := f
		bad.BootID = "old"
		if ok, err := s.RenewServingReplica(ctx, bad, sNow, sNow.Add(time.Minute)); ok || err != nil {
			t.Fatalf("stale renew: %v %v", ok, err)
		}
		// Failed report ends the lease and clears active.
		if ok, err := s.ReportServingReplica(ctx, f, store.ServingReplicaFailed, "runtime", sNow); !ok || err != nil {
			t.Fatalf("failed: %v %v", ok, err)
		}
		got, _ = s.GetPersistentInstance(ctx, "ws_1", inst.ID)
		if got.ActiveReplicaID != "" {
			t.Fatalf("active not cleared: %+v", got)
		}
		r, _ := s.GetServingReplica(ctx, rep.ID)
		if r.State != store.ServingReplicaFailed || !r.LeaseUntil.Equal(sNow) {
			t.Fatalf("row: %+v", r)
		}
		// Terminal row: cannot renew or report again.
		if ok, _ := s.RenewServingReplica(ctx, f, sNow, sNow.Add(time.Minute)); ok {
			t.Fatal("renewed terminal")
		}
		// Fresh claim gets the next epoch and a fresh id — after the
		// persisted backoff the failure report set.
		claim.ReplicaID = "rep_c3"
		claim.ExpectedEpoch = 1
		claim.Now = sNow.Add(10 * time.Second)
		claim.LeaseUntil = sNow.Add(10*time.Second + time.Minute)
		rep2, err := s.ClaimServingReplica(ctx, claim)
		if err != nil || rep2.Epoch != 2 {
			t.Fatalf("reclaim: %v %+v", err, rep2)
		}
		_ = rep2
	})

	t.Run("claim_concurrency", func(t *testing.T) {
		_, inst := seed(t, s)
		n1, _ := s.RegisterServingNode(ctx, node("ra1", "b", 0), sNow)
		n2, _ := s.RegisterServingNode(ctx, node("ra2", "b", 0), sNow)
		var wg sync.WaitGroup
		var wins, conflicts int64
		var mu sync.Mutex
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				n := n1
				if i%2 == 1 {
					n = n2
				}
				_, err := s.ClaimServingReplica(ctx, store.ServingReplicaClaim{
					InstanceID: inst.ID, ReleaseID: inst.ReleaseID,
					NodeID: n.NodeID, BootID: n.BootID, ReplicaID: store.NewID(store.PrefixServingReplica),
					NodeIncarnation: n.Incarnation, Generation: inst.Generation,
					ExpectedEpoch: 0, Now: sNow, LeaseUntil: sNow.Add(time.Minute),
				})
				mu.Lock()
				if err == nil {
					wins++
				} else if errors.Is(err, store.ErrConflict) {
					conflicts++
				}
				mu.Unlock()
			}(i)
		}
		wg.Wait()
		if wins != 1 || wins+conflicts != 8 {
			t.Fatalf("wins=%d conflicts=%d", wins, conflicts)
		}
	})

	t.Run("list_node_replicas", func(t *testing.T) {
		_, inst := seed(t, s)
		n, _ := s.RegisterServingNode(ctx, node("ln", "lb", 0), sNow)
		f := store.ServingNodeFence{NodeID: n.NodeID, BootID: n.BootID, Incarnation: n.Incarnation}
		if _, err := s.ListNodeServingReplicas(ctx, store.ServingNodeFence{NodeID: n.NodeID, BootID: "x"}, sNow); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("stale fence list: %v", err)
		}
		rep, err := s.ClaimServingReplica(ctx, store.ServingReplicaClaim{
			InstanceID: inst.ID, ReleaseID: inst.ReleaseID, NodeID: n.NodeID, BootID: n.BootID,
			ReplicaID: "rep_l1", NodeIncarnation: n.Incarnation, Generation: inst.Generation,
			ExpectedEpoch: 0, Now: sNow, LeaseUntil: sNow.Add(time.Minute),
		})
		if err != nil {
			t.Fatal(err)
		}
		list, err := s.ListNodeServingReplicas(ctx, f, sNow)
		if err != nil || len(list) != 1 || list[0].ID != rep.ID {
			t.Fatalf("list: %v %d", err, len(list))
		}
		// Disabled instance drops out of the restore set.
		if ok, _ := s.SetPersistentEnabled(ctx, "ws_1", inst.ID, inst.Generation, false, sNow); !ok {
			t.Fatal("disable")
		}
		list, _ = s.ListNodeServingReplicas(ctx, f, sNow)
		if len(list) != 0 {
			t.Fatalf("disabled listed: %d", len(list))
		}
	})
	t.Run("claim_validation", func(t *testing.T) {
		_, inst := seed(t, s)
		n, _ := s.RegisterServingNode(ctx, node("cv", "b", 0), sNow)
		base := store.ServingReplicaClaim{InstanceID: inst.ID, ReleaseID: inst.ReleaseID,
			NodeID: n.NodeID, BootID: n.BootID, ReplicaID: store.NewID(store.PrefixServingReplica),
			NodeIncarnation: n.Incarnation, Generation: inst.Generation,
			ExpectedEpoch: inst.LastEpoch, Now: sNow, LeaseUntil: sNow.Add(time.Minute)}
		for name, mut := range map[string]func(*store.ServingReplicaClaim){
			"empty_replica":  func(c *store.ServingReplicaClaim) { c.ReplicaID = "" },
			"empty_instance": func(c *store.ServingReplicaClaim) { c.InstanceID = "" },
			"zero_gen":       func(c *store.ServingReplicaClaim) { c.Generation = 0 },
			"zero_inc":       func(c *store.ServingReplicaClaim) { c.NodeIncarnation = 0 },
			"neg_epoch":      func(c *store.ServingReplicaClaim) { c.ExpectedEpoch = -1 },
			"zero_now":       func(c *store.ServingReplicaClaim) { c.Now = time.Time{} },
			"past_lease":     func(c *store.ServingReplicaClaim) { c.LeaseUntil = sNow },
		} {
			c := base
			mut(&c)
			if _, err := s.ClaimServingReplica(ctx, c); !errors.Is(err, store.ErrInvalidServingRecord) {
				t.Fatalf("%s: %v", name, err)
			}
		}
		// Stale ExpectedEpoch with no active lease still fails, no insert.
		bad := base
		bad.ExpectedEpoch = 99
		if _, err := s.ClaimServingReplica(ctx, bad); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("stale epoch: %v", err)
		}
	})

	t.Run("renew_bounds", func(t *testing.T) {
		_, inst := seed(t, s)
		n, _ := s.RegisterServingNode(ctx, node("rb", "b", 0), sNow)
		rep, err := s.ClaimServingReplica(ctx, store.ServingReplicaClaim{
			InstanceID: inst.ID, ReleaseID: inst.ReleaseID, NodeID: n.NodeID, BootID: n.BootID,
			ReplicaID: store.NewID(store.PrefixServingReplica), NodeIncarnation: n.Incarnation,
			Generation: inst.Generation, ExpectedEpoch: inst.LastEpoch,
			Now: sNow, LeaseUntil: sNow.Add(30 * time.Minute)})
		if err != nil {
			t.Fatal(err)
		}
		f := store.ServingReplicaFence{ReplicaID: rep.ID, InstanceID: inst.ID, ReleaseID: inst.ReleaseID,
			NodeID: n.NodeID, BootID: n.BootID, NodeIncarnation: n.Incarnation,
			Generation: inst.Generation, Epoch: rep.Epoch}
		// Past until rejected.
		if _, err := s.RenewServingReplica(ctx, f, sNow, sNow.Add(-time.Second)); !errors.Is(err, store.ErrInvalidServingRecord) {
			t.Fatalf("past until: %v", err)
		}
		// Beyond node heartbeat rejected, not clamped.
		if _, err := s.RenewServingReplica(ctx, f, sNow, n.HeartbeatUntil.Add(time.Minute)); !errors.Is(err, store.ErrInvalidServingRecord) {
			t.Fatalf("over-heartbeat until: %v", err)
		}
		// No extension requested -> idempotent true, expiry unchanged.
		if ok, err := s.RenewServingReplica(ctx, f, sNow, rep.LeaseUntil); !ok || err != nil {
			t.Fatalf("no-op renew: %v %v", ok, err)
		}
		got, _ := s.GetServingReplica(ctx, rep.ID)
		if !got.LeaseUntil.Equal(rep.LeaseUntil) {
			t.Fatal("no-op renew mutated expiry")
		}
		// Forged instance id cannot renew.
		bad := f
		bad.InstanceID = "pin_other"
		if ok, err := s.RenewServingReplica(ctx, bad, sNow, sNow.Add(40*time.Minute)); ok || err != nil {
			t.Fatalf("forged renew: %v %v", ok, err)
		}
	})

	t.Run("terminal_on_disabled", func(t *testing.T) {
		_, inst := seed(t, s)
		n, _ := s.RegisterServingNode(ctx, node("td", "b", 0), sNow)
		rep, err := s.ClaimServingReplica(ctx, store.ServingReplicaClaim{
			InstanceID: inst.ID, ReleaseID: inst.ReleaseID, NodeID: n.NodeID, BootID: n.BootID,
			ReplicaID: store.NewID(store.PrefixServingReplica), NodeIncarnation: n.Incarnation,
			Generation: inst.Generation, ExpectedEpoch: inst.LastEpoch,
			Now: sNow, LeaseUntil: sNow.Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		f := store.ServingReplicaFence{ReplicaID: rep.ID, InstanceID: inst.ID, ReleaseID: inst.ReleaseID,
			NodeID: n.NodeID, BootID: n.BootID, NodeIncarnation: n.Incarnation,
			Generation: inst.Generation, Epoch: rep.Epoch}
		if ok, err := s.ReportServingReplica(ctx, f, store.ServingReplicaReady, "", sNow); !ok || err != nil {
			t.Fatalf("ready: %v %v", ok, err)
		}
		// Disable bumps the generation; the truthful stop ack on the
		// old generation must still be accepted (frees the lease).
		if ok, _ := s.SetPersistentEnabled(ctx, "ws_1", inst.ID, inst.Generation, false, sNow); !ok {
			t.Fatal("disable")
		}
		if ok, err := s.ReportServingReplica(ctx, f, store.ServingReplicaStopped, "", sNow); !ok || err != nil {
			t.Fatalf("stopped on disabled: %v %v", ok, err)
		}
		got, _ := s.GetServingReplica(ctx, rep.ID)
		if got.State != store.ServingReplicaStopped || !got.LeaseUntil.Equal(sNow) {
			t.Fatalf("row: %+v", got)
		}
		// Ready can no longer be reported for the old generation.
		if ok, _ := s.ReportServingReplica(ctx, f, store.ServingReplicaReady, "", sNow); ok {
			t.Fatal("terminal accepted ready")
		}
		// Invalid error codes rejected.
		if _, err := s.ReportServingReplica(ctx, f, store.ServingReplicaFailed, "plz-leak-this", sNow); !errors.Is(err, store.ErrInvalidServingRecord) {
			t.Fatalf("bad code: %v", err)
		}
	})

	t.Run("cross_instance_fence", func(t *testing.T) {
		rel, a := seed(t, s)
		b := &store.PersistentInstance{ID: fmt.Sprintf("pin_seed%d", seedN.Add(1)), WorkspaceID: "ws_1",
			UserID: "usr_1", ReleaseID: rel.ID, Enabled: true, CreatedAt: sNow, UpdatedAt: sNow}
		if err := s.CreatePersistentInstance(ctx, b); err != nil {
			t.Fatal(err)
		}
		n, _ := s.RegisterServingNode(ctx, node("xf", "b", 0), sNow)
		rep, err := s.ClaimServingReplica(ctx, store.ServingReplicaClaim{
			InstanceID: a.ID, ReleaseID: a.ReleaseID, NodeID: n.NodeID, BootID: n.BootID,
			ReplicaID: store.NewID(store.PrefixServingReplica), NodeIncarnation: n.Incarnation,
			Generation: a.Generation, ExpectedEpoch: a.LastEpoch,
			Now: sNow, LeaseUntil: sNow.Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		// Same everything but the wrong logical id must not report.
		f := store.ServingReplicaFence{ReplicaID: rep.ID, InstanceID: b.ID, ReleaseID: a.ReleaseID,
			NodeID: n.NodeID, BootID: n.BootID, NodeIncarnation: n.Incarnation,
			Generation: a.Generation, Epoch: rep.Epoch}
		if ok, err := s.ReportServingReplica(ctx, f, store.ServingReplicaReady, "", sNow); ok || err != nil {
			t.Fatalf("cross-instance ready: %v %v", ok, err)
		}
		got, _ := s.GetPersistentInstance(ctx, "ws_1", b.ID)
		if got.ActiveReplicaID != "" {
			t.Fatal("wrong instance gained active pointer")
		}
		gotR, _ := s.GetServingReplica(ctx, rep.ID)
		if gotR.State != store.ServingReplicaPreparing {
			t.Fatal("replica mutated by forged report")
		}
	})

	t.Run("old_boot_lease", func(t *testing.T) {
		_, inst := seed(t, s)
		old, _ := s.RegisterServingNode(ctx, node("ob", "b1", 0), sNow)
		rep, err := s.ClaimServingReplica(ctx, store.ServingReplicaClaim{
			InstanceID: inst.ID, ReleaseID: inst.ReleaseID, NodeID: old.NodeID, BootID: old.BootID,
			ReplicaID: store.NewID(store.PrefixServingReplica), NodeIncarnation: old.Incarnation,
			Generation: inst.Generation, ExpectedEpoch: inst.LastEpoch,
			Now: sNow, LeaseUntil: sNow.Add(10 * time.Minute)})
		if err != nil {
			t.Fatal(err)
		}
		lease := rep.LeaseUntil
		// New boot registers; the old replica's lease is untouched.
		nb, err := s.RegisterServingNode(ctx, node("ob", "b2", 0), sNow)
		if err != nil || nb.Incarnation != 2 {
			t.Fatalf("new boot: %v %+v", err, nb)
		}
		got, _ := s.GetServingReplica(ctx, rep.ID)
		if !got.LeaseUntil.Equal(lease) {
			t.Fatal("old lease shortened by new boot")
		}
		// Old boot cannot heartbeat, renew, report or claim.
		of := store.ServingReplicaFence{ReplicaID: rep.ID, InstanceID: inst.ID, ReleaseID: inst.ReleaseID,
			NodeID: old.NodeID, BootID: old.BootID, NodeIncarnation: old.Incarnation,
			Generation: inst.Generation, Epoch: rep.Epoch}
		if ok, _ := s.RenewServingReplica(ctx, of, sNow, sNow.Add(time.Minute)); ok {
			t.Fatal("old boot renewed")
		}
		if ok, _ := s.HeartbeatServingNode(ctx,
			store.ServingNodeFence{NodeID: "ob", BootID: "b1", Incarnation: 1},
			sNow, sNow.Add(time.Hour), 4); ok {
			t.Fatal("old boot heartbeat")
		}
		if ok, _ := s.ReportServingReplica(ctx, of, store.ServingReplicaStopped, "", sNow); ok {
			t.Fatal("old boot reported")
		}
		// New boot cannot claim while the old lease is live.
		if _, err := s.ClaimServingReplica(ctx, store.ServingReplicaClaim{
			InstanceID: inst.ID, ReleaseID: inst.ReleaseID, NodeID: nb.NodeID, BootID: nb.BootID,
			ReplicaID: store.NewID(store.PrefixServingReplica), NodeIncarnation: nb.Incarnation,
			Generation: inst.Generation, ExpectedEpoch: 1,
			Now: sNow, LeaseUntil: sNow.Add(5 * time.Minute)}); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("overlapping claim: %v", err)
		}
		// After the old lease expires, the new boot claims a fresh id/epoch.
		later := lease.Add(time.Second)
		rep2, err := s.ClaimServingReplica(ctx, store.ServingReplicaClaim{
			InstanceID: inst.ID, ReleaseID: inst.ReleaseID, NodeID: nb.NodeID, BootID: nb.BootID,
			ReplicaID: store.NewID(store.PrefixServingReplica), NodeIncarnation: nb.Incarnation,
			Generation: inst.Generation, ExpectedEpoch: 1,
			Now: later, LeaseUntil: later.Add(5 * time.Minute)})
		if err != nil || rep2.Epoch != rep.Epoch+1 || rep2.ID == rep.ID {
			t.Fatalf("fresh claim: %v %+v", err, rep2)
		}
	})

	t.Run("capacity_second_instance", func(t *testing.T) {
		rel, a := seed(t, s)
		b := &store.PersistentInstance{ID: fmt.Sprintf("pin_seed%d", seedN.Add(1)), WorkspaceID: "ws_1",
			UserID: "usr_1", ReleaseID: rel.ID, Enabled: true, CreatedAt: sNow, UpdatedAt: sNow}
		if err := s.CreatePersistentInstance(ctx, b); err != nil {
			t.Fatal(err)
		}
		cap1 := &store.ServingNode{NodeID: "cap1", BootID: "b", Addr: "a", Capacity: 1,
			HeartbeatUntil: sNow.Add(time.Hour)}
		n, err := s.RegisterServingNode(ctx, cap1, sNow)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.ClaimServingReplica(ctx, store.ServingReplicaClaim{
			InstanceID: a.ID, ReleaseID: a.ReleaseID, NodeID: n.NodeID, BootID: n.BootID,
			ReplicaID: store.NewID(store.PrefixServingReplica), NodeIncarnation: n.Incarnation,
			Generation: a.Generation, ExpectedEpoch: a.LastEpoch,
			Now: sNow, LeaseUntil: sNow.Add(time.Minute)}); err != nil {
			t.Fatal(err)
		}
		// Capacity 1 blocks a claim for a DIFFERENT instance.
		bGot, _ := s.GetPersistentInstance(ctx, "ws_1", b.ID)
		if _, err := s.ClaimServingReplica(ctx, store.ServingReplicaClaim{
			InstanceID: bGot.ID, ReleaseID: bGot.ReleaseID, NodeID: n.NodeID, BootID: n.BootID,
			ReplicaID: store.NewID(store.PrefixServingReplica), NodeIncarnation: n.Incarnation,
			Generation: bGot.Generation, ExpectedEpoch: bGot.LastEpoch,
			Now: sNow, LeaseUntil: sNow.Add(time.Minute)}); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("capacity: %v", err)
		}
		bGot2, _ := s.GetPersistentInstance(ctx, "ws_1", b.ID)
		if bGot2.LastEpoch != 0 {
			t.Fatal("rejected claim bumped epoch")
		}
	})
}
