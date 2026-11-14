package application

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"calcside/cmd/calcside/internal/service"
	"calcside/internal/store"
	"calcside/internal/types"
)

type applicationFenceStore struct {
	store.Store
	app       *store.Application
	pin       *store.PersistentInstance
	pins      map[string]*store.PersistentInstance
	rep       *store.ServingReplica
	reps      []*store.ServingReplica
	repsByPin map[string][]*store.ServingReplica
	nodes     []*store.ServingNode
}

func (st *applicationFenceStore) GetPersistentInstance(_ context.Context, _, id string) (*store.PersistentInstance, error) {
	if st.pins != nil {
		return st.pins[id], nil
	}
	return st.pin, nil
}
func (st *applicationFenceStore) ListInstanceServingReplicas(_ context.Context, instanceID string, _ time.Time) ([]*store.ServingReplica, error) {
	if st.repsByPin != nil {
		return st.repsByPin[instanceID], nil
	}
	if st.reps != nil {
		return st.reps, nil
	}
	if st.rep != nil && st.rep.InstanceID == instanceID {
		return []*store.ServingReplica{st.rep}, nil
	}
	return nil, nil
}
func (st *applicationFenceStore) GetServingReplica(context.Context, string) (*store.ServingReplica, error) {
	return st.rep, nil
}
func (st *applicationFenceStore) GetCurrentServingReplica(context.Context, string) (*store.ServingReplica, error) {
	return st.rep, nil
}
func (st *applicationFenceStore) ListServingNodes(context.Context, time.Time) ([]*store.ServingNode, error) {
	return st.nodes, nil
}

func TestApplicationReadinessUsesEntireFence(t *testing.T) {
	cases := []struct {
		name   string
		change func(*applicationFenceStore)
	}{
		{"disabled", func(st *applicationFenceStore) { st.pin.Enabled = false }},
		{"wrong instance", func(st *applicationFenceStore) { st.rep.InstanceID = "pin_other" }},
		{"wrong workspace", func(st *applicationFenceStore) { st.rep.WorkspaceID = "ws_other" }},
		{"old generation", func(st *applicationFenceStore) { st.rep.Generation-- }},
		{"future epoch", func(st *applicationFenceStore) { st.rep.Epoch = st.pin.LastEpoch + 1 }},
		{"zero epoch", func(st *applicationFenceStore) { st.rep.Epoch = 0 }},
		{"old release", func(st *applicationFenceStore) { st.rep.ReleaseID = "rel_old" }},
		{"expired lease", func(st *applicationFenceStore) { st.rep.LeaseUntil = time.Now().Add(-time.Second) }},
		{"preparing", func(st *applicationFenceStore) { st.rep.State = store.ServingReplicaPreparing }},
		{"dead node", func(st *applicationFenceStore) { st.nodes = nil }},
		{"old boot", func(st *applicationFenceStore) { st.nodes[0].BootID = "boot_new" }},
		{"old incarnation", func(st *applicationFenceStore) { st.nodes[0].Incarnation++ }},
		{"expired heartbeat", func(st *applicationFenceStore) { st.nodes[0].HeartbeatUntil = time.Now().Add(-time.Second) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := &applicationFenceStore{
				app: &store.Application{WorkspaceID: "ws_x", PersistentInstanceID: "pin_x", PublishedVersion: 1, Version: 1},
				pin: &store.PersistentInstance{
					ID: "pin_x", WorkspaceID: "ws_x", UserID: "usr_x", ReleaseID: "rel_x",
					Enabled: true, Generation: 2, LastEpoch: 3, ActiveReplicaID: "",
				},
				rep: &store.ServingReplica{
					ID: "rep_x", WorkspaceID: "ws_x", InstanceID: "pin_x", ReleaseID: "rel_x",
					Generation: 2, Epoch: 3, State: store.ServingReplicaReady,
					LeaseUntil: time.Now().Add(time.Minute), NodeID: "node_x", BootID: "boot_x", NodeIncarnation: 4,
				},
				nodes: []*store.ServingNode{{
					NodeID: "node_x", BootID: "boot_x", Incarnation: 4, HeartbeatUntil: time.Now().Add(time.Minute),
				}},
			}
			s := &Service{o: Options{Store: st, Now: time.Now}}
			if _, _, err := s.readyReplica(context.Background(), st.app); err != nil {
				t.Fatalf("valid fixture not ready: %v", err)
			}
			if status, _, _, _ := s.status(context.Background(), st.app); status != "ready" {
				t.Fatalf("valid fixture status %s", status)
			}
			tc.change(st)
			if _, _, err := s.readyReplica(context.Background(), st.app); err == nil {
				t.Fatal("invalid fence accepted")
			}
			if status, _, _, _ := s.status(context.Background(), st.app); status == "ready" {
				t.Fatal("status advertised stale replica as ready")
			}
		})
	}
}

