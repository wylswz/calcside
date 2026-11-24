package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"calcside/internal/module"
)

// nbConfig builds a notebook ProgramConfig from name->source pairs.
func nbConfig(sources map[string]string) ProgramConfig {
	cfg := ProgramConfig{
		Notebook:     true,
		Sources:      map[string][]byte{},
		InputSchema:  json.RawMessage(`{"type":"object"}`),
		OutputSchema: json.RawMessage(`{"type":"object"}`),
	}
	for name, src := range sources {
		cfg.Sources[name] = []byte(src)
	}
	return cfg
}

// nbSources is the canonical notebook release: main in a non-main.star
// section, a shared helper and constant in their own sections ordered
// lexically after their consumers (proving topological init), both
// handlers in one section, and an unrelated section whose top-level
// fail must never run.
func nbSources() map[string]string {
	return map[string]string{
		// a_main sorts first but depends on z_lib and m_handlers is
		// only in handler closures.
		"a_main.star": `def main(input):
    target = "on_decision" if input.get("decide", True) else "on_timeout"
    req = interrupt.new(expires_in_s = 60)
    interrupt.wait(req["id"], handler = target,
                   context = {"order_id": input["order_id"]})
    return {"status": "not reached"}
`,
		"m_handlers.star": `def on_decision(ctx):
    status = "accepted" if ctx["event"]["approved"] else "rejected"
    return {"status": status, "order": label(ctx["context"]["order_id"])}

def on_timeout(ctx):
    return {"status": "expired", "order": ctx["context"]["order_id"]}
`,
		"y_const.star": `PREFIX = "order"`,
		"z_lib.star": `def label(order_id):
    return PREFIX + ":" + order_id
`,
		// Never in any entry's closure; its top-level fail must not
		// affect prepare or any invoke.
		"q_unused.star": `fail("must not initialize")`,
	}
}

func TestNotebookPrepareInvokeMain(t *testing.T) {
	e := New(4)
	deps, creation := progEnv(t, &spyFactory{}, nil, nil, nil)
	p, _ := prepare(t, e, nbConfig(nbSources()), deps, creation, ProgramCall{ID: "nb-prep"})

	res := e.InvokeProgram(context.Background(), p, json.RawMessage(`{"order_id":"o7","decide":true}`),
		deps, creation, invokeCall("nb-inv-1"))
	if res.Error != nil {
		t.Fatalf("invoke: %+v", res.Error)
	}
	if res.Output != nil {
		t.Fatalf("waiting call produced output: %s", res.Output)
	}
	w := res.Waiting
	if w == nil || w.InterruptID != "int_1" || w.Handler != "on_decision" {
		t.Fatalf("bad waiting: %+v", w)
	}
}

func TestNotebookHandlerInvoke(t *testing.T) {
	e := New(4)
	deps, creation := progEnv(t, &spyFactory{}, nil, nil, nil)
	p, _ := prepare(t, e, nbConfig(nbSources()), deps, creation, ProgramCall{ID: "nb-prep"})

	// A handler dispatch initializes the owning section plus its
	// providers — never main — and gets the ctx envelope.
	call := ProgramCall{ID: "nb-h-1", Entry: "on_decision", RunID: "run_1", Step: 1}
	input := json.RawMessage(`{"context":{"order_id":"o7"},"event":{"approved":true}}`)
	res := e.InvokeProgram(context.Background(), p, input, deps, creation, call)
	if res.Error != nil {
		t.Fatalf("handler invoke: %+v", res.Error)
	}
	var out map[string]any
	if err := json.Unmarshal(res.Output, &out); err != nil {
		t.Fatal(err)
	}
	if out["status"] != "accepted" || out["order"] != "order:o7" {
		t.Fatalf("bad handler output: %s", res.Output)
	}

	res = e.InvokeProgram(context.Background(), p,
		json.RawMessage(`{"context":{"order_id":"o9"},"event":{}}`), deps, creation,
		ProgramCall{ID: "nb-h-2", Entry: "on_timeout", RunID: "run_1", Step: 2})
	if res.Error != nil {
		t.Fatalf("on_timeout invoke: %+v", res.Error)
	}
	if !strings.Contains(string(res.Output), `"expired"`) {
		t.Fatalf("bad on_timeout output: %s", res.Output)
	}
}

