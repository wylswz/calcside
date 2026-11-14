package scheduler

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
	"calcside/internal/store/storetest"
	"calcside/internal/types"

	_ "calcside/cmd/calcside/internal/gormstore"
)

func openStore(t *testing.T) store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), types.DriverSQLite, storetest.SQLite(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// testClock is a manually advanced wall clock shared by the store
// writes and the scheduler under test.
type testClock struct{ t atomic.Int64 }

func newClock() *testClock {
	c := &testClock{}
	c.t.Store(time.Now().UTC().Truncate(time.Second).UnixNano())
	return c
}

func (c *testClock) Now() time.Time          { return time.Unix(0, c.t.Load()).UTC() }
func (c *testClock) Advance(d time.Duration) { c.t.Add(int64(d)) }

func newSched(t *testing.T, st store.Store, clk *testClock) *Scheduler {
	t.Helper()
	sch, err := New(Options{
		Store: st, Now: clk.Now,
		LeaseDuration: 10 * time.Second, NodeTTL: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return sch
}

// registerNode persists a node boot whose heartbeat lives for ttl —
// the store requires a future heartbeat, so "already dead" nodes are
// written with a nanosecond ttl and the clock advances past it.
func registerNode(t *testing.T, st store.Store, clk *testClock, id, boot, addr string, ttl time.Duration) {
	t.Helper()
	if _, err := st.RegisterServingNode(context.Background(), &store.ServingNode{
		NodeID: id, BootID: boot, Addr: addr, Capacity: 4,
		HeartbeatUntil: clk.Now().Add(ttl),
	}, clk.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestNodesMembership(t *testing.T) {
	st := openStore(t)
	clk := newClock()
	registerNode(t, st, clk, "w1", "b1", "10.0.0.1:8090", 30*time.Second)
	registerNode(t, st, clk, "w2", "b1", "w2.ns.svc:8090", 30*time.Second)
	registerNode(t, st, clk, "dead", "b1", "10.0.0.9:8090", time.Nanosecond)
	registerNode(t, st, clk, "bad", "b1", "not an addr", 30*time.Second) // malformed stored endpoint
	clk.Advance(2 * time.Nanosecond)                                     // only "dead" expires

	sch := newSched(t, st, clk)
	refs, err := sch.Nodes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 || refs[0].NodeID != "w1" || refs[1].NodeID != "w2" {
		t.Fatalf("discovery nodes: %+v", refs)
	}
	if refs[0].Addr != "10.0.0.1:8090" || refs[0].BootID != "b1" {
		t.Fatalf("ref fields: %+v", refs[0])
	}

	// A second scheduler over the same store computes the identical
	// member set — membership is DB authority, not per-process state.
	other := newSched(t, st, clk)
	refs2, err := other.Nodes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(refs2) != len(refs) {
		t.Fatalf("member sets diverge: %+v vs %+v", refs, refs2)
	}
	for i := range refs {
		if refs[i] != refs2[i] {
			t.Fatalf("member %d diverges: %+v vs %+v", i, refs[i], refs2[i])
		}
	}
}

func TestPickRoundRobinAndExpiry(t *testing.T) {
	st := openStore(t)
	clk := newClock()
	registerNode(t, st, clk, "w1", "b1", "10.0.0.1:8090", 30*time.Second)
	registerNode(t, st, clk, "w2", "b1", "10.0.0.2:8090", 30*time.Second)
	registerNode(t, st, clk, "w3", "b1", "10.0.0.3:8090", 30*time.Second)

	sch := newSched(t, st, clk)
	seen := map[string]int{}
	for i := 0; i < 6; i++ {
		n, err := sch.Pick(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		seen[n.NodeID]++
	}
	if len(seen) != 3 || seen["w1"] != 2 || seen["w2"] != 2 || seen["w3"] != 2 {
		t.Fatalf("rr over live members: %v", seen)
	}

	// Advance past every heartbeat, then renew w1/w3 only: w2 leaves
	// the pick set with no config change.
	clk.Advance(31 * time.Second)
	for _, id := range []string{"w1", "w3"} {
		ok, err := st.HeartbeatServingNode(context.Background(), store.ServingNodeFence{
			NodeID: id, BootID: "b1", Incarnation: 1,
		}, clk.Now(), clk.Now().Add(30*time.Second), 4)
		if err != nil || !ok {
			t.Fatalf("renew %s: %v %v", id, ok, err)
		}
	}
	seen = map[string]int{}
	for i := 0; i < 4; i++ {
		n, err := sch.Pick(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		seen[n.NodeID]++
	}
	if len(seen) != 2 || seen["w1"] != 2 || seen["w3"] != 2 {
		t.Fatalf("rr after expiry: %v", seen)
	}
}

func TestPickEmpty(t *testing.T) {
	st := openStore(t)
	clk := newClock()
	sch := newSched(t, st, clk)
	if _, err := sch.Pick(context.Background()); !errors.Is(err, runtime.ErrTooMany) {
		t.Fatalf("empty pick: %v", err)
	}
	registerNode(t, st, clk, "w1", "b1", "10.0.0.1:8090", time.Nanosecond)
	clk.Advance(2 * time.Nanosecond) // registered but expired
	if _, err := sch.Pick(context.Background()); !errors.Is(err, runtime.ErrTooMany) {
		t.Fatalf("expired-only pick: %v", err)
	}
}

// seedInstance persists an enabled persistent instance for reconcile
// tests; all instances may share one release.
func seedInstance(t *testing.T, st store.Store, rel *store.ServingRelease, n int) *store.PersistentInstance {
	t.Helper()
	ctx := context.Background()
	u, err := st.UpsertUserByEmail(ctx, "sch@x.com", "Sch", "sub-sch")
	if err != nil {
		t.Fatal(err)
	}
	inst := &store.PersistentInstance{
		ID:          store.NewID("pin_"),
		WorkspaceID: "ws_1", UserID: u.ID, ReleaseID: rel.ID,
		Enabled: true, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	_ = n
	if err := st.CreatePersistentInstance(ctx, inst); err != nil {
		t.Fatal(err)
	}
	return inst
}

func seedRelease(t *testing.T, st store.Store) *store.ServingRelease {
	t.Helper()
	ctx := context.Background()
	u, err := st.UpsertUserByEmail(ctx, "rel@x.com", "Rel", "sub-rel")
	if err != nil {
		t.Fatal(err)
	}
	prog := runtime.ProgramBundle{
		Sources:      map[string][]byte{"main.star": []byte("def main(input):\n    return {}")},
		InputSchema:  json.RawMessage(`{"type":"object"}`),
		OutputSchema: json.RawMessage(`{"type":"object"}`),
	}
	rel, err := store.NewServingRelease("ws_1", u.ID,
		json.RawMessage(`{"ttl_seconds":300,"capabilities":{"fs":{}}}`),
		runtime.PolicyBundle{Global: map[string]string{}, User: map[string]string{}},
		prog, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateServingRelease(ctx, rel); err != nil {
		t.Fatal(err)
	}
	return rel
}

func poolRows(t *testing.T, st store.Store, instID string, clk *testClock) []*store.ServingReplica {
	t.Helper()
	rows, err := st.ListInstanceServingReplicas(context.Background(), instID, clk.Now())
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestReconcilePoolFillsNodes(t *testing.T) {
	st := openStore(t)
	clk := newClock()
	sch := newSched(t, st, clk)
	rel := seedRelease(t, st)
	inst := seedInstance(t, st, rel, 0)

	// No registered nodes: reconcile is a no-op.
	if err := sch.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rows := poolRows(t, st, inst.ID, clk); len(rows) != 0 {
		t.Fatalf("claims without nodes: %d", len(rows))
	}

	registerNode(t, st, clk, "w1", "b1", "10.0.0.1:8090", 30*time.Second)
	registerNode(t, st, clk, "w2", "b1", "10.0.0.2:8090", 30*time.Second)
	if err := sch.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows := poolRows(t, st, inst.ID, clk)
	if len(rows) != 2 {
		t.Fatalf("pool size: %d", len(rows))
	}
	byNode := map[string]*store.ServingReplica{}
	epochs := map[int64]bool{}
	for _, r := range rows {
		byNode[r.NodeID] = r
		epochs[r.Epoch] = true
		if r.State != store.ServingReplicaPreparing || r.Generation != inst.Generation {
			t.Fatalf("row: %+v", r)
		}
	}
	if byNode["w1"] == nil || byNode["w2"] == nil || len(epochs) != 2 {
		t.Fatalf("pool: %+v", rows)
	}
	pin, _ := st.GetPersistentInstance(context.Background(), "ws_1", inst.ID)
	if pin.LastEpoch != 2 {
		t.Fatalf("high-water: %d", pin.LastEpoch)
	}
	// Idempotent: another pass adds nothing while leases live.
	if err := sch.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rows := poolRows(t, st, inst.ID, clk); len(rows) != 2 {
		t.Fatalf("pool grew: %d", len(rows))
	}

	// A third worker joins: only its replica is added; existing ids
	// and leases are untouched.
	registerNode(t, st, clk, "w3", "b1", "10.0.0.3:8090", 30*time.Second)
	before := map[string]time.Time{}
	for _, r := range poolRows(t, st, inst.ID, clk) {
		before[r.ID] = r.LeaseUntil
	}
	if err := sch.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows = poolRows(t, st, inst.ID, clk)
	if len(rows) != 3 {
		t.Fatalf("pool after join: %d", len(rows))
	}
	for _, r := range rows {
		if old, ok := before[r.ID]; ok && !r.LeaseUntil.Equal(old) {
			t.Fatalf("existing lease moved: %+v", r)
		}
	}
	var w3 *store.ServingReplica
	for _, r := range rows {
		if r.NodeID == "w3" {
			w3 = r
		}
	}
	if w3 == nil || w3.Epoch != 3 {
		t.Fatalf("w3 replica: %+v", w3)
	}

	// Node loss removes only eligibility: w2's heartbeat dies, its
	// replica row stays exactly as claimed in the store, and a NEW
	// instance never gets a w2 replica.
	var w2rep *store.ServingReplica
	for _, r := range poolRows(t, st, inst.ID, clk) {
		if r.NodeID == "w2" {
			w2rep = r
		}
	}
	if w2rep == nil {
		t.Fatal("w2 replica missing before loss")
	}
	clk.Advance(31 * time.Second)
	for _, id := range []string{"w1", "w3"} {
		if ok, err := st.HeartbeatServingNode(context.Background(), store.ServingNodeFence{
			NodeID: id, BootID: "b1", Incarnation: 1,
		}, clk.Now(), clk.Now().Add(30*time.Second), 4); err != nil || !ok {
			t.Fatalf("renew %s: %v %v", id, ok, err)
		}
	}
	inst2 := seedInstance(t, st, rel, 1)
	if err := sch.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows2 := poolRows(t, st, inst2.ID, clk)
	for _, r := range rows2 {
		if r.NodeID == "w2" {
			t.Fatal("dead node received a replica")
		}
	}
	if len(rows2) != 2 {
		t.Fatalf("new instance pool: %d", len(rows2))
	}
	// The lost node's replica row is untouched in the store — its
	// lease simply ran out; reconcile never deletes or migrates it.
	got, err := st.GetServingReplica(context.Background(), w2rep.ID)
	if err != nil || got.NodeID != "w2" || got.Epoch != w2rep.Epoch ||
		!got.LeaseUntil.Equal(w2rep.LeaseUntil) {
		t.Fatalf("node loss mutated the replica: %v %+v", err, got)
	}
}

func TestReconcilePoolConcurrentSchedulers(t *testing.T) {
	st := openStore(t)
	clk := newClock()
	rel := seedRelease(t, st)
	inst := seedInstance(t, st, rel, 0)
	registerNode(t, st, clk, "w1", "b1", "10.0.0.1:8090", 30*time.Second)
	registerNode(t, st, clk, "w2", "b1", "10.0.0.2:8090", 30*time.Second)
	registerNode(t, st, clk, "w3", "b1", "10.0.0.3:8090", 30*time.Second)

	// Two independent scheduler objects reconcile the same store —
	// CAS converges them to exactly one replica per node.
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sch := newSched(t, st, clk)
			for j := 0; j < 3; j++ {
				if err := sch.Reconcile(context.Background()); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	rows := poolRows(t, st, inst.ID, clk)
	if len(rows) != 3 {
		t.Fatalf("converged pool: %d", len(rows))
	}
	perNode := map[string]int{}
	for _, r := range rows {
		perNode[r.NodeID]++
	}
	for node, n := range perNode {
		if n != 1 {
			t.Fatalf("node %s has %d replicas", node, n)
		}
	}
}

func TestReconcilePoolCapacity(t *testing.T) {
	st := openStore(t)
	clk := newClock()
	sch := newSched(t, st, clk)
	rel := seedRelease(t, st)
	// Capacity-1 node plus a second instance: only one replica fits.
	if _, err := st.RegisterServingNode(context.Background(), &store.ServingNode{
		NodeID: "cap", BootID: "b", Addr: "10.0.0.9:8090", Capacity: 1,
		HeartbeatUntil: clk.Now().Add(30 * time.Second),
	}, clk.Now()); err != nil {
		t.Fatal(err)
	}
	a := seedInstance(t, st, rel, 0)
	b := seedInstance(t, st, rel, 1)
	if err := sch.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, inst := range []*store.PersistentInstance{a, b} {
		total += len(poolRows(t, st, inst.ID, clk))
	}
	if total != 1 {
		t.Fatalf("capacity pool: %d", total)
	}
}

// poolFakeStore implements only the store methods Reconcile touches —
// enough to prove budget/rotation/cursor behavior without 200+ rows.
type poolFakeStore struct {
	store.Store
	nodes       []*store.ServingNode
	insts       []*store.PersistentInstance
	claims      int
	attempted   []string
	claimedInst []string
	conflict    map[string]bool
	epoch       int64
}

func (f *poolFakeStore) ListServingNodes(context.Context, time.Time) ([]*store.ServingNode, error) {
	return f.nodes, nil
}

func (f *poolFakeStore) ListPersistentInstances(_ context.Context, after string, limit int) ([]*store.PersistentInstance, error) {
	out := []*store.PersistentInstance{}
	for _, in := range f.insts {
		if in.ID > after {
			out = append(out, in)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (f *poolFakeStore) ListInstanceServingReplicas(context.Context, string, time.Time) ([]*store.ServingReplica, error) {
	return nil, nil
}

func (f *poolFakeStore) GetPersistentInstance(_ context.Context, _, id string) (*store.PersistentInstance, error) {
	for _, in := range f.insts {
		if in.ID == id {
			c := *in
			c.LastEpoch = f.epoch
			return &c, nil
		}
	}
	return nil, store.ErrNotFound
}

func (f *poolFakeStore) ClaimServingReplica(_ context.Context, c store.ServingReplicaClaim) (*store.ServingReplica, error) {
	f.claims++
	f.attempted = append(f.attempted, c.NodeID)
	f.claimedInst = append(f.claimedInst, c.InstanceID)
	if f.conflict[c.NodeID] {
		return nil, store.ErrConflict
	}
	f.epoch++
	return &store.ServingReplica{
		ID: store.NewID(store.PrefixServingReplica), InstanceID: c.InstanceID,
		NodeID: c.NodeID, Epoch: c.ExpectedEpoch + 1,
		State: store.ServingReplicaPreparing, LeaseUntil: c.LeaseUntil,
	}, nil
}

func fakeNode(clk *testClock, id string, ip int) *store.ServingNode {
	return &store.ServingNode{
		NodeID: id, BootID: "b", Addr: fmt.Sprintf("10.0.0.%d:8090", ip),
		Incarnation: 1, Capacity: 8, HeartbeatUntil: clk.Now().Add(time.Hour),
	}
}

func fakeInst(id string) *store.PersistentInstance {
	return &store.PersistentInstance{
		ID: id, WorkspaceID: "ws_1", UserID: "u", ReleaseID: "rel_x",
		Enabled: true, Generation: 1,
	}
}

// With 201 candidates and a 200-attempt budget, pass 1 must skip
// exactly one node; once that node frees up the NEXT pass's rotated
// preference reaches it instead of starving the tail forever.
func TestReconcileRotatesPastFullPrefix(t *testing.T) {
	clk := newClock()
	st := &poolFakeStore{conflict: map[string]bool{}}
	for i := 0; i < 201; i++ {
		n := fakeNode(clk, fmt.Sprintf("n%03d", i), i+1)
		st.nodes = append(st.nodes, n)
		st.conflict[n.NodeID] = true
	}
	st.insts = []*store.PersistentInstance{fakeInst("pin_a")}
	sch := newSched(t, st, clk)
	ctx := context.Background()

	if err := sch.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if st.claims != maxClaimAttempts {
		t.Fatalf("pass1 attempts %d, want budget %d", st.claims, maxClaimAttempts)
	}
	attempted := map[string]bool{}
	for _, id := range st.attempted {
		attempted[id] = true
	}
	var skipped string
	for _, n := range st.nodes {
		if !attempted[n.NodeID] {
			if skipped != "" {
				t.Fatal("more than one candidate skipped")
			}
			skipped = n.NodeID
		}
	}
	if skipped == "" {
		t.Fatal("no candidate skipped within budget")
	}

	// Free the skipped node: the rotated second pass reaches it.
	delete(st.conflict, skipped)
	st.claims, st.attempted = 0, nil
	if err := sch.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if st.claims > maxClaimAttempts {
		t.Fatalf("pass2 attempts %d over budget", st.claims)
	}
	reached := false
	for _, id := range st.attempted {
		if id == skipped {
			reached = true
		}
	}
	if !reached {
		t.Fatalf("rotated pass never attempted freed node %s", skipped)
	}
}

// The instance cursor must only advance over processed instances: when
// the attempt budget breaks mid-page, the first unprocessed instance is
// resumed by the next pass — never skipped until a full wrap.
func TestReconcileBudgetPreservesCursor(t *testing.T) {
	clk := newClock()
	st := &poolFakeStore{conflict: map[string]bool{"n_bad": true}}
	st.nodes = []*store.ServingNode{
		fakeNode(clk, "n_ok", 1),
		fakeNode(clk, "n_bad", 2),
	}
	for i := 0; i < 201; i++ {
		st.insts = append(st.insts, fakeInst(fmt.Sprintf("pin_%03d", i)))
	}
	sch := newSched(t, st, clk)
	ctx := context.Background()

	if err := sch.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if st.claims != maxClaimAttempts {
		t.Fatalf("pass1 attempts %d, want budget %d", st.claims, maxClaimAttempts)
	}
	uniq := map[string]bool{}
	for _, id := range st.claimedInst {
		uniq[id] = true
	}
	if len(uniq) >= 201 {
		t.Fatal("pass1 processed every instance — test cannot discriminate")
	}
	// The cursor sits on the LAST PROCESSED instance: every claimed
	// instance id is <= lastID and lastID itself was processed.
	processed := map[string]bool{}
	for _, id := range st.claimedInst {
		processed[id] = true
	}
	if !processed[sch.lastID] {
		t.Fatalf("cursor %q skipped an unprocessed instance", sch.lastID)
	}
	// Later passes fill the rest — including the page tail.
	for i := 0; i < 3 && len(processed) < 201; i++ {
		if err := sch.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		for _, id := range st.claimedInst {
			processed[id] = true
		}
	}
	if len(processed) != 201 {
		t.Fatalf("instances never claimed: %d/201", len(processed))
	}
}