func TestApplicationKeysAndWorkspaceIsolation(t *testing.T) {
	s, st, _, actor := lifecycleService(t)
	ctx := context.Background()
	v, err := s.Create(ctx, actor, CreateRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []service.Actor{
		{UserID: actor.UserID, WorkspaceID: actor.WorkspaceID, Kind: types.AuthAPIKey, KeyBoundID: "ins_x"},
		{UserID: actor.UserID, WorkspaceID: actor.WorkspaceID, Kind: types.AuthAPIKey, KeySpec: json.RawMessage(`{}`)},
	} {
		if _, err := s.Get(ctx, key, v.Application.ID); err == nil {
			t.Fatal("restricted key viewed application")
		}
		if _, err := s.List(ctx, key, "", 200); err == nil {
			t.Fatal("restricted key listed applications")
		}
	}
	foreign, err := st.UpsertUserByEmail(ctx, "foreign@x.com", "Foreign", "foreign")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, service.Actor{
		UserID: foreign.ID, Kind: types.AuthSession, WorkspaceID: store.PersonalWorkspaceID(foreign.ID),
	}, v.Application.ID); err == nil {
		t.Fatal("foreign workspace read application")
	}
	// The object's workspace, not the request's currently selected workspace,
	// determines both authorization and the boundary used for draft tests.
	actor.WorkspaceID = store.PersonalWorkspaceID(foreign.ID)
	if result, err := s.Test(ctx, actor, v.Application.ID, 1, json.RawMessage(`{}`)); err != nil ||
		result.Result.Error != nil {
		t.Fatalf("app workspace did not override unrelated request workspace: %+v, %v", result, err)
	}
}

