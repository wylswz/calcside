package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"calcside/internal/secrets"
)

// interruptConfig is a program that reserves an interrupt and waits on
// it; on_ok and on_exp are plain top-level functions.
func interruptConfig() ProgramConfig {
	return ProgramConfig{
		Sources: map[string][]byte{
			"main.star": []byte(`def main(input):
    rid = interrupt.run_id()
    req = interrupt.new(expires_in_s = 60)
    print("rid=" + rid + " id=" + req["id"])
    interrupt.wait(
        req["id"],
        handler = "on_ok",
        on_expire = "on_exp",
        context = {"order_id": "o1"},
    )
    print("after wait")

def on_ok(ctx):
    return {"status": "ok"}

def on_exp(ctx):
    return {"status": "expired"}
`),
		},
		InputSchema:  json.RawMessage(`{"type":"object"}`),
		OutputSchema: json.RawMessage(`{"type":"object"}`),
	}
}

func invokeCall(id string) ProgramCall {
	return ProgramCall{
		ID:    id,
		RunID: "run_1",
		Interrupts: []InterruptSlot{
			{ID: "int_1", ResumeURL: "https://api/int_1/resume", Token: "resume_tok_1"},
			{ID: "int_2", ResumeURL: "https://api/int_2/resume", Token: "resume_tok_2"},
		},
	}
}

func TestProgramInterruptWaiting(t *testing.T) {
	e := New(4)
	deps, creation := progEnv(t, &spyFactory{}, nil, nil, nil)
	p, _ := prepare(t, e, interruptConfig(), deps, creation, ProgramCall{ID: "prep-1"})
	res := e.InvokeProgram(context.Background(), p, json.RawMessage(`{}`), deps, creation, invokeCall("inv-1"))
	if res.Error != nil {
		t.Fatalf("invoke: %+v", res.Error)
	}
	if res.Output != nil {
		t.Fatalf("waiting call produced output: %s", res.Output)
	}
	w := res.Waiting
	if w == nil {
		t.Fatal("no waiting outcome")
	}
	if w.InterruptID != "int_1" || w.Handler != "on_ok" || w.OnExpire != "on_exp" || w.ExpiresInS != 60 {
		t.Fatalf("bad waiting: %+v", w)
	}
	var ctx map[string]any
	if err := json.Unmarshal(w.Context, &ctx); err != nil || ctx["order_id"] != "o1" {
		t.Fatalf("context: %s %v", w.Context, err)
	}
	if strings.Contains(res.Stdout, "after wait") {
		t.Fatalf("code after wait ran: %q", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "rid=run_1 id=int_1") {
		t.Fatalf("run_id/new output missing: %q", res.Stdout)
	}
}

func TestProgramInterruptResumeTokenRedacted(t *testing.T) {
	e := New(4)
	deps, creation := progEnv(t, &spyFactory{}, nil, nil, nil)
	cfg := interruptConfig()
	cfg.Sources["main.star"] = []byte(`def main(input):
    req = interrupt.new()
    print("tok=" + req["resume_token"] + " url=" + req["resume_url"])
    return {"tok": req["resume_token"]}
`)
	p, _ := prepare(t, e, cfg, deps, creation, ProgramCall{ID: "prep-1"})
	res := e.InvokeProgram(context.Background(), p, json.RawMessage(`{}`), deps, creation, invokeCall("inv-1"))
	if res.Error != nil {
		t.Fatalf("invoke: %+v", res.Error)
	}
	if strings.Contains(res.Stdout, "resume_tok_1") || !strings.Contains(res.Stdout, "[REDACTED]") ||
		!strings.Contains(res.Stdout, "https://api/int_1/resume") {
		t.Fatalf("stdout: %q", res.Stdout)
	}
	if strings.Contains(string(res.Output), "resume_tok_1") || !strings.Contains(string(res.Output), "[REDACTED]") {
		t.Fatalf("token leaked to output: %s", res.Output)
	}
}

func TestProgramInterruptErrors(t *testing.T) {
	e := New(4)
	deps, creation := progEnv(t, &spyFactory{}, nil, nil, nil)
	base := `def main(input):
%s
def on_ok(ctx):
    return {}

def two(a, b):
    return {}

def _private(ctx):
    return {}

NOT_FN = 1
`
	cases := []struct {
		name    string
		body    string
		call    ProgramCall
		wantSub string // substring of the surfaced usage message
	}{
		{"wait unreserved", `    interrupt.wait("int_9", handler = "on_ok", context = {})`, invokeCall("i"), "not reserved"},
		{"missing handler", `    req = interrupt.new()
    interrupt.wait(req["id"], handler = "nope", context = {})`, invokeCall("i"), "top-level one-argument function"},
		{"wrong arity", `    req = interrupt.new()
    interrupt.wait(req["id"], handler = "two", context = {})`, invokeCall("i"), "top-level one-argument function"},
		{"underscore", `    req = interrupt.new()
    interrupt.wait(req["id"], handler = "_private", context = {})`, invokeCall("i"), "top-level one-argument function"},
		{"main", `    req = interrupt.new()
    interrupt.wait(req["id"], handler = "main", context = {})`, invokeCall("i"), "top-level one-argument function"},
		{"not a function", `    req = interrupt.new()
    interrupt.wait(req["id"], handler = "NOT_FN", context = {})`, invokeCall("i"), "top-level one-argument function"},
		{"bad on_expire", `    req = interrupt.new()
    interrupt.wait(req["id"], handler = "on_ok", on_expire = "nope", context = {})`, invokeCall("i"), "on_expire"},
		{"no run", `    interrupt.new()`, ProgramCall{ID: "i"}, "application runs"},
		{"run_id no run", `    interrupt.run_id()`, ProgramCall{ID: "i"}, "application runs"},
		{"no slots", `    interrupt.new()`, ProgramCall{ID: "i", RunID: "run_1"}, "--secret-key"},
		{"slot limit", `    interrupt.new()
    interrupt.new()
    interrupt.new()`, invokeCall("i"), "interrupt limit"},
		{"bad expiry", `    interrupt.new(expires_in_s = 0)`, invokeCall("i"), "expires_in_s"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := interruptConfig()
			cfg.Sources["main.star"] = []byte(strings.Replace(base, "%s", tc.body, 1))
			p, _ := prepare(t, e, cfg, deps, creation, ProgramCall{ID: "prep-" + tc.name})
			res := e.InvokeProgram(context.Background(), p, json.RawMessage(`{}`), deps, creation, tc.call)
			if res.Error == nil {
				t.Fatalf("expected error, got output=%s waiting=%+v", res.Output, res.Waiting)
			}
			if res.Waiting != nil {
				t.Fatalf("failed call recorded waiting: %+v", res.Waiting)
			}
			if res.Error.Code != "runtime" || !strings.Contains(res.Error.Message, tc.wantSub) {
				t.Fatalf("error = %+v, want runtime containing %q", res.Error, tc.wantSub)
			}
		})
	}
}