func TestNotebookWaitDoesNotInitHandler(t *testing.T) {
	e := New(4)
	deps, creation := progEnv(t, &spyFactory{}, nil, nil, nil)
	// The handler's own provider fails at top level; a main that only
	// waits must not pay for it, but dispatching the handler must.
	src := nbSources()
	src["m_handlers.star"] = `def on_decision(ctx):
    return {"status": "ok", "b": BROKEN}

def on_timeout(ctx):
    return {"status": "expired"}
`
	src["x_broken.star"] = `BROKEN = fail("handler-only dependency must not initialize for wait")`
	delete(src, "q_unused.star")
	p, res := prepare(t, e, nbConfig(src), deps, creation, ProgramCall{ID: "nb-prep"})
	if res.Error != nil {
		t.Fatalf("prepare: %+v", res.Error)
	}
	out := e.InvokeProgram(context.Background(), p, json.RawMessage(`{"order_id":"o1","decide":true}`),
		deps, creation, invokeCall("nb-w-1"))
	if out.Error != nil || out.Waiting == nil || out.Waiting.Handler != "on_decision" {
		t.Fatalf("wait must not initialize the handler closure: err=%+v waiting=%+v", out.Error, out.Waiting)
	}
	dispatch := e.InvokeProgram(context.Background(), p,
		json.RawMessage(`{"context":{"order_id":"o1"},"event":{"approved":true}}`), deps, creation,
		ProgramCall{ID: "nb-d-1", Entry: "on_decision", RunID: "run_1", Step: 1})
	if dispatch.Error == nil {
		t.Fatalf("handler dispatch should fail on its broken dependency: %s", dispatch.Output)
	}
}

func TestNotebookWaitRejectsBadTargets(t *testing.T) {
	e := New(4)
	deps, creation := progEnv(t, &spyFactory{}, nil, nil, nil)
	base := map[string]string{
		"main.star": `def main(input):
    req = interrupt.new()
    interrupt.wait(req["id"], handler = input["h"], context = {})
`,
	}
	extra := map[string]map[string]string{
		"missing": {},
		// Two sections both define dup: ambiguous, so it is not an
		// entry — but prepare still succeeds while nothing uses it.
		"ambiguous": {
			"a_dup.star": `def dup(ctx):
    return {}
`,
			"b_dup.star": `def dup(ctx):
    return {}
`,
		},
		// A second binding in the same section disqualifies the def.
		"rebound": {
			"r.star": `def reb(ctx):
    return {}

reb = 1
`,
		},
		"wrong-arity": {
			"w.star": `def two(a, b):
    return {}
`,
		},
	}
	targets := map[string]string{
		"missing":     "ghost",
		"ambiguous":   "dup",
		"rebound":     "reb",
		"wrong-arity": "two",
	}
	for name, extraSrc := range extra {
		t.Run(name, func(t *testing.T) {
			src := map[string]string{}
			for k, v := range base {
				src[k] = v
			}
			for k, v := range extraSrc {
				src[k] = v
			}
			p, res := prepare(t, e, nbConfig(src), deps, creation, ProgramCall{ID: "nb-prep-" + name})
			if res.Error != nil {
				t.Fatalf("prepare: %+v", res.Error)
			}
			call := invokeCall("nb-inv-" + name)
			out := e.InvokeProgram(context.Background(), p,
				json.RawMessage(`{"h":"`+targets[name]+`"}`), deps, creation, call)
			if out.Error == nil {
				t.Fatalf("handler %q accepted: out=%s waiting=%+v", targets[name], out.Output, out.Waiting)
			}
			if out.Waiting != nil {
				t.Fatalf("failed wait recorded a waiting outcome: %+v", out.Waiting)
			}
		})
	}
}

