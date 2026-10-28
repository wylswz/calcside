package audit

import (
	"context"
	"testing"
	"time"

	_ "calcside/cmd/calcside/internal/gormstore"
	"calcside/internal/runtime"
	"calcside/internal/store"
	"calcside/internal/store/storetest"
	"calcside/internal/types"
)

func newStore(t *testing.T) store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), types.DriverSQLite, storetest.SQLite(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func batch(instanceID string) runtime.AuditBatch {
	return runtime.AuditBatch{Events: []runtime.AuditEvent{{
		Ts: time.Now(), UserID: "u1", InstanceID: instanceID, ExecID: "ex_1",
		Capability: "fs", Op: "read", Phase: types.PhaseBefore, Decision: types.DecisionAllow,
	}}}
}

func list(t *testing.T, st store.Store, f store.AuditFilter) []*store.AuditEvent {
	t.Helper()
	evs, err := st.ListAuditEvents(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

// Application replicas carry no instance row: the workspace arrives
// with the call.
func TestRecordWorkspaceStampsWorkspace(t *testing.T) {
	st := newStore(t)
	r := NewRecorder(st)
	r.RecordWorkspace("ws_app", batch("apv_replica"))
	r.Close()
	evs := list(t, st, store.AuditFilter{InstanceID: "apv_replica"})
	if len(evs) != 1 || evs[0].WorkspaceID != "ws_app" {
		t.Fatalf("events: %+v", evs)
	}
}

// An event whose workspace cannot be resolved is dropped, never
// poisoning the rest of the batch.
func TestRecordDropsUnresolvableEvent(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	u, err := st.UpsertUserByEmail(ctx, "a@x.com", "A", "a1")
	if err != nil {
		t.Fatal(err)
	}
	ws := store.PersonalWorkspaceID(u.ID)
	in := &store.Instance{ID: "ins_real", UserID: u.ID, WorkspaceID: ws, Spec: []byte(`{}`), Status: types.InstanceRunning}
	if err := st.CreateInstance(ctx, in); err != nil {
		t.Fatal(err)
	}
	r := NewRecorder(st)
	r.Record(runtime.AuditBatch{Events: append(batch("ins_real").Events, batch("apv_unknown").Events...)})
	r.Close()
	if evs := list(t, st, store.AuditFilter{InstanceID: "ins_real"}); len(evs) != 1 || evs[0].WorkspaceID != ws {
		t.Fatalf("valid event lost: %+v", evs)
	}
	if evs := list(t, st, store.AuditFilter{InstanceID: "apv_unknown"}); len(evs) != 0 {
		t.Fatalf("unresolvable event persisted: %+v", evs)
	}
}
