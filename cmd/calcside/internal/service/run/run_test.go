package run

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "calcside/cmd/calcside/internal/gormstore"
	"calcside/cmd/calcside/internal/service"
	"calcside/cmd/calcside/internal/service/access"
	"calcside/cmd/calcside/internal/service/sandbox"
	"calcside/cmd/calcside/internal/service/vault"
	"calcside/cmd/calcside/internal/vaultcipher"
	"calcside/internal/capability"
	"calcside/internal/engine"
	"calcside/internal/runtime"
	"calcside/internal/runtime/instance"
	"calcside/internal/runtime/remote"
	"calcside/internal/store"
	"calcside/internal/store/storetest"
	"calcside/internal/types"
)

var rtTestKey = []byte("run-test-shared-key-0123456789ab")

// gateRT fails requests to a path suffix — a real transport error, the
// only invoke failure the contract cannot classify.
type gateRT struct {
	base   http.RoundTripper
	suffix string
	fail   atomic.Bool
}

func (g *gateRT) RoundTrip(r *http.Request) (*http.Response, error) {
	if g.fail.Load() && strings.HasSuffix(r.URL.Path, g.suffix) {
		return nil, errors.New("gate: transport failure")
	}
	return g.base.RoundTrip(r)
}

var rNow = time.Now().UTC().Truncate(time.Second)

type tenv struct {
	st   store.Store
	svc  *Service
	gate *gateRT
	app  *store.Application
	rel  *store.ServingRelease
	usr  *store.User
}