// The ready pool accepts every fenced live replica — ActiveReplicaID
// and LastEpoch equality are irrelevant — and invoke selection
// round-robins across it without status consuming the rotation.
func TestApplicationReadyPoolSelection(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	live := func(id string) *store.ServingNode {
		return &store.ServingNode{NodeID: id, BootID: "b_" + id, Incarnation: 1,
			HeartbeatUntil: now.Add(time.Minute)}
	}
	rep := func(id, node string, epoch int64, state string) *store.ServingReplica {
		return &store.ServingReplica{
			ID: id, WorkspaceID: "ws_x", InstanceID: "pin_x", ReleaseID: "rel_x",
			Generation: 2, Epoch: epoch, State: state,
			LeaseUntil: now.Add(time.Minute), NodeID: node,
			BootID: "b_" + node, NodeIncarnation: 1,
		}
	}
	app := &store.Application{WorkspaceID: "ws_x", PersistentInstanceID: "pin_x",
		PublishedVersion: 1, Version: 1}
	pin := &store.PersistentInstance{
		ID: "pin_x", WorkspaceID: "ws_x", UserID: "usr_x", ReleaseID: "rel_x",
		Enabled: true, Generation: 2, LastEpoch: 3, ActiveReplicaID: "rep_elsewhere",
	}
	// Two ready replicas at older epochs, plus rows that must be
	// filtered out: preparing, future epoch, dead node, wrong boot.
	goodA := rep("rep_a", "n_a", 1, store.ServingReplicaReady)
	goodB := rep("rep_b", "n_b", 2, store.ServingReplicaReady)
	bad := []*store.ServingReplica{
		rep("rep_prep", "n_c", 3, store.ServingReplicaPreparing),
		rep("rep_future", "n_d", 99, store.ServingReplicaReady),
		rep("rep_dead", "n_dead", 1, store.ServingReplicaReady),
	}
	bad[2].NodeID = "n_dead"
	bad[2].BootID = "b_n_dead"
	st := &applicationFenceStore{
		app: app, pin: pin,
		reps:  append([]*store.ServingReplica{goodA, goodB}, bad...),
		nodes: []*store.ServingNode{live("n_a"), live("n_b"), live("n_c"), live("n_d")},
	}
	s := &Service{o: Options{Store: st, Now: time.Now}}

	pool, gotPin, err := s.readyReplicas(ctx, app)
	if err != nil || gotPin != pin {
		t.Fatalf("pool: %v %v", err, gotPin)
	}
	if len(pool) != 2 || pool[0].ID != "rep_a" || pool[1].ID != "rep_b" {
		t.Fatalf("pool members: %+v", pool)
	}
	// Selection rotates across the ready pool only — exact order, so a
	// status call that consumed the counter would be caught.
	want := []string{"rep_b", "rep_a", "rep_b", "rep_a"}
	for i, id := range want {
		r, _, err := s.readyReplica(ctx, app)
		if err != nil {
			t.Fatal(err)
		}
		if r.ID != id {
			t.Fatalf("selection %d = %s, want %s", i, r.ID, id)
		}
	}
	// Status reports ready and does not consume the rotation: the next
	// selection continues the same alternation.
	if status, _, _, _ := s.status(ctx, app); status != "ready" {
		t.Fatalf("status: %s", status)
	}
	r, _, err := s.readyReplica(ctx, app)
	if err != nil {
		t.Fatal(err)
	}
	if r.ID != "rep_b" {
		t.Fatalf("post-status selection %s, want rep_b", r.ID)
	}
	// Empty pool -> not_running even though a preparing row exists.
	st2 := &applicationFenceStore{app: app, pin: pin,
		reps:  []*store.ServingReplica{rep("rep_prep", "n_c", 3, store.ServingReplicaPreparing)},
		nodes: []*store.ServingNode{live("n_c")}}
	s2 := &Service{o: Options{Store: st2, Now: time.Now}}
	if _, _, err := s2.readyReplicas(ctx, app); err == nil {
		t.Fatal("preparing-only pool accepted")
	}
	if status, _, _, _ := s2.status(ctx, app); status == "ready" {
		t.Fatal("status ready on preparing-only pool")
	}
}