func TestProgramInterruptSecretContext(t *testing.T) {
	e := New(4)
	secs := secrets.NewSet()
	secs.Add("TOK", []byte("s3cr3t"), nil)
	deps, creation := progEnv(t, &spyFactory{}, nil, secs, nil)
	cfg := interruptConfig()
	cfg.Sources["main.star"] = []byte(`def main(input):
    req = interrupt.new()
    interrupt.wait(req["id"], handler = "on_ok", context = {"k": "s3cr3t"})

def on_ok(ctx):
    return {}
`)
	p, _ := prepare(t, e, cfg, deps, creation, ProgramCall{ID: "prep-1"})
	res := e.InvokeProgram(context.Background(), p, json.RawMessage(`{}`), deps, creation, invokeCall("inv-1"))
	if res.Error == nil || res.Waiting != nil {
		t.Fatalf("secret context accepted: %+v %+v", res.Error, res.Waiting)
	}
}

func TestProgramHandlerEntry(t *testing.T) {
	e := New(4)
	deps, creation := progEnv(t, &spyFactory{}, nil, nil, nil)
	cfg := interruptConfig()
	cfg.Sources["main.star"] = []byte(`def main(input):
    return {"status": "main"}

def on_ok(ctx):
    return {"status": ctx["context"]["order_id"], "approved": ctx["event"]["approved"], "iid": ctx["interrupt_id"]}

def two(a, b):
    return {}
`)
	p, _ := prepare(t, e, cfg, deps, creation, ProgramCall{ID: "prep-1"})

	call := ProgramCall{ID: "inv-1", Entry: "on_ok", RunID: "run_1", Step: 1}
	input := json.RawMessage(`{"run_id":"run_1","interrupt_id":"int_9","context":{"order_id":"o7"},"event":{"approved":true}}`)
	res := e.InvokeProgram(context.Background(), p, input, deps, creation, call)
	if res.Error != nil {
		t.Fatalf("handler invoke: %+v", res.Error)
	}
	var out map[string]any
	if err := json.Unmarshal(res.Output, &out); err != nil {
		t.Fatal(err)
	}
	if out["status"] != "o7" || out["approved"] != true || out["iid"] != "int_9" {
		t.Fatalf("bad handler output: %s", res.Output)
	}

	// A non-object or context-less input is invalid_input.
	for _, raw := range []string{`[]`, `{"run_id":"r"}`, `{"context":[]}`} {
		r := e.InvokeProgram(context.Background(), p, json.RawMessage(raw), deps, creation,
			ProgramCall{ID: "inv-3" + raw, Entry: "on_ok", RunID: "run_1", Step: 1})
		if r.Error == nil || r.Error.Code != "invalid_input" {
			t.Fatalf("input %s: %+v", raw, r.Error)
		}
	}

	// Names that can never be handlers are invalid_input before any
	// session work.
	for _, entry := range []string{"_x", "1bad", "a.b"} {
		r := e.InvokeProgram(context.Background(), p, json.RawMessage(`{"context":{}}`), deps, creation,
			ProgramCall{ID: "inv-n" + entry, Entry: entry, RunID: "run_1", Step: 1})
		if r.Error == nil || r.Error.Code != "invalid_input" {
			t.Fatalf("entry %q: %+v", entry, r.Error)
		}
	}
	// A well-formed name the program does not define as a one-argument
	// function fails as invalid_program.
	for _, entry := range []string{"ghost", "two"} {
		r := e.InvokeProgram(context.Background(), p, json.RawMessage(`{"context":{}}`), deps, creation,
			ProgramCall{ID: "inv-g" + entry, Entry: entry, RunID: "run_1", Step: 1})
		if r.Error == nil || r.Error.Code != "invalid_program" {
			t.Fatalf("entry %q: %+v", entry, r.Error)
		}
	}
}

func TestProgramInterruptTopLevel(t *testing.T) {
	e := New(4)
	deps, creation := progEnv(t, &spyFactory{}, nil, nil, nil)
	cfg := interruptConfig()
	cfg.Sources["main.star"] = []byte(`interrupt.run_id()
def main(input):
    return {}
`)
	_, res := e.PrepareProgram(context.Background(), cfg, deps, creation, ProgramCall{ID: "p"})
	if res.Error == nil || res.Error.Code != "invalid_program" {
		t.Fatalf("top-level interrupt call: %+v", res.Error)
	}
}
