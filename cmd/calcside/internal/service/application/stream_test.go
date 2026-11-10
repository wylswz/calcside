package application

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"calcside/cmd/calcside/internal/service/sandbox"

	runsvc "calcside/cmd/calcside/internal/service/run"
	"calcside/internal/runtime"
	"calcside/internal/store"
)

type disconnectedStreamSink struct{}

func (disconnectedStreamSink) Start() error                { return nil }
func (disconnectedStreamSink) Item(json.RawMessage) error  { return nil }
func (disconnectedStreamSink) Stats() (bool, int64, int64) { return true, 1, 7 }

func TestCompleteRunDisconnectedStream(t *testing.T) {
	svc, st := newSvc(t)
	a := ownerActor(st)
	ctx := context.Background()
	v, err := svc.Create(ctx, a, CreateRequest{Name: "stream"})
	if err != nil {
		t.Fatal(err)
	}
	rel, _, err := svc.release(ctx, a, v.Application)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateServingRelease(ctx, rel); err != nil {
		t.Fatal(err)
	}
	runs, err := runsvc.New(runsvc.Options{
		Store: st, Runtime: svc.o.Runtime, Sandbox: svc.o.Sandbox, Access: svc.o.Access,
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.o.Runs = runs
	started, err := runs.Begin(ctx, v.Application, rel, store.RunTriggerTest, a)
	if err != nil {
		t.Fatal(err)
	}
	cctx, cancel := context.WithCancel(runtime.WithInvokeStream(ctx, disconnectedStreamSink{}))
	cancel()
	outcome, err := svc.completeRun(cctx, started, "inv_disconnected", nil, context.Canceled)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Status != store.RunUnknown {
		t.Fatalf("outcome: %+v", outcome)
	}
	run, err := st.GetRun(ctx, started.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != store.RunUnknown || !run.Streaming || run.StreamItems != 1 || run.StreamBytes != 7 || len(run.Output) != 0 {
		t.Fatalf("run: %+v", run)
	}
	steps, err := st.ListRunSteps(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 1 || steps[0].Status != store.StepUnknown || !steps[0].Streaming || steps[0].StreamItems != 1 || steps[0].StreamBytes != 7 {
		t.Fatalf("steps: %+v", steps)
	}
}

func TestNewReleaseDefaultOutputSchema(t *testing.T) {
	now := time.Now()
	app := &store.Application{
		ID: "app_d", WorkspaceID: "ws_1", UserID: "u_1",
		Code: "def main(i): return i", Spec: json.RawMessage(`{}`),
	}
	b := &sandbox.Boundary{Spec: json.RawMessage(`{}`)}
	rel, err := newRelease(app, b, now)
	if err != nil {
		t.Fatal(err)
	}
	if string(rel.Program.OutputSchema) != `{}` {
		t.Fatalf("nil output must default to bounded-any, got %s", rel.Program.OutputSchema)
	}
	rel2, err := newRelease(app, b, now)
	if err != nil || rel2.ID != rel.ID {
		t.Fatalf("release must be deterministic: %v %v", err, rel2)
	}
	app.OutputSchema = json.RawMessage(`{"type":"object"}`)
	relObj, err := newRelease(app, b, now)
	if err != nil || relObj.ID == rel.ID {
		t.Fatal("explicit object schema must differ from default digest")
	}
}
