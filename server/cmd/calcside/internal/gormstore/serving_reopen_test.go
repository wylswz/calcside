package gormstore

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"calcside/internal/runtime"
	"calcside/internal/store"
	"calcside/internal/store/storetest"
	"calcside/internal/types"
)

// A fresh handle on the same file must see durable serving state —
// releases, desired instances, node boots and replica fences survive
// the process boundary the scheduler is built for.
func TestServingReopen(t *testing.T) {
	dsn := storetest.SQLite(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	st, err := open(ctx, types.DriverSQLite, dsn)
	if err != nil {
		t.Fatal(err)
	}
	rel, err := store.NewServingRelease("ws_1", "usr_1",
		json.RawMessage(`{"capabilities":{"fs":{}}}`), runtime.PolicyBundle{},
		runtime.ProgramBundle{
			Sources:     map[string][]byte{"main.star": []byte(`def main(i): return {}`)},
			InputSchema: json.RawMessage(`{}`), OutputSchema: json.RawMessage(`{}`),
		}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateServingRelease(ctx, rel); err != nil {
		t.Fatal(err)
	}
	inst := &store.PersistentInstance{WorkspaceID: "ws_1", UserID: "usr_1",
		ReleaseID: rel.ID, Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := st.CreatePersistentInstance(ctx, inst); err != nil {
		t.Fatal(err)
	}
	n, err := st.RegisterServingNode(ctx, &store.ServingNode{NodeID: "rn", BootID: "b1",
		Addr: "a", Capacity: 4, HeartbeatUntil: now.Add(time.Hour)}, now)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := st.ClaimServingReplica(ctx, store.ServingReplicaClaim{
		InstanceID: inst.ID, ReleaseID: inst.ReleaseID, NodeID: n.NodeID, BootID: n.BootID,
		ReplicaID: store.NewID(store.PrefixServingReplica), NodeIncarnation: n.Incarnation,
		Generation: inst.Generation, ExpectedEpoch: 0,
		Now: now, LeaseUntil: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	st2, err := open(ctx, types.DriverSQLite, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	gotRel, err := st2.GetServingRelease(ctx, "ws_1", rel.ID)
	if err != nil || gotRel.ID != rel.ID {
		t.Fatalf("release not durable: %v", err)
	}
	gotInst, err := st2.GetPersistentInstance(ctx, "ws_1", inst.ID)
	if err != nil || gotInst.Generation != 1 || gotInst.LastEpoch != 1 {
		t.Fatalf("instance not durable: %v %+v", err, gotInst)
	}
	// The old boot's identity is retained: re-registering it still
	// fails after a restart.
	// Same current boot re-registers idempotently after restart.
	if _, err := st2.RegisterServingNode(ctx, &store.ServingNode{NodeID: "rn", BootID: "b1",
		Addr: "a", Capacity: 4, HeartbeatUntil: now.Add(time.Hour)}, now); err != nil {
		t.Fatal(err)
	}
	// A fresh unseen boot takes over; afterwards b1 can never return.
	if _, err := st2.RegisterServingNode(ctx, &store.ServingNode{NodeID: "rn", BootID: "b0",
		Addr: "a", Capacity: 4, HeartbeatUntil: now.Add(time.Hour)}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := st2.RegisterServingNode(ctx, &store.ServingNode{NodeID: "rn", BootID: "b1",
		Addr: "a", Capacity: 4, HeartbeatUntil: now.Add(time.Hour)}, now); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("old boot re-register: %v", err)
	}
	gotRep, err := st2.GetServingReplica(ctx, rep.ID)
	if err != nil || gotRep.Epoch != rep.Epoch || gotRep.BootID != "b1" {
		t.Fatalf("replica fence not durable: %v %+v", err, gotRep)
	}
}