// Round-robin is per pin: interleaved invokes for two applications must
// each spread across BOTH of their own ready replicas — a service-global
// counter would pin app A to one worker and app B to the other forever.
func TestApplicationPoolRotationIsPerPin(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	live := func(id string) *store.ServingNode {
		return &store.ServingNode{NodeID: id, BootID: "b_" + id, Incarnation: 1,
			HeartbeatUntil: now.Add(time.Minute)}
	}
	mkPin := func(id, rel string) *store.PersistentInstance {
		return &store.PersistentInstance{
			ID: id, WorkspaceID: "ws_x", UserID: "usr_x", ReleaseID: rel,
			Enabled: true, Generation: 1, LastEpoch: 2,
		}
	}
	mkRep := func(id, pinID, rel, node string, epoch int64) *store.ServingReplica {
		return &store.ServingReplica{
			ID: id, WorkspaceID: "ws_x", InstanceID: pinID, ReleaseID: rel,
			Generation: 1, Epoch: epoch, State: store.ServingReplicaReady,
			LeaseUntil: now.Add(time.Minute), NodeID: node,
			BootID: "b_" + node, NodeIncarnation: 1,
		}
	}
	appA := &store.Application{WorkspaceID: "ws_x", PersistentInstanceID: "pin_a"}
	appB := &store.Application{WorkspaceID: "ws_x", PersistentInstanceID: "pin_b"}
	st := &applicationFenceStore{
		pins: map[string]*store.PersistentInstance{
			"pin_a": mkPin("pin_a", "rel_a"), "pin_b": mkPin("pin_b", "rel_b"),
		},
		repsByPin: map[string][]*store.ServingReplica{
			"pin_a": {mkRep("ra1", "pin_a", "rel_a", "na1", 1), mkRep("ra2", "pin_a", "rel_a", "na2", 2)},
			"pin_b": {mkRep("rb1", "pin_b", "rel_b", "nb1", 1), mkRep("rb2", "pin_b", "rel_b", "nb2", 2)},
		},
		nodes: []*store.ServingNode{live("na1"), live("na2"), live("nb1"), live("nb2")},
	}
	s := &Service{o: Options{Store: st, Now: time.Now}}

	// Interleave A,B,A,B: each app's own counter must rotate it across
	// both of ITS replicas regardless of the other's traffic.
	aSeen, bSeen := map[string]bool{}, map[string]bool{}
	for i := 0; i < 4; i++ {
		app := appA
		seen := aSeen
		if i%2 == 1 {
			app, seen = appB, bSeen
		}
		r, _, err := s.readyReplica(ctx, app)
		if err != nil {
			t.Fatal(err)
		}
		seen[r.ID] = true
	}
	if len(aSeen) != 2 || !aSeen["ra1"] || !aSeen["ra2"] {
		t.Fatalf("app A aliased: %v", aSeen)
	}
	if len(bSeen) != 2 || !bSeen["rb1"] || !bSeen["rb2"] {
		t.Fatalf("app B aliased: %v", bSeen)
	}
}

// A valid preparing sibling means recovery is in flight: status reports
// preparing even when the latest-epoch diagnostic row has failed.
func TestApplicationStatusPreparingBeatsStaleFailure(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	app := &store.Application{WorkspaceID: "ws_x", PersistentInstanceID: "pin_x",
		PublishedVersion: 1, Version: 1}
	pin := &store.PersistentInstance{
		ID: "pin_x", WorkspaceID: "ws_x", UserID: "usr_x", ReleaseID: "rel_x",
		Enabled: true, Generation: 2, LastEpoch: 3,
	}
	mk := func(id string, epoch int64, state string) *store.ServingReplica {
		return &store.ServingReplica{
			ID: id, WorkspaceID: "ws_x", InstanceID: "pin_x", ReleaseID: "rel_x",
			Generation: 2, Epoch: epoch, State: state,
			LeaseUntil: now.Add(time.Minute), NodeID: "n_c",
			BootID: "b_n_c", NodeIncarnation: 1,
		}
	}
	// Latest-epoch row failed; an older-epoch sibling is still
	// preparing — the pool has no ready member yet.
	failed := mk("rep_fail", 3, store.ServingReplicaFailed)
	failed.ErrorCode = "runtime"
	st := &applicationFenceStore{
		app: app, pin: pin,
		rep:  failed,
		reps: []*store.ServingReplica{mk("rep_prep", 2, store.ServingReplicaPreparing)},
		nodes: []*store.ServingNode{{
			NodeID: "n_c", BootID: "b_n_c", Incarnation: 1,
			HeartbeatUntil: now.Add(time.Minute),
		}},
	}
	s := &Service{o: Options{Store: st, Now: time.Now}}
	if status, _, _, _ := s.status(ctx, app); status != "preparing" {
		t.Fatalf("status %s, want preparing", status)
	}
	// With no preparing sibling the same failure stays degraded.
	st.reps = nil
	if status, _, code, _ := s.status(ctx, app); status != "degraded" || code != "runtime" {
		t.Fatalf("status %s code %s, want degraded/runtime", status, code)
	}
}
