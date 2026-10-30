package run

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	_ "calcside/cmd/calcside/internal/gormstore"
	"calcside/cmd/calcside/internal/service"
	"calcside/cmd/calcside/internal/service/access"
	"calcside/cmd/calcside/internal/service/sandbox"
	"calcside/cmd/calcside/internal/service/vault"
	"calcside/cmd/calcside/internal/vaultcipher"
	"calcside/internal/capability"
	"calcside/internal/runtime"
	"calcside/internal/store"
	"calcside/internal/store/storetest"
	"calcside/internal/types"
)

// fakeRuntime is a scripted runtime.Runtime: Prepare always succeeds
// and Invoke returns the queued result, echoing the requested entry.
type fakeRuntime struct {
	res      runtime.ProgramResult
	invokeE  error
	prepareE error
	invokes  []*runtime.InvokeRequest
}

func (f *fakeRuntime) Create(ctx context.Context, r *runtime.CreateRequest) (*runtime.CreateResponse, error) {
	return &runtime.CreateResponse{}, nil
}
func (f *fakeRuntime) Exec(ctx context.Context, r *runtime.ExecRequest) (*runtime.ExecResponse, error) {
	return &runtime.ExecResponse{}, nil
}
func (f *fakeRuntime) Keepalive(ctx context.Context, r *runtime.KeepaliveRequest) (*runtime.KeepaliveResponse, error) {
	return &runtime.KeepaliveResponse{}, nil
}
func (f *fakeRuntime) Delete(ctx context.Context, r *runtime.DeleteRequest) (*runtime.DeleteResponse, error) {
	return &runtime.DeleteResponse{}, nil
}
func (f *fakeRuntime) Browse(ctx context.Context, r *runtime.BrowseRequest) (*runtime.BrowseResponse, error) {
	return &runtime.BrowseResponse{}, nil
}
func (f *fakeRuntime) Export(ctx context.Context, r *runtime.ExportRequest) (*runtime.ExportResponse, error) {
	return &runtime.ExportResponse{}, nil
}
func (f *fakeRuntime) Prompt(ctx context.Context, r *runtime.PromptRequest) (*runtime.PromptResponse, error) {
	return &runtime.PromptResponse{}, nil
}
func (f *fakeRuntime) Inspect(ctx context.Context, r *runtime.InspectRequest) (*runtime.InspectResponse, error) {
	return &runtime.InspectResponse{}, nil
}
func (f *fakeRuntime) Prepare(ctx context.Context, r *runtime.PrepareRequest) (*runtime.PrepareResponse, error) {
	return &runtime.PrepareResponse{Epoch: 1, Result: runtime.ProgramResult{}}, f.prepareE
}
func (f *fakeRuntime) Invoke(ctx context.Context, r *runtime.InvokeRequest) (*runtime.InvokeResponse, error) {
	f.invokes = append(f.invokes, r)
	res := f.res
	if res.Waiting != nil && len(r.Interrupts) > 0 {
		// The program waits on the first slot it was offered.
		res.Waiting.InterruptID = r.Interrupts[0].ID
	}
	return &runtime.InvokeResponse{Result: res}, f.invokeE
}

var _ runtime.Runtime = (*fakeRuntime)(nil)

var rNow = time.Now().UTC().Truncate(time.Second)

type tenv struct {
	st  store.Store
	svc *Service
	rt  *fakeRuntime
	app *store.Application
	rel *store.ServingRelease
	usr *store.User
}

