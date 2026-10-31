package serving

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"calcside/cmd/calcside/internal/scheduler"
	"calcside/internal/runtime"
	"calcside/internal/store"
	"calcside/internal/store/storetest"
	"calcside/internal/types"

	_ "calcside/cmd/calcside/internal/gormstore"
)

type fakeNow struct{ t atomic.Int64 }

// t holds a unix nanosecond timestamp.
func (f *fakeNow) Now() time.Time          { return time.Unix(0, f.t.Load()).UTC() }
func (f *fakeNow) Advance(d time.Duration) { f.t.Add(int64(d)) }

type fakeSecrets struct {
	calls atomic.Int64
	out   []runtime.Secret
}

func (f *fakeSecrets) Resolve(ctx context.Context, ws string, in map[string]runtime.SecretSpec) ([]runtime.Secret, map[string]runtime.SecretSpec, error) {
	f.calls.Add(1)
	return f.out, nil, nil
}

func newSvc(t *testing.T, clock *fakeNow) (*Service, store.Store, *fakeSecrets) {
	t.Helper()
	dsn := storetest.SQLite(t)
	st, err := store.Open(context.Background(), types.DriverSQLite, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	sec := &fakeSecrets{}
	sch, err := scheduler.New(scheduler.Options{
		Store: st, Nodes: map[string]string{"n1": "addr1", "n2": "addr2"},
		Now: clock.Now, LeaseDuration: 10 * time.Second, NodeTTL: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := New(Options{
		Store: st, Secrets: sec, Scheduler: sch,
		GlobalPolicies: map[string]string{"g": "package g"},
		Now:            clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc, st, sec
}

var t0 = time.Now().UTC().Truncate(time.Second)

func svcRelease(t *testing.T, st store.Store, secrets bool) *store.ServingRelease {
	spec := `{"capabilities":{"fs":{}}}`
	if secrets {
		spec = `{"capabilities":{"fs":{}},"secrets":{"T":{"source":"vault","ref":"v/t"}}}`
	}
	r, err := store.NewServingRelease("ws_1", "usr_1", json.RawMessage(spec), runtime.PolicyBundle{
		Global: map[string]string{"rel": "package rel"},
		User:   map[string]string{"u": "package u"},
	}, runtime.ProgramBundle{
		Sources:     map[string][]byte{"main.star": []byte(`def main(i): return {}`)},
		InputSchema: json.RawMessage(`{}`), OutputSchema: json.RawMessage(`{}`),
	}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateServingRelease(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	return r
}

func seedUser(t *testing.T, st store.Store) *store.User {
	u, err := st.UpsertUserByEmail(context.Background(), "u@x.com", "U", "sub")
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func seedInst(t *testing.T, st store.Store, rel *store.ServingRelease, u *store.User) *store.PersistentInstance {
	inst := &store.PersistentInstance{WorkspaceID: "ws_1", UserID: u.ID, ReleaseID: rel.ID,
		Enabled: true, CreatedAt: t0, UpdatedAt: t0}
	if err := st.CreatePersistentInstance(context.Background(), inst); err != nil {
		t.Fatal(err)
	}
	return inst
}

func reg(t *testing.T, svc *Service, node, boot string, cap int) *runtime.NodeIdentity {
	r, err := svc.RegisterNode(context.Background(), &runtime.NodeRegisterRequest{
		NodeID: node, BootID: boot, Capacity: cap})
	if err != nil {
		t.Fatal(err)
	}
	return &r.Node
}

func TestRegisterAndReconcile(t *testing.T) {
	clk := &fakeNow{}
	clk.t.Store(t0.UnixNano())
	svc, st, _ := newSvc(t, clk)
	u := seedUser(t, st)
	rel := svcRelease(t, st, false)
	inst := seedInst(t, st, rel, u)

	id := reg(t, svc, "n1", "b1", 4)
	if id.Incarnation != 1 {
		t.Fatalf("incarnation %d", id.Incarnation)
	}
	// One replica was claimed on registration.
	cur, err := st.GetCurrentServingReplica(context.Background(), inst.ID)
	if err != nil || cur.State != store.ServingReplicaPreparing || cur.Epoch != 1 {
		t.Fatalf("claim: %v %+v", err, cur)
	}
	// Idempotent re-register, same incarnation.
	id2 := reg(t, svc, "n1", "b1", 4)
	if id2.Incarnation != 1 || id2.BootID != "b1" {
		t.Fatalf("re-register: %+v", id2)
	}
	// Old boot rejected after a new one takes over.
	reg(t, svc, "n1", "b2", 4)
	if _, err := svc.RegisterNode(context.Background(), &runtime.NodeRegisterRequest{
		NodeID: "n1", BootID: "b1", Capacity: 4}); !errors.Is(err, runtime.ErrStaleEpoch) {
		t.Fatalf("old boot: %v", err)
	}
	// Unknown node rejected.
	if _, err := svc.RegisterNode(context.Background(), &runtime.NodeRegisterRequest{
		NodeID: "ghost", BootID: "b", Capacity: 4}); err == nil {
		t.Fatal("unknown node accepted")
	}
}

func TestPrepareAndSync(t *testing.T) {
	clk := &fakeNow{}
	clk.t.Store(t0.UnixNano())
	svc, st, sec := newSvc(t, clk)
	u := seedUser(t, st)
	sec.out = []runtime.Secret{{Name: "T", Value: "v", Source: types.SecretVault}}
	rel := svcRelease(t, st, true)
	inst := seedInst(t, st, rel, u)
	id := reg(t, svc, "n1", "b1", 4)
	cur, err := st.GetCurrentServingReplica(context.Background(), inst.ID)
	if err != nil {
		t.Fatal(err)
	}
	// PrepareReplica returns the full self-contained payload.
	resp, err := svc.PrepareReplica(context.Background(), &runtime.NodePrepareRequest{
		Node: *id,
		Assignment: runtime.ReplicaAssignment{
			ReplicaID: cur.ID, InstanceID: inst.ID, ReleaseID: rel.ID,
			Generation: inst.Generation, Epoch: cur.Epoch,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	preq := resp.Request
	if preq.InstanceID != cur.ID || preq.ReleaseID != rel.ID ||
		preq.Generation != inst.Generation || preq.Epoch != cur.Epoch ||
		preq.Owner.UserID != u.ID || len(preq.Secrets) != 1 ||
		preq.Policies.Global["server/g"] != "package g" ||
		preq.Policies.Global["release/rel"] != "package rel" ||
		preq.Policies.User["u"] != "package u" {
		t.Fatalf("payload: %+v", preq)
	}
	if sec.calls.Load() != 1 {
		t.Fatalf("resolver calls %d", sec.calls.Load())
	}
	// A stale-boot prepare rejects.
	if _, err := svc.PrepareReplica(context.Background(), &runtime.NodePrepareRequest{
		Node: runtime.NodeIdentity{NodeID: "n1", BootID: "old", Incarnation: 1},
		Assignment: runtime.ReplicaAssignment{ReplicaID: cur.ID, InstanceID: inst.ID,
			ReleaseID: rel.ID, Generation: inst.Generation, Epoch: cur.Epoch},
	}); !errors.Is(err, runtime.ErrStaleEpoch) {
		t.Fatalf("stale prepare: %v", err)
	}
	// Report ready via sync: audit lands, active pointer set.
	report := runtime.ReplicaReport{
		ReportID: "r1",
		Assignment: runtime.ReplicaAssignment{
			ReplicaID: cur.ID, InstanceID: inst.ID, ReleaseID: rel.ID,
			Generation: inst.Generation, Epoch: cur.Epoch,
		},
		State: store.ServingReplicaReady,
		Audit: runtime.AuditBatch{Events: []runtime.AuditEvent{{
			Ts: t0, UserID: "spoof", InstanceID: "spoof",
			Capability: types.CapFS, Op: "write",
			Phase: types.PhaseAfter, Decision: types.DecisionAllow,
		}}},
	}
	sync, err := svc.SyncNode(context.Background(), &runtime.NodeSyncRequest{
		Node: *id, Capacity: 4, Reports: []runtime.ReplicaReport{report},
		Inventory: []runtime.ReplicaAssignment{report.Assignment},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sync.AcknowledgedReports) != 1 || sync.AcknowledgedReports[0] != "r1" {
		t.Fatalf("ack: %+v", sync.AcknowledgedReports)
	}
	got, _ := st.GetPersistentInstance(context.Background(), "ws_1", inst.ID)
	if got.ActiveReplicaID != cur.ID {
		t.Fatalf("active: %+v", got)
	}
	evs, err := st.ListAuditEvents(context.Background(), store.AuditFilter{WorkspaceID: "ws_1"})
	if err != nil || len(evs) != 1 {
		t.Fatalf("audit: %v %d", err, len(evs))
	}
	if evs[0].UserID != u.ID || evs[0].InstanceID != inst.ID || evs[0].WorkspaceID != "ws_1" {
		t.Fatalf("audit identity not forced: %+v", evs[0])
	}
	// Retried report with the same id dedups events.
	if _, err := svc.SyncNode(context.Background(), &runtime.NodeSyncRequest{
		Node: *id, Capacity: 4, Reports: []runtime.ReplicaReport{report},
		Inventory: []runtime.ReplicaAssignment{report.Assignment},
	}); err != nil {
		t.Fatal(err)
	}
	evs, _ = st.ListAuditEvents(context.Background(), store.AuditFilter{WorkspaceID: "ws_1"})
	if len(evs) != 1 {
		t.Fatalf("dup audit inserted: %d", len(evs))
	}
	// Sync from a stale boot is refused outright.
	if _, err := svc.SyncNode(context.Background(), &runtime.NodeSyncRequest{
		Node: runtime.NodeIdentity{NodeID: "n1", BootID: "old", Incarnation: 1}, Capacity: 4,
	}); !errors.Is(err, runtime.ErrStaleEpoch) {
		t.Fatalf("stale sync: %v", err)
	}
}

func TestBackoffAndHandover(t *testing.T) {
	clk := &fakeNow{}
	clk.t.Store(t0.UnixNano())
	svc, st, _ := newSvc(t, clk)
	u := seedUser(t, st)
	rel := svcRelease(t, st, false)
	inst := seedInst(t, st, rel, u)
	id := reg(t, svc, "n1", "b1", 4)
	cur, _ := st.GetCurrentServingReplica(context.Background(), inst.ID)
	// Failed prepare report: audit kept, backoff set.
	rep := runtime.ReplicaReport{
		ReportID: "f1",
		Assignment: runtime.ReplicaAssignment{ReplicaID: cur.ID, InstanceID: inst.ID,
			ReleaseID: rel.ID, Generation: inst.Generation, Epoch: cur.Epoch},
		State: store.ServingReplicaFailed, ErrorCode: "invalid_program",
		Audit: runtime.AuditBatch{Events: []runtime.AuditEvent{{
			Ts: t0, Capability: types.CapFS, Op: "write",
			Phase: types.PhaseAfter, Decision: types.DecisionDeny,
		}}},
	}
	if _, err := svc.SyncNode(context.Background(), &runtime.NodeSyncRequest{
		Node: *id, Capacity: 4, Reports: []runtime.ReplicaReport{rep},
	}); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetPersistentInstance(context.Background(), "ws_1", inst.ID)
	if got.ConsecutiveFailures != 1 || got.RetryAfter == nil || got.RetryAfter.Before(clk.Now().Add(4*time.Second)) {
		t.Fatalf("backoff: %+v", got)
	}
	// Reclaim blocked until retry-after.
	if err := svc.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	cur2, _ := st.GetCurrentServingReplica(context.Background(), inst.ID)
	if cur2.ID != cur.ID {
		t.Fatal("claimed during backoff")
	}
	clk.Advance(6 * time.Second)
	// The old lease has also expired; a fresh claim succeeds.
	if err := svc.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	cur3, err := st.GetCurrentServingReplica(context.Background(), inst.ID)
	if err != nil || cur3.ID == cur.ID || cur3.Epoch != cur.Epoch+1 {
		t.Fatalf("fresh claim: %v %+v", err, cur3)
	}
}
