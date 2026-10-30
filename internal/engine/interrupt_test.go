package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"calcside/internal/secrets"
)

// interruptConfig is a program that reserves an approval interrupt and
// waits on it, with two declared handlers.
func interruptConfig() ProgramConfig {
	return ProgramConfig{
		Sources: map[string][]byte{
			"main.star": []byte(`def main(input):
    rid = interrupt.run_id()
    req = interrupt.new(
        "approval",
        title = "Approve order",
        assignees = ["usr_a", "usr_b"],
        event_schema = {"type": "object"},
        expires_in_s = 60,
    )
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
		Handlers: map[string]json.RawMessage{
			"on_ok":  json.RawMessage(`{"type":"object","required":["order_id"],"properties":{"order_id":{"type":"string"}}}`),
			"on_exp": nil,
		},
	}
}

func invokeCall(id string) ProgramCall {
	return ProgramCall{
		ID:    id,
		RunID: "run_1",
		Interrupts: []InterruptSlot{
			{ID: "int_1", ConsoleURL: "https://console/int_1"},
			{ID: "int_2", ConsoleURL: "https://console/int_2", CallbackURL: "https://api/cb/int_2", Token: "cb_secret_tok"},
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
	if w.InterruptID != "int_1" || w.Handler != "on_ok" || w.OnExpire != "on_exp" ||
		w.Kind != "approval" || w.Title != "Approve order" || w.ExpiresInS != 60 {
		t.Fatalf("bad waiting: %+v", w)
	}
	if len(w.Assignees) != 2 || w.Assignees[0] != "usr_a" || w.Assignees[1] != "usr_b" {
		t.Fatalf("assignees: %v", w.Assignees)
	}
	if string(w.EventSchema) != `{"type":"object"}` {
		t.Fatalf("event_schema: %s", w.EventSchema)
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

func TestProgramInterruptCallbackTokenRedacted(t *testing.T) {
	e := New(4)
	deps, creation := progEnv(t, &spyFactory{}, nil, nil, nil)
	cfg := interruptConfig()
	cfg.Sources["main.star"] = []byte(`def main(input):
    req = interrupt.new("callback")
    print("cb=" + req["callback_token"])
    return {"tok": req["callback_token"]}

def on_ok(ctx):
    return {}
`)
	cfg.Handlers = map[string]json.RawMessage{"on_ok": nil}
	p, _ := prepare(t, e, cfg, deps, creation, ProgramCall{ID: "prep-1"})

	// Reserve the callback slot by skipping the approval slot: the
	// first new() claims int_1; call new twice so the callback claims
	// int_2.
	cfg.Sources["main.star"] = []byte(`def main(input):
    interrupt.new("approval")
    req = interrupt.new("callback")
    print("cb=" + req["callback_token"])
    return {"tok": req["callback_token"]}

def on_ok(ctx):
    return {}
`)
	p, _ = prepare(t, e, cfg, deps, creation, ProgramCall{ID: "prep-2"})
	res := e.InvokeProgram(context.Background(), p, json.RawMessage(`{}`), deps, creation, invokeCall("inv-1"))
	if res.Error != nil {
		t.Fatalf("invoke: %+v", res.Error)
	}
	if strings.Contains(res.Stdout, "cb_secret_tok") || !strings.Contains(res.Stdout, "[REDACTED]") {
		t.Fatalf("token leaked to stdout: %q", res.Stdout)
	}
	if strings.Contains(string(res.Output), "cb_secret_tok") || !strings.Contains(string(res.Output), "[REDACTED]") {
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
`
	cases := []struct {
		name    string
		body    string
		call    ProgramCall
		wantSub string // optional substring of the eval error (message is generic in result)
	}{
		{"wait unreserved", `    interrupt.wait("int_9", handler = "on_ok", context = {})`, invokeCall("i"), "not reserved"},
		{"undeclared handler", `    req = interrupt.new("approval")
    interrupt.wait(req["id"], handler = "nope", context = {})`, invokeCall("i"), "not declared"},
		{"bad context", `    req = interrupt.new("approval")
    interrupt.wait(req["id"], handler = "on_ok", context = {})`, invokeCall("i"), "handler schema"},
		{"no run", `    interrupt.new("approval")`, ProgramCall{ID: "i"}, "application runs"},
		{"run_id no run", `    interrupt.run_id()`, ProgramCall{ID: "i"}, "application runs"},
		{"slot limit", `    interrupt.new("approval")`, ProgramCall{ID: "i", RunID: "run_1"}, "interrupt limit"},
		{"timer no fire", `    interrupt.new("timer")`, invokeCall("i"), "fire_in_s"},
		{"approval fire", `    interrupt.new("approval", fire_in_s = 5)`, invokeCall("i"), "fire_in_s"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := interruptConfig()
			cfg.Sources["main.star"] = []byte(strings.Replace(base, "%s", tc.body, 1))
			cfg.Handlers = map[string]json.RawMessage{
				"on_ok": json.RawMessage(`{"type":"object","required":["k"],"properties":{"k":{"type":"string"}}}`),
			}
			p, _ := prepare(t, e, cfg, deps, creation, ProgramCall{ID: "prep-" + tc.name})
			res := e.InvokeProgram(context.Background(), p, json.RawMessage(`{}`), deps, creation, tc.call)
			if res.Error == nil {
				t.Fatalf("expected error, got output=%s waiting=%+v", res.Output, res.Waiting)
			}
			if res.Waiting != nil {
				t.Fatalf("failed call recorded waiting: %+v", res.Waiting)
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
    req = interrupt.new("approval")
    interrupt.wait(req["id"], handler = "on_ok", context = {"k": "s3cr3t"})

def on_ok(ctx):
    return {}
`)
	cfg.Handlers = map[string]json.RawMessage{"on_ok": nil}
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
`)
	cfg.Handlers = map[string]json.RawMessage{
		"on_ok": json.RawMessage(`{"type":"object","required":["order_id"],"properties":{"order_id":{"type":"string"}}}`),
	}
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

	// A context that fails the handler schema is invalid_input.
	bad := e.InvokeProgram(context.Background(), p, json.RawMessage(`{"context":{"other":1}}`), deps, creation,
		ProgramCall{ID: "inv-2", Entry: "on_ok", RunID: "run_1", Step: 1})
	if bad.Error == nil || bad.Error.Code != "invalid_input" {
		t.Fatalf("bad context: %+v", bad.Error)
	}

	// A non-object or context-less input is invalid_input.
	for _, raw := range []string{`[]`, `{"run_id":"r"}`} {
		r := e.InvokeProgram(context.Background(), p, json.RawMessage(raw), deps, creation,
			ProgramCall{ID: "inv-3" + raw, Entry: "on_ok", RunID: "run_1", Step: 1})
		if r.Error == nil || r.Error.Code != "invalid_input" {
			t.Fatalf("input %s: %+v", raw, r.Error)
		}
	}

	// Unknown entry is invalid_input.
	unk := e.InvokeProgram(context.Background(), p, json.RawMessage(`{"context":{"order_id":"o"}}`), deps, creation,
		ProgramCall{ID: "inv-4", Entry: "ghost", RunID: "run_1", Step: 1})
	if unk.Error == nil || unk.Error.Code != "invalid_input" {
		t.Fatalf("unknown entry: %+v", unk.Error)
	}
}

func TestProgramHandlerDeclarations(t *testing.T) {
	e := New(4)
	deps, creation := progEnv(t, &spyFactory{}, nil, nil, nil)
	src := `def main(input):
    return {}

def on_ok(ctx):
    return {}
`
	badHandler := func(handlers map[string]json.RawMessage, s string) ProgramResult {
		cfg := interruptConfig()
		cfg.Sources["main.star"] = []byte(s)
		cfg.Handlers = handlers
		_, res := e.PrepareProgram(context.Background(), cfg, deps, creation, ProgramCall{ID: "p"})
		return res
	}
	for name, handlers := range map[string]map[string]json.RawMessage{
		"underscore":  {"_x": nil},
		"main":        {"main": nil},
		"bad ident":   {"1bad": nil},
		"non object":  {"on_ok": json.RawMessage(`{"type":"string"}`)},
		"non compile": {"on_ok": json.RawMessage(`{"type":"object","pattern":"x"}`)},
	} {
		res := badHandler(handlers, src)
		if res.Error == nil || res.Error.Code != "invalid_program" {
			t.Fatalf("%s: %+v", name, res.Error)
		}
	}
	// Declared but missing in code.
	res := badHandler(map[string]json.RawMessage{"ghost": nil}, src)
	if res.Error == nil || res.Error.Code != "invalid_program" {
		t.Fatalf("missing handler: %+v", res.Error)
	}
	// Declared but wrong arity.
	res = badHandler(map[string]json.RawMessage{"on_ok": nil}, `def main(input):
    return {}

def on_ok(a, b):
    return {}
`)
	if res.Error == nil || res.Error.Code != "invalid_program" {
		t.Fatalf("arity: %+v", res.Error)
	}
	// Too many handlers.
	many := map[string]json.RawMessage{}
	for i := 0; i < 33; i++ {
		many[string(rune('a'+i%26))+strings.Repeat("x", i/26)] = nil
	}
	cfg := interruptConfig()
	cfg.Sources["main.star"] = []byte(src)
	cfg.Handlers = many
	_, res = e.PrepareProgram(context.Background(), cfg, deps, creation, ProgramCall{ID: "p2"})
	if res.Error == nil || res.Error.Code != "invalid_program" {
		t.Fatalf("handler count: %+v", res.Error)
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
	cfg.Handlers = nil
	_, res := e.PrepareProgram(context.Background(), cfg, deps, creation, ProgramCall{ID: "p"})
	if res.Error == nil || res.Error.Code != "invalid_program" {
		t.Fatalf("top-level interrupt call: %+v", res.Error)
	}
}