// newEnv wires the service to a real execution path — a Manager behind
// the signed worker protocol — so dispatch behavior comes from real
// prepares/invokes, not a scripted runtime.
func newEnv(t *testing.T, cipher *vaultcipher.Cipher, maxSteps int) *tenv {
	t.Helper()
	st, err := store.Open(context.Background(), "sqlite", storetest.SQLite(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ac := access.New(st)
	reg := capability.NewRegistry()
	mgr := instance.New(instance.Options{
		Engine: engine.New(8), Registry: reg,
		Limits: capability.ServerLimits{MaxTTL: time.Hour},
	})
	t.Cleanup(mgr.StopReaper)
	srv := httptest.NewServer(remote.NewHandler(mgr, rtTestKey))
	t.Cleanup(srv.Close)
	gate := &gateRT{base: http.DefaultTransport, suffix: "/invoke"}
	rt, err := remote.NewDirect(srv.URL, &http.Client{Transport: gate}, rtTestKey, "run-test")
	if err != nil {
		t.Fatal(err)
	}
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
	app, err := store.NewApplication(store.PersonalWorkspaceID(u.ID), u.ID, "app", "code",
		json.RawMessage(`{}`), nil, nil, rNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateApplication(ctx, app); err != nil {
		t.Fatal(err)
	}
	rel, err := store.NewServingRelease(app.WorkspaceID, u.ID, json.RawMessage(`{}`),
		runtime.PolicyBundle{}, echoBundle(), rNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateServingRelease(ctx, rel); err != nil {
		t.Fatal(err)
	}
	return &tenv{st: st, svc: svc, gate: gate, app: app, rel: rel, usr: u}
}

// echoBundle is a program whose handler returns what it was invoked
// with: dispatch correctness is verified on the run output, not on a
// captured request.
func echoBundle() runtime.ProgramBundle {
	return runtime.ProgramBundle{
		Sources: map[string][]byte{"main.star": []byte(`def main(i):
    req = interrupt.new()
    interrupt.wait(req["id"], handler = "on_ok", context = {"x": 1})

def on_ok(i):
    return {"run_id": interrupt.run_id(), "input": i}
`)},
		InputSchema:  json.RawMessage(`{"type":"object","additionalProperties":true}`),
		OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":true}`),
	}
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

func waitingRes(id string) runtime.ProgramResult {
	return runtime.ProgramResult{Waiting: &runtime.ProgramWaiting{
		InterruptID: id, Handler: "on_ok",
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

func alreadyResolved(t *testing.T, err error) {
	t.Helper()
	var se *service.Error
	if !errors.As(err, &se) || se.Code != types.ErrCodeAlreadyResolved {
		t.Fatalf("want already_resolved, got %v", err)
	}
}

func TestEarlyTokenResumeCommit(t *testing.T) {
	e := newEnv(t, testCipher(t), 0)
	ctx := context.Background()
	st := startRun(t, e)
	slot := st.Interrupts[0]
	if slot.ResumeURL != "http://c/api/v1/interrupts/"+slot.ID+"/resume" {
		t.Fatalf("resume url: %s", slot.ResumeURL)
	}

	// The token may resume while the slot is still reserved.
	if err := e.svc.Resume(ctx, keyActor(e.usr, slot.ID), slot.ID, json.RawMessage(`{"go":true}`)); err != nil {
		t.Fatalf("early resume: %v", err)
	}
	// Committing the wait applies the early event and queues dispatch.
	oc, err := e.svc.Complete(ctx, st, 0, 1, "main", "",
		&runtime.InvokeResponse{Result: waitingRes(slot.ID)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if oc.Status != store.RunWaiting || oc.Waiting == nil || oc.Waiting.InterruptID != slot.ID {
		t.Fatalf("outcome: %+v", oc)
	}
	intr, _ := e.st.GetInterrupt(ctx, slot.ID)
	if intr.Status != store.InterruptResolved || intr.DispatchState != store.DispatchQueued ||
		intr.ResolvedBy != store.ResolvedByToken {
		t.Fatalf("early commit: %+v", intr)
	}
	alreadyResolved(t, e.svc.Resume(ctx, keyActor(e.usr, slot.ID), slot.ID, json.RawMessage(`{"go":2}`)))

	// Dispatch runs the handler; the program echoes its input, so the
	// run output proves the entry, the run fence and the handler input.
	e.svc.RunOnce(ctx)
	run, _ := e.st.GetRun(ctx, st.Run.ID)
	if run.Status != store.RunSucceeded || run.Steps != 2 {
		t.Fatalf("dispatched run: %+v", run)
	}
	d, err := e.svc.GetRun(ctx, actor(e.usr), st.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		RunID string         `json:"run_id"`
		Input map[string]any `json:"input"`
	}
	if err := json.Unmarshal([]byte(d.OutputJSON), &out); err != nil {
		t.Fatalf("handler output: %v %s", err, d.OutputJSON)
	}
	if out.RunID != st.Run.ID {
		t.Fatalf("invoke run fence lost: %+v", out)
	}
	in := out.Input
	if in["run_id"] != st.Run.ID || in["interrupt_id"] != slot.ID ||
		in["handler"] != "on_ok" || in["resolution"] != "resolved" ||
		in["resolved_by"] != store.ResolvedByToken {
		t.Fatalf("handler input: %v", in)
	}
	if ev, ok := in["event"].(map[string]any); !ok || ev["go"] != true {
		t.Fatalf("handler event: %v", in["event"])
	}
	if _, ok := in["kind"]; ok {
		t.Fatalf("handler input carries kind: %v", in)
	}
}

func TestResumeAuthorization(t *testing.T) {
	e := newEnv(t, testCipher(t), 0)
	ctx := context.Background()
	st := startRun(t, e)
	slot, other := st.Interrupts[0], st.Interrupts[1]
	user := actor(e.usr)

	// A reserved slot is invisible to regular callers.
	if err := e.svc.Resume(ctx, user, slot.ID, json.RawMessage(`{}`)); err == nil {
		t.Fatal("user resumed a reserved slot")
	}
	if _, err := e.svc.Complete(ctx, st, 0, 1, "main", "",
		&runtime.InvokeResponse{Result: waitingRes(slot.ID)}, nil); err != nil {
		t.Fatal(err)
	}
	// Another interrupt's token is hidden; non-object events are rejected.
	var se *service.Error
	if err := e.svc.Resume(ctx, keyActor(e.usr, other.ID), slot.ID, json.RawMessage(`{}`)); !errors.As(err, &se) || se.Code != types.ErrCodeNotFound {
		t.Fatalf("foreign token: %v", err)
	}
	if err := e.svc.Resume(ctx, user, slot.ID, json.RawMessage(`[1]`)); !errors.As(err, &se) || se.Code != types.ErrCodeBadRequest {
		t.Fatalf("array event: %v", err)
	}
	// A user with Run resumes the pending interrupt once.
	v, err := e.svc.GetInterrupt(ctx, user, slot.ID)
	if err != nil || !v.CanResume || v.ResumeURL == "" {
		t.Fatalf("view: %v %+v", err, v)
	}
	if err := e.svc.Resume(ctx, user, slot.ID, json.RawMessage(`{"approved":true}`)); err != nil {
		t.Fatalf("user resume: %v", err)
	}
	intr, _ := e.st.GetInterrupt(ctx, slot.ID)
	if intr.ResolvedBy != e.usr.ID || intr.DispatchState != store.DispatchQueued {
		t.Fatalf("resumed: %+v", intr)
	}
	alreadyResolved(t, e.svc.Resume(ctx, keyActor(e.usr, slot.ID), slot.ID, json.RawMessage(`{}`)))
}

func TestNoSlotsWithoutInterruptReference(t *testing.T) {
	e := newEnv(t, testCipher(t), 0)
	bundle := e.rel.Program
	bundle.Sources = map[string][]byte{"main.star": []byte("def main(i):\n    return i\n")}
	rel, err := store.NewServingRelease(e.app.WorkspaceID, e.usr.ID, json.RawMessage(`{}`),
		runtime.PolicyBundle{}, bundle, rNow)
	if err != nil {
		t.Fatal(err)
	}
	st, err := e.svc.Begin(context.Background(), e.app, rel, store.RunTriggerTest, actor(e.usr))
	if err != nil || len(st.Interrupts) != 0 {
		t.Fatalf("slots for a program without interrupt: %v %d", err, len(st.Interrupts))
	}
}

func TestChainLimit(t *testing.T) {
	e := newEnv(t, testCipher(t), 2)
	ctx := context.Background()
	// The handler waits on a fresh slot, so the dispatched step hits
	// the step budget instead of queuing another interrupt.
	bundle := e.rel.Program
	bundle.Sources = map[string][]byte{"main.star": []byte(`def main(i):
    req = interrupt.new()
    interrupt.wait(req["id"], handler = "on_ok", context = {})

def on_ok(i):
    req = interrupt.new()
    interrupt.wait(req["id"], handler = "on_ok", context = {})
`)}
	rel, err := store.NewServingRelease(e.app.WorkspaceID, e.usr.ID, json.RawMessage(`{}`),
		runtime.PolicyBundle{}, bundle, rNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.CreateServingRelease(ctx, rel); err != nil {
		t.Fatal(err)
	}
	st, err := e.svc.Begin(ctx, e.app, rel, store.RunTriggerTest, actor(e.usr))
	if err != nil {
		t.Fatal(err)
	}
	// Step 1 waits (steps=1 < 2 OK).
	oc, err := e.svc.Complete(ctx, st, 0, 1, "main", "",
		&runtime.InvokeResponse{Result: waitingRes(st.Interrupts[0].ID)}, nil)
	if err != nil || oc.Status != store.RunWaiting {
		t.Fatalf("main wait: %v %+v", err, oc)
	}
	// Resolve + dispatch → handler step (steps=2) waits again →
	// chain_limit fails the run.
	if _, err := e.st.ResolveInterrupt(ctx, st.Interrupts[0].ID, []byte("s"), "usr_o", rNow); err != nil {
		t.Fatal(err)
	}
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
		&runtime.InvokeResponse{Result: waitingRes("int_x")}, nil)
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
		&runtime.InvokeResponse{Result: waitingRes(st.Interrupts[0].ID)}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.ResolveInterrupt(ctx, st.Interrupts[0].ID, []byte("s"), "usr_o", rNow); err != nil {
		t.Fatal(err)
	}
	// First dispatch: handler step goes unknown (transport error).
	e.gate.fail.Store(true)
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
	e.gate.fail.Store(false)
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

// A fresh resolution must nudge the dispatch loop instead of waiting
// for the next Tick.
func TestResolveWakesDispatchLoop(t *testing.T) {
	e := newEnv(t, testCipher(t), 0)
	ctx := context.Background()
	st := startRun(t, e)
	slot := st.Interrupts[0]
	if err := e.svc.Resume(ctx, keyActor(e.usr, slot.ID), slot.ID, json.RawMessage(`{"go":true}`)); err != nil {
		t.Fatalf("resume: %v", err)
	}
	select {
	case <-e.svc.wake:
	case <-time.After(time.Second):
		t.Fatal("resolve did not wake the dispatch loop")
	}
}