func newEnv(t *testing.T, cipher *vaultcipher.Cipher, maxSteps int) *tenv {
	t.Helper()
	st, err := store.Open(context.Background(), "sqlite", storetest.SQLite(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ac := access.New(st)
	rt := &fakeRuntime{}
	reg := capability.NewRegistry()
	vaultSvc := vault.New(st, cipher, ac)
	sbx := sandbox.New(sandbox.Options{
		Store: st, Runtime: rt, Secrets: vaultSvc, Access: ac,
		Registry: reg, Limits: capability.ServerLimits{MaxTTL: time.Hour},
	})
	o := Options{
		Store: st, Runtime: rt, Sandbox: sbx, Access: ac,
		Cipher: cipher, ConsoleOrigin: "http://c", Now: time.Now,
		MaxSteps: maxSteps,
	}
	svc, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	u, err := st.UpsertUserByEmail(ctx, "o@x.com", "O", "sub-o")
	if err != nil {
		t.Fatal(err)
	}
	handlers := json.RawMessage(`{"on_ok":{},"on_exp":{},"on_tick":{}}`)
	app, err := store.NewApplication(store.PersonalWorkspaceID(u.ID), u.ID, "app", "code",
		json.RawMessage(`{}`), nil, nil, handlers, rNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateApplication(ctx, app); err != nil {
		t.Fatal(err)
	}
	bundle := runtime.ProgramBundle{
		Sources:      map[string][]byte{"main.star": []byte("def main(i): pass")},
		InputSchema:  json.RawMessage(`{"type":"object","additionalProperties":true}`),
		OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":true}`),
		Handlers: map[string]runtime.HandlerSpec{
			"on_ok": {}, "on_exp": {}, "on_tick": {},
		},
	}
	rel, err := store.NewServingRelease(app.WorkspaceID, u.ID, json.RawMessage(`{}`),
		runtime.PolicyBundle{}, bundle, rNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateServingRelease(ctx, rel); err != nil {
		t.Fatal(err)
	}
	return &tenv{st: st, svc: svc, rt: rt, app: app, rel: rel, usr: u}
}

func testCipher(t *testing.T) *vaultcipher.Cipher {
	c, err := vaultcipher.NewCipher("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func actor(u *store.User) service.Actor {
	return service.Actor{UserID: u.ID, Email: u.Email,
		WorkspaceID: store.PersonalWorkspaceID(u.ID)}
}

func keyActor(u *store.User, boundID string) service.Actor {
	return service.Actor{UserID: u.ID, Email: u.Email, Kind: types.AuthAPIKey,
		KeyID: "key_t", KeyBoundID: boundID, WorkspaceID: store.PersonalWorkspaceID(u.ID)}
}

func waitingRes(id, kind string) runtime.ProgramResult {
	return runtime.ProgramResult{Waiting: &runtime.ProgramWaiting{
		InterruptID: id, Kind: kind, Title: "t", Handler: "on_ok",
		Context: json.RawMessage(`{"x":1}`),
	}}
}

func startRun(t *testing.T, e *tenv) *Started {
	st, err := e.svc.Begin(context.Background(), e.app, e.rel, store.RunTriggerTest, actor(e.usr))
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Interrupts) != Slots {
		t.Fatalf("slots: %d", len(st.Interrupts))
	}
	return st
}

func TestEarlyCallbackCommit(t *testing.T) {
	e := newEnv(t, testCipher(t), 0)
	ctx := context.Background()
	st := startRun(t, e)
	slot := st.Interrupts[0]

	// Early resolution while reserved → accepted, stays reserved.
	if err := e.svc.Callback(ctx, keyActor(e.usr, slot.ID), slot.ID, []byte(`{"go":true}`)); err != nil {
		t.Fatalf("early callback: %v", err)
	}

	// Commit the step as a callback wait → early event is accepted and
	// the interrupt resolves + queues dispatch in one transaction.
	oc, err := e.svc.Complete(ctx, st, 0, 1, "main", "",
		&runtime.InvokeResponse{Result: waitingRes(slot.ID, store.InterruptCallback)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if oc.Status != store.RunWaiting || oc.Waiting == nil || oc.Waiting.Kind != store.InterruptCallback {
		t.Fatalf("outcome: %+v", oc)
	}
	intr, _ := e.st.GetInterrupt(ctx, slot.ID)
	if intr.Status != store.InterruptResolved || intr.DispatchState != store.DispatchQueued ||
		intr.ResolvedBy != "callback" {
		t.Fatalf("early commit: %+v", intr)
	}
	// Duplicate callback → already_resolved.
	if err := e.svc.Callback(ctx, keyActor(e.usr, slot.ID), slot.ID, []byte(`{"go":2}`)); err == nil {
		t.Fatal("second callback accepted")
	} else {
		var se *service.Error
		if !errors.As(err, &se) || se.Code != types.ErrCodeAlreadyResolved {
			t.Fatalf("dup callback: %v", err)
		}
	}

	// Dispatch runs the handler and finishes the run.
	e.rt.res = runtime.ProgramResult{Output: json.RawMessage(`{"done":true}`)}
	e.svc.RunOnce(ctx)
	run, _ := e.st.GetRun(ctx, st.Run.ID)
	if run.Status != store.RunSucceeded || run.Steps != 2 {
		t.Fatalf("dispatched run: %+v", run)
	}
	steps, _ := e.st.ListRunSteps(ctx, st.Run.ID)
	if len(steps) != 2 || steps[1].Entry != "on_ok" || steps[1].Status != store.StepSucceeded {
		t.Fatalf("steps: %+v", steps)
	}
	inv := e.rt.invokes[0]
	if inv.Entry != "on_ok" || inv.RunID != st.Run.ID {
		t.Fatalf("invoke: %+v", inv)
	}
	// Handler input carries the decrypted early event.
	var input map[string]any
	if err := json.Unmarshal(inv.Input, &input); err != nil {
		t.Fatal(err)
	}
	if input["event"] == nil || input["resolution"] != "resolved" || input["kind"] != "callback" {
		t.Fatalf("handler input: %s", inv.Input)
	}
}

func TestEarlyCallbackDiscarded(t *testing.T) {
	e := newEnv(t, testCipher(t), 0)
	ctx := context.Background()

	// Early event on a slot that commits as approval → discarded,
	// stays pending.
	st := startRun(t, e)
	slot := st.Interrupts[0]
	if err := e.svc.Callback(ctx, keyActor(e.usr, slot.ID), slot.ID, []byte(`{"go":true}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Complete(ctx, st, 0, 1, "main", "",
		&runtime.InvokeResponse{Result: waitingRes(slot.ID, store.InterruptApproval)}, nil); err != nil {
		t.Fatal(err)
	}
	intr, _ := e.st.GetInterrupt(ctx, slot.ID)
	if intr.Status != store.InterruptPending || intr.Event != nil || intr.ResolvedBy != "" {
		t.Fatalf("approval early not discarded: %+v", intr)
	}

	// Invalid event on a callback-committed slot → discarded.
	st2 := startRun(t, e)
	slot2 := st2.Interrupts[0]
	if err := e.svc.Callback(ctx, keyActor(e.usr, slot2.ID), slot2.ID, []byte(`[1,2]`)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Complete(ctx, st2, 0, 1, "main", "",
		&runtime.InvokeResponse{Result: waitingRes(slot2.ID, store.InterruptCallback)}, nil); err != nil {
		t.Fatal(err)
	}
	intr2, _ := e.st.GetInterrupt(ctx, slot2.ID)
	if intr2.Status != store.InterruptPending || intr2.Event != nil {
		t.Fatalf("invalid early not discarded: %+v", intr2)
	}
}

func TestChainLimit(t *testing.T) {
	e := newEnv(t, testCipher(t), 2)
	ctx := context.Background()
	st := startRun(t, e)
	// Step 1 waits (steps=1 < 2 OK).
	oc, err := e.svc.Complete(ctx, st, 0, 1, "main", "",
		&runtime.InvokeResponse{Result: waitingRes(st.Interrupts[0].ID, store.InterruptApproval)}, nil)
	if err != nil || oc.Status != store.RunWaiting {
		t.Fatalf("main wait: %v %+v", err, oc)
	}
	// Resolve + dispatch → handler step (steps=2) waits again →
	// chain_limit fails the run.
	if _, err := e.st.ResolveInterrupt(ctx, st.Interrupts[0].ID, []byte("s"), "usr_o", rNow); err != nil {
		t.Fatal(err)
	}
	e.rt.res = waitingRes("", store.InterruptApproval) // fake echoes slot id
	e.svc.RunOnce(ctx)
	run, _ := e.st.GetRun(ctx, st.Run.ID)
	if run.Status != store.RunFailed || run.ErrorCode != "chain_limit" {
		t.Fatalf("chain limit: %+v", run)
	}
}

func TestInterruptsUnavailable(t *testing.T) {
	e := newEnv(t, nil, 0) // no cipher
	ctx := context.Background()
	st, err := e.svc.Begin(ctx, e.app, e.rel, store.RunTriggerTest, actor(e.usr))
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Interrupts) != 0 {
		t.Fatalf("slots minted without cipher: %d", len(st.Interrupts))
	}
	oc, err := e.svc.Complete(ctx, st, 0, 1, "main", "",
		&runtime.InvokeResponse{Result: waitingRes("int_x", store.InterruptApproval)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if oc.Status != store.RunFailed {
		t.Fatalf("outcome: %+v", oc)
	}
	run, _ := e.st.GetRun(ctx, st.Run.ID)
	if run.ErrorCode != "interrupts_unavailable" {
		t.Fatalf("run: %+v", run)
	}
}

func TestRetrySeq(t *testing.T) {
	e := newEnv(t, testCipher(t), 0)
	ctx := context.Background()
	st := startRun(t, e)
	if _, err := e.svc.Complete(ctx, st, 0, 1, "main", "",
		&runtime.InvokeResponse{Result: waitingRes(st.Interrupts[0].ID, store.InterruptApproval)}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.ResolveInterrupt(ctx, st.Interrupts[0].ID, []byte("s"), "usr_o", rNow); err != nil {
		t.Fatal(err)
	}
	// First dispatch: handler step goes unknown (transport error).
	e.rt.invokeE = errors.New("transport")
	e.svc.RunOnce(ctx)
	run, _ := e.st.GetRun(ctx, st.Run.ID)
	if run.Status != store.RunUnknown {
		t.Fatalf("unknown run: %+v", run)
	}
	// Retry → dispatch again: same seq, attempt 2, steps unchanged.
	ok, err := e.st.RetryRun(ctx, st.Run.ID, rNow)
	if err != nil || !ok {
		t.Fatalf("retry: %v %v", ok, err)
	}
	e.rt.invokeE = nil
	e.rt.res = runtime.ProgramResult{Output: json.RawMessage(`{"done":true}`)}
	e.svc.RunOnce(ctx)
	run, _ = e.st.GetRun(ctx, st.Run.ID)
	steps, _ := e.st.ListRunSteps(ctx, st.Run.ID)
	if run.Status != store.RunSucceeded || run.Steps != 2 {
		t.Fatalf("retried run: %+v", run)
	}
	if len(steps) != 3 || steps[1].Seq != 1 || steps[1].Attempt != 1 ||
		steps[2].Seq != 1 || steps[2].Attempt != 2 {
		t.Fatalf("steps: %+v", steps)
	}
	intr, _ := e.st.GetInterrupt(ctx, st.Interrupts[0].ID)
	if intr.Attempt != 2 || intr.DispatchState != store.DispatchDone {
		t.Fatalf("interrupt: %+v", intr)
	}
}
