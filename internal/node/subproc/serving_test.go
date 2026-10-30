package subproc

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"calcside/internal/runtime"
)

func servingSpec() []byte {
	return []byte(`{"ttl_seconds":300,"limits":{"exec_timeout_ms":30000,"max_steps":1000000,"max_output_bytes":1048576},"capabilities":{"fs":{},"io":{}}}`)
}

func supPrep(id, src string, assets map[string][]byte) *runtime.PrepareRequest {
	return &runtime.PrepareRequest{
		CreateRequest: runtime.CreateRequest{
			InstanceID: id, Owner: owner,
			Spec: servingSpec(), ExpiresAt: time.Now().Add(time.Hour),
		},
		ReleaseID: "rel-1", Generation: 1, PrepareID: id + "-p",
		Program: runtime.ProgramBundle{
			Sources:      map[string][]byte{"main.star": []byte(src)},
			Assets:       assets,
			InputSchema:  json.RawMessage(`{}`),
			OutputSchema: json.RawMessage(`{}`),
		},
	}
}

func supInv(id, invokeID, input string) *runtime.InvokeRequest {
	return &runtime.InvokeRequest{InstanceID: id, Owner: owner,
		ReleaseID: "rel-1", Generation: 1, Epoch: 0, InvokeID: invokeID,
		Input: json.RawMessage(input)}
}

func TestServingPrepareInvoke(t *testing.T) {
	s := newSupervisor(t, nil)
	prep := supPrep("rep_1", `counter = [0]
def main(input):
    counter[0] += 1
    return {"n": counter[0], "seed": fs.read("seed.txt")}
`, map[string][]byte{"seed.txt": []byte("v")})
	resp, err := s.Prepare(context.Background(), prep)
	if err != nil || resp.Result.Error != nil {
		t.Fatalf("prepare: %v %+v", err, resp.Result.Error)
	}
	for i := 0; i < 3; i++ {
		out, err := s.Invoke(context.Background(), supInv("rep_1", "inv-"+string(rune('0'+i)), `{}`))
		if err != nil || out.Result.Error != nil {
			t.Fatalf("invoke %d: %v %+v", i, err, out.Result.Error)
		}
		if !strings.Contains(string(out.Result.Output), `"n":1`) || !strings.Contains(string(out.Result.Output), `"seed":"v"`) {
			t.Fatalf("call %d stale state: %s", i, out.Result.Output)
		}
	}
	if s.Count() != 1 {
		t.Fatalf("count %d", s.Count())
	}
}

func TestServingFailureResults(t *testing.T) {
	s := newSupervisor(t, nil)
	prep := supPrep("rep_2", `print("top")
def main(input):
    return {}
`, nil)
	resp, err := s.Prepare(context.Background(), prep)
	if err != nil {
		t.Fatalf("prepare should return a result, got %v", err)
	}
	if resp.Result.Error == nil || resp.Result.Error.Code != "invalid_program" {
		t.Fatalf("expected invalid_program, got %+v", resp.Result.Error)
	}
	if len(resp.Audit.Events) == 0 {
		t.Fatal("failed prepare lost its denial audit")
	}
	if s.Count() != 0 {
		t.Fatalf("failed prepare left child: %d", s.Count())
	}

	// An allowed op followed by a runtime failure still carries audit.
	prep2 := supPrep("rep_3", `def main(input):
    fs.write("/work/x", "d")
    return 1 / 0
`, nil)
	r2, err := s.Prepare(context.Background(), prep2)
	if err != nil || r2.Result.Error != nil {
		t.Fatalf("prepare2: %v %+v", err, r2.Result.Error)
	}
	out, err := s.Invoke(context.Background(), supInv("rep_3", "i1", `{}`))
	if err != nil || out.Result.Error == nil || out.Result.Error.Code != "runtime" {
		t.Fatalf("invoke: %v %+v", err, out.Result.Error)
	}
	found := false
	for _, e := range out.Audit.Events {
		if e.Capability == "fs" {
			found = true
		}
	}
	if !found {
		t.Fatal("fs audit missing")
	}
}

func TestServingFencesAndDeath(t *testing.T) {
	s := newSupervisor(t, nil)
	prep := supPrep("rep_4", `def main(input): return {}`, nil)
	if _, err := s.Prepare(context.Background(), prep); err != nil {
		t.Fatal(err)
	}
	bad := supInv("rep_4", "i1", `{}`)
	bad.ReleaseID = "rel-2"
	if _, err := s.Invoke(context.Background(), bad); !errors.Is(err, runtime.ErrStaleEpoch) {
		t.Fatalf("stale release: %v", err)
	}
	bad = supInv("rep_4", "i1", `{}`)
	bad.Owner.UserID = "other"
	if _, err := s.Invoke(context.Background(), bad); !errors.Is(err, runtime.ErrNotOwner) {
		t.Fatalf("wrong owner: %v", err)
	}
	// Kill the child: invoke must report not-running/not-found, no respawn.
	s.mu.Lock()
	p := s.procs["rep_4"]
	s.mu.Unlock()
	s.kill(p)
	time.Sleep(100 * time.Millisecond)
	if _, err := s.Invoke(context.Background(), supInv("rep_4", "i2", `{}`)); err == nil {
		t.Fatal("invoke on dead child succeeded")
	}
	if s.Count() != 0 {
		t.Fatalf("count %d after kill", s.Count())
	}
}

