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
	app   *store.Application
	pin   *store.PersistentInstance
	rep   *store.ServingReplica
	nodes []*store.ServingNode
}

func (st *applicationFenceStore) GetPersistentInstance(context.Context, string, string) (*store.PersistentInstance, error) {
	return st.pin, nil
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
		{"not active", func(st *applicationFenceStore) { st.pin.ActiveReplicaID = "rep_other" }},
		{"wrong instance", func(st *applicationFenceStore) { st.rep.InstanceID = "pin_other" }},
		{"wrong workspace", func(st *applicationFenceStore) { st.rep.WorkspaceID = "ws_other" }},
		{"old generation", func(st *applicationFenceStore) { st.rep.Generation-- }},
		{"old epoch", func(st *applicationFenceStore) { st.rep.Epoch-- }},
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
					Enabled: true, Generation: 2, LastEpoch: 3, ActiveReplicaID: "rep_x",
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
