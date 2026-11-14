package serving

import (
	"context"
	"errors"
	"testing"
	"time"

	"calcside/internal/runtime"
	"calcside/internal/store"
	"calcside/internal/types"
)

// audit_only delivers events without shortening the lease or moving
// the replica out of preparing.
func TestAuditOnlyReport(t *testing.T) {
	clk := &fakeNow{}
	clk.t.Store(t0.UnixNano())
	svc, st, _ := newSvc(t, clk)
	u := seedUser(t, st)
	rel := svcRelease(t, st, false)
	inst := seedInst(t, st, rel, u)
	id := reg(t, svc, "n1", "b1", 4)
	cur, err := st.GetCurrentServingReplica(context.Background(), inst.ID)
	if err != nil {
		t.Fatal(err)
	}
	rep := runtime.ReplicaReport{
		ReportID: "ao1",
		Assignment: runtime.ReplicaAssignment{
			ReplicaID: cur.ID, InstanceID: inst.ID, ReleaseID: rel.ID,
			Generation: inst.Generation, Epoch: cur.Epoch,
		},
		State: runtime.NodeReplicaAuditOnly,
		Audit: runtime.AuditBatch{Events: []runtime.AuditEvent{{
			Ts: t0, Capability: types.CapFS, Op: "write",
			Phase: types.PhaseBefore, Decision: types.DecisionDeny,
		}}},
	}
	sync, err := svc.SyncNode(context.Background(), &runtime.NodeSyncRequest{
		Node: *id, Capacity: 4, Reports: []runtime.ReplicaReport{rep},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sync.AcknowledgedReports) != 1 {
		t.Fatalf("ack: %+v", sync.AcknowledgedReports)
	}
	row, _ := st.GetServingReplica(context.Background(), cur.ID)
	if row.State != store.ServingReplicaPreparing ||
		!row.LeaseUntil.Equal(cur.LeaseUntil) || row.Epoch != cur.Epoch {
		t.Fatalf("audit_only mutated replica: %+v", row)
	}
	evs, _ := st.ListAuditEvents(context.Background(), store.AuditFilter{WorkspaceID: "ws_1"})
	if len(evs) != 1 {
		t.Fatalf("audit: %d", len(evs))
	}
	// Retry with the same report id dedups the insert.
	if _, err := svc.SyncNode(context.Background(), &runtime.NodeSyncRequest{
		Node: *id, Capacity: 4, Reports: []runtime.ReplicaReport{rep},
	}); err != nil {
		t.Fatal(err)
	}
	evs, _ = st.ListAuditEvents(context.Background(), store.AuditFilter{WorkspaceID: "ws_1"})
	if len(evs) != 1 {
		t.Fatalf("dedup: %d", len(evs))
	}
}

// A forged inventory tuple aborts the whole sync — no valid neighbour
// is partially renewed.
func TestMixedInventoryForged(t *testing.T) {
	clk := &fakeNow{}
	clk.t.Store(t0.UnixNano())
	svc, st, _ := newSvc(t, clk)
	u := seedUser(t, st)
	rel := svcRelease(t, st, false)
	instA := seedInst(t, st, rel, u)
	instB := &store.PersistentInstance{WorkspaceID: "ws_1", UserID: u.ID,
		ReleaseID: rel.ID, Enabled: true, CreatedAt: t0, UpdatedAt: t0}
	if err := st.CreatePersistentInstance(context.Background(), instB); err != nil {
		t.Fatal(err)
	}
	id := reg(t, svc, "n1", "b1", 4) // registers + reconciles both
	repA, _ := st.GetCurrentServingReplica(context.Background(), instA.ID)
	repB, _ := st.GetCurrentServingReplica(context.Background(), instB.ID)
	clk.Advance(time.Second)
	// inv A valid, inv B forged epoch: the whole sync must reject.
	_, err := svc.SyncNode(context.Background(), &runtime.NodeSyncRequest{
		Node: *id, Capacity: 4,
		Inventory: []runtime.ReplicaAssignment{
			{ReplicaID: repA.ID, InstanceID: instA.ID, ReleaseID: rel.ID,
				Generation: instA.Generation, Epoch: repA.Epoch},
			{ReplicaID: repB.ID, InstanceID: instB.ID, ReleaseID: rel.ID,
				Generation: instB.Generation, Epoch: repB.Epoch + 99},
		},
	})
	if !errors.Is(err, runtime.ErrStaleEpoch) {
		t.Fatalf("forged inventory: %v", err)
	}
	a2, _ := st.GetServingReplica(context.Background(), repA.ID)
	if a2.LeaseUntil.After(repA.LeaseUntil.Add(time.Millisecond)) {
		t.Fatal("valid neighbour partially renewed through forged batch")
	}
	// Owner forgery: real tuple, wrong claimed owner.
	clk.Advance(0)
	_, err = svc.SyncNode(context.Background(), &runtime.NodeSyncRequest{
		Node: *id, Capacity: 4,
		Inventory: []runtime.ReplicaAssignment{{
			ReplicaID: repA.ID, InstanceID: instA.ID, ReleaseID: rel.ID,
			Generation: instA.Generation, Epoch: repA.Epoch,
			Owner: runtime.Owner{UserID: "usr_attacker"},
		}},
	})
	if !errors.Is(err, runtime.ErrStaleEpoch) {
		t.Fatalf("owner forgery: %v", err)
	}
}