func TestServingHardTimeout(t *testing.T) {
	s := newSupervisor(t, nil)
	prep := supPrep("rep_h", `def main(i):
    if i["spin"]:
        while True:
            pass
    return {}
`, nil)
	prep.Spec = []byte(`{"ttl_seconds":300,"limits":{"exec_timeout_ms":30000,"max_steps":1152921504606846976},"capabilities":{"io":{}}}`)
	resp, err := s.Prepare(context.Background(), prep)
	if err != nil || resp.Result.Error != nil {
		t.Fatalf("prepare: %v %+v", err, resp.Result.Error)
	}
	// Soft script timeout: result timeout, child survives.
	out, err := s.Invoke(context.Background(), &runtime.InvokeRequest{InstanceID: "rep_h", Owner: owner,
		ReleaseID: "rel-1", Generation: 1, InvokeID: "soft", TimeoutMs: 10,
		Input: json.RawMessage(`{"spin":true}`)})
	if err != nil || out.Result.Error == nil || out.Result.Error.Code != "timeout" {
		t.Fatalf("soft: %v %+v", err, out.Result.Error)
	}
	// Future call proves the child is still alive.
	ok, err := s.Invoke(context.Background(), &runtime.InvokeRequest{InstanceID: "rep_h", Owner: owner,
		ReleaseID: "rel-1", Generation: 1, InvokeID: "ok", Input: json.RawMessage(`{"spin":false}`)})
	if err != nil || ok.Result.Error != nil {
		t.Fatalf("post-soft call: %v %+v", err, ok.Result.Error)
	}
	// Hard timeout kills the unresponsive child entirely.
	s.o.HardExecTimeout = 50 * time.Millisecond
	_, err = s.Invoke(context.Background(), &runtime.InvokeRequest{InstanceID: "rep_h", Owner: owner,
		ReleaseID: "rel-1", Generation: 1, InvokeID: "hard",
		Input: json.RawMessage(`{"spin":true}`)})
	if !errors.Is(err, runtime.ErrNotFound) {
		t.Fatalf("hard-timeout must report the exited child: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if s.Count() != 0 {
		t.Fatalf("count %d after hard kill", s.Count())
	}
}

func TestServingStoppedSupervisor(t *testing.T) {
	s := newSupervisor(t, nil)
	s.Close()
	_, err := s.Prepare(context.Background(), supPrep("rep_s", `def main(i): return {}`, nil))
	if !errors.Is(err, runtime.ErrNotRunning) {
		t.Fatalf("expected not_running, got %v", err)
	}
	if s.Count() != 0 {
		t.Fatalf("child escaped close: %d", s.Count())
	}
}

// TestServingWaitingRoundTrip verifies the waiting outcome survives the
// subprocess transport untouched.
func TestServingWaitingRoundTrip(t *testing.T) {
	s := newSupervisor(t, nil)
	prep := supPrep("rep_w", `def main(input):
    req = interrupt.new("approval", title = "go?")
    interrupt.wait(req["id"], handler = "on_ok", context = {"k": "v"})

def on_ok(ctx):
    return {"status": "ok"}
`, nil)
	prep.Program.Handlers = map[string]runtime.HandlerSpec{"on_ok": {}}
	resp, err := s.Prepare(context.Background(), prep)
	if err != nil || resp.Result.Error != nil {
		t.Fatalf("prepare: %v %+v", err, resp.Result.Error)
	}
	inv := supInv("rep_w", "inv-w", `{}`)
	inv.RunID = "run_1"
	inv.Interrupts = []runtime.InterruptSlot{{ID: "int_1", ConsoleURL: "https://console/int_1"}}
	out, err := s.Invoke(context.Background(), inv)
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	w := out.Result.Waiting
	if w == nil || w.InterruptID != "int_1" || w.Handler != "on_ok" || w.Kind != "approval" ||
		w.Title != "go?" || string(w.Context) != `{"k":"v"}` {
		t.Fatalf("waiting lost in transport: %+v err=%+v", w, out.Result.Error)
	}
	if out.Result.Output != nil || out.Result.Error != nil {
		t.Fatalf("waiting carried extra outcome: %+v", out.Result)
	}
}