func TestNotebookPrepareRejections(t *testing.T) {
	e := New(4)
	deps, creation := progEnv(t, &spyFactory{}, nil, nil, nil)
	cases := map[string]map[string]string{
		"missing dep": {
			"main.star": `def main(input):
    return {"v": never_defined}
`,
		},
		"cycle": {
			"main.star": `def main(input):
    return {"v": b_val}
`,
			"b.star": `a_val = 1
b_val = a_val_2
`,
			"a.star": `a_val_2 = b_val
`,
		},
		"no main": {
			"only.star": `def handler(ctx):
    return {}
`,
		},
		"ambiguous main": {
			"main.star": `def main(input):
    return {}
`,
			"other.star": `main = 1`,
		},
		"bad main arity": {
			"main.star": `def main(a, b):
    return {}
`,
		},
		"shadowed main": {
			"main.star": `def main(input):
    return {}

main = 1
`,
		},
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			_, res := e.PrepareProgram(context.Background(), nbConfig(src), deps, creation, ProgramCall{ID: "nb-r-" + name})
			if res.Error == nil || res.Error.Code != "invalid_program" {
				t.Fatalf("prepare should reject %s: %+v", name, res.Error)
			}
		})
	}
}

func TestNotebookMountExport(t *testing.T) {
	e := New(4)
	defer e.Close()
	deps, creation := progEnv(t, &spyFactory{}, nil, nil, nil)
	creation.Modules = map[string]module.Tree{
		"strkit": modTree(t, "strkit", 2, map[string]string{
			"u.star": "def shout(s): return s.upper()",
		}),
	}
	// z_lib re-exports a mounted name via load(); a_main reads it
	// through the section graph. LoadBindsGlobally must match the
	// provider shape AnalyzeFile reports.
	p, res := prepare(t, e, nbConfig(map[string]string{
		"a_main.star": `def main(input):
    return {"msg": shout(input["name"])}
`,
		"z_lib.star": `load("@strkit/u.star", "shout")`,
	}), deps, creation, ProgramCall{ID: "nb-mnt"})
	if res.Error != nil {
		t.Fatalf("prepare: %+v", res.Error)
	}
	out := e.InvokeProgram(context.Background(), p, json.RawMessage(`{"name":"ann"}`),
		deps, creation, ProgramCall{ID: "nb-mnt-inv"})
	if out.Error != nil {
		t.Fatalf("invoke: %+v", out.Error)
	}
	if string(out.Output) != `{"msg":"ANN"}` {
		t.Fatalf("output: %s", out.Output)
	}
}

func TestNotebookEntrypointRejected(t *testing.T) {
	e := New(4)
	deps, creation := progEnv(t, &spyFactory{}, nil, nil, nil)
	cfg := nbConfig(map[string]string{"main.star": `def main(input):
    return {}
`})
	cfg.Entrypoint = "main.star"
	_, res := e.PrepareProgram(context.Background(), cfg, deps, creation, ProgramCall{ID: "nb-ep"})
	if res.Error == nil || res.Error.Code != "invalid_program" {
		t.Fatalf("notebook + entrypoint accepted: %+v", res.Error)
	}
}

func TestNotebookUnknownHandlerEntry(t *testing.T) {
	e := New(4)
	deps, creation := progEnv(t, &spyFactory{}, nil, nil, nil)
	p, _ := prepare(t, e, nbConfig(nbSources()), deps, creation, ProgramCall{ID: "nb-prep"})
	res := e.InvokeProgram(context.Background(), p, json.RawMessage(`{"context":{}}`),
		deps, creation, ProgramCall{ID: "nb-gh", Entry: "ghost", RunID: "run_1", Step: 1})
	if res.Error == nil || res.Error.Code != "invalid_program" {
		t.Fatalf("unknown handler entry: %+v", res.Error)
	}
}
