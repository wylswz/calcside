package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"calcside/internal/capability"
	capfs "calcside/internal/capability/fs"
	"calcside/internal/secrets"
	"calcside/internal/serving"
	"calcside/internal/types"
)

// spyFactory is a test capability counting builds and gated ops.
type spyFactory struct {
	mu       sync.Mutex
	newCalls int
	opCalls  int
	closers  []*spyCloser
	gates    []*capability.Gate

	delay   time.Duration // artificial New() latency
	onNew   func()        // runs inside New after counters
	onOp    func()        // runs inside every gated op
	opBlock chan struct{} // when set, ops block until closed
}

type spyCloser struct {
	mu     sync.Mutex
	closed int
}

func (c *spyCloser) Close() error {
	c.mu.Lock()
	c.closed++
	c.mu.Unlock()
	return nil
}

func (f *spyFactory) Name() types.CapabilityName { return "probe" }
func (f *spyFactory) Ops() []capability.OpInfo {
	return []capability.OpInfo{{Name: "go"}}
}
func (f *spyFactory) ConfigFields() []capability.FieldDoc { return nil }
func (f *spyFactory) Validate(raw json.RawMessage, limits capability.ServerLimits) (any, error) {
	return struct{}{}, nil
}
func (f *spyFactory) Prompt(cfg any) string               { return "" }
func (f *spyFactory) Help(cfg any) []capability.HelpEntry { return nil }
func (f *spyFactory) New(cfg any, env capability.InstanceEnv) (starlark.Value, io.Closer, error) {
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	f.mu.Lock()
	f.newCalls++
	cl := &spyCloser{}
	f.closers = append(f.closers, cl)
	f.gates = append(f.gates, env.Gate)
	onNew := f.onNew
	f.mu.Unlock()
	if onNew != nil {
		onNew()
	}
	b := starlark.NewBuiltin("probe.go", func(thread *starlark.Thread, bi *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		return env.Gate.Invoke(capability.ThreadContext(thread), "probe", "go", nil,
			func(ctx context.Context, _ map[string]any) (starlark.Value, map[string]any, error) {
				f.mu.Lock()
				f.opCalls++
				onOp := f.onOp
				block := f.opBlock
				f.mu.Unlock()
				if onOp != nil {
					onOp()
				}
				if block != nil {
					<-block
				}
				return starlark.None, map[string]any{}, nil
			})
	})
	m := &starlarkstruct.Module{Name: "probe", Members: starlark.StringDict{"go": b}}
	return m, cl, nil
}

func (f *spyFactory) counts() (news, ops int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.newCalls, f.opCalls
}

func (f *spyFactory) anyArmed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, g := range f.gates {
		if g.Armed() {
			return true
		}
	}
	return false
}

func (f *spyFactory) closedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.closers {
		c.mu.Lock()
		n += c.closed
		c.mu.Unlock()
	}
	return n
}

// recObserver records gate observations.
type recObserver struct {
	mu   sync.Mutex
	recs []capability.Record
}

func (o *recObserver) Observe(r capability.Record) {
	o.mu.Lock()
	o.recs = append(o.recs, r)
	o.mu.Unlock()
}

func (o *recObserver) all() []capability.Record {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.recs)
}

// progEnv builds a registry with fs + probe and matching deps/creation.
func progEnv(t *testing.T, probe *spyFactory, obs capability.Observer, secs *secrets.Set, hooks []capability.Hook) (SessionDeps, SessionCreation) {
	t.Helper()
	reg := capability.NewRegistry()
	reg.Register(capfs.Factory())
	reg.Register(probe)
	fsCfg, err := capfs.Factory().Validate(nil, capability.ServerLimits{MaxFSQuotaBytes: 256 << 20})
	if err != nil {
		t.Fatal(err)
	}
	probeCfg, err := probe.Validate(nil, capability.ServerLimits{})
	if err != nil {
		t.Fatal(err)
	}
	deps := SessionDeps{
		Context:  context.Background(),
		Registry: reg,
		Limits:   capability.ServerLimits{MaxFSQuotaBytes: 256 << 20},
		Hooks:    hooks,
		Observer: obs,
		Secrets:  secs,
	}
	creation := SessionCreation{
		InstanceID:   "ins_p",
		UserID:       "owner",
		UserEmail:    "o@x",
		Capabilities: map[string]any{"fs": fsCfg, "probe": probeCfg},
		MaxSteps:     10_000_000,
	}
	return deps, creation
}

func greetConfig() ProgramConfig {
	return ProgramConfig{
		Sources: map[string][]byte{
			"main.star": []byte(`def main(input):
    tags = input.get("tags", [])
    return {"greeting": "hello, " + input["name"], "tag_count": len(tags)}
`),
		},
		InputSchema:  json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","minLength":1},"tags":{"type":"array","items":{"type":"string"},"maxItems":16}},"required":["name"],"additionalProperties":false}`),
		OutputSchema: json.RawMessage(`{"type":"object","properties":{"greeting":{"type":"string"},"tag_count":{"type":"integer"}},"required":["greeting","tag_count"],"additionalProperties":false}`),
	}
}

func prepare(t *testing.T, e *Engine, cfg ProgramConfig, deps SessionDeps, creation SessionCreation, call ProgramCall) (*Program, ProgramResult) {
	t.Helper()
	p, res := e.PrepareProgram(context.Background(), cfg, deps, creation, call)
	if res.Error != nil {
		t.Fatalf("prepare: %+v", res.Error)
	}
	if p == nil {
		t.Fatal("nil program")
	}
	return p, res
}

func TestProgramPrepareInvoke(t *testing.T) {
	e := New(4)
	probe := &spyFactory{}
	deps, creation := progEnv(t, probe, nil, nil, nil)
	p, res := prepare(t, e, greetConfig(), deps, creation, ProgramCall{ID: "prep-1"})
	if res.Steps <= 0 {
		t.Fatal("expected steps recorded")
	}
	out := e.InvokeProgram(context.Background(), p, json.RawMessage(`{"name":"ann","tags":["a","b"]}`), deps, creation, ProgramCall{ID: "inv-1"})
	if out.Error != nil {
		t.Fatalf("invoke: %+v", out.Error)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Output, &got); err != nil {
		t.Fatal(err)
	}
	if got["greeting"] != "hello, ann" || got["tag_count"] != float64(2) {
		t.Fatalf("bad output: %s", out.Output)
	}
	if out.DurationMs < 0 {
		t.Fatal("missing metrics")
	}
	news, ops := probe.counts()
	if news != 2 { // one session for prepare, one for invoke
		t.Fatalf("expected 2 session builds, got %d", news)
	}
	if ops != 0 {
		t.Fatalf("greeting program ran %d probe ops", ops)
	}
}

func TestPrepareNeverRunsMain(t *testing.T) {
	e := New(4)
	probe := &spyFactory{}
	deps, creation := progEnv(t, probe, nil, nil, nil)
	cfg := greetConfig()
	cfg.OutputSchema = json.RawMessage(`{}`)
	cfg.Sources["main.star"] = []byte(`def main(input):
    probe.go()
    return {}
`)
	// A successful prepare runs only pure init: the gated op inside
	// main is never executed.
	p, res := e.PrepareProgram(context.Background(), cfg, deps, creation, ProgramCall{ID: "p1"})
	if res.Error != nil {
		t.Fatalf("prepare: %+v", res.Error)
	}
	_, ops := probe.counts()
	if ops != 0 {
		t.Fatalf("prepare ran %d main ops", ops)
	}
	out := e.InvokeProgram(context.Background(), p, json.RawMessage(`{"name":"x"}`), deps, creation, ProgramCall{ID: "p1-inv"})
	if out.Error != nil {
		t.Fatalf("invoke: %+v", out.Error)
	}
	_, ops = probe.counts()
	if ops != 1 {
		t.Fatalf("invoke ran %d ops, want 1", ops)
	}
}

func TestPrepareDeniedTopLevel(t *testing.T) {
	e := New(4)
	probe := &spyFactory{}
	obs := &recObserver{}
	deps, creation := progEnv(t, probe, obs, nil, nil)
	cfg := greetConfig()
	cfg.Sources["main.star"] = []byte(`fs.write("/work/x.txt", "data")
def main(input):
    return {}
`)
	_, res := e.PrepareProgram(context.Background(), cfg, deps, creation, ProgramCall{ID: "p1"})
	if res.Error == nil || res.Error.Code != "invalid_program" {
		t.Fatalf("expected invalid_program, got %+v", res.Error)
	}
	recs := obs.all()
	found := false
	for _, r := range recs {
		if r.Decision == types.DecisionDeny && r.Call.Capability == types.CapFS {
			found = true
		}
	}
	if !found {
		t.Fatal("observer did not see the denied top-level op")
	}
}

func TestProgramMainShape(t *testing.T) {
	e := New(4)
	deps, creation := progEnv(t, &spyFactory{}, nil, nil, nil)
	for _, tc := range []struct {
		name string
		src  string
	}{
		{"missing", `x = 1`},
		{"nonfunction", `main = 5`},
		{"zero params", `def main(): return {}`},
		{"two params", `def main(a, b): return {}`},
		{"varargs", `def main(*args): return {}`},
		{"kwargs", `def main(**kwargs): return {}`},
		{"kwonly", `def main(a, *, b): return {}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := greetConfig()
			cfg.Sources["main.star"] = []byte(tc.src)
			_, res := e.PrepareProgram(context.Background(), cfg, deps, creation, ProgramCall{ID: "p-" + tc.name})
			if res.Error == nil || res.Error.Code != "invalid_program" {
				t.Fatalf("expected invalid_program, got %+v", res.Error)
			}
		})
	}
}

func TestProgramConfigErrors(t *testing.T) {
	e := New(4)
	deps, creation := progEnv(t, &spyFactory{}, nil, nil, nil)
	base := greetConfig
	for _, tc := range []struct {
		name   string
		mutate func(*ProgramConfig)
	}{
		{"syntax", func(c *ProgramConfig) { c.Sources["main.star"] = []byte("def (:") }},
		{"no sources", func(c *ProgramConfig) { c.Sources = nil }},
		{"entrypoint absent", func(c *ProgramConfig) { c.Entrypoint = "other.star" }},
		{"entrypoint not star", func(c *ProgramConfig) {
			c.Entrypoint = "main.txt"
			c.Sources["main.txt"] = c.Sources["main.star"]
		}},
		{"source absolute", func(c *ProgramConfig) { c.Sources["/abs.star"] = []byte("x=1") }},
		{"source traversal", func(c *ProgramConfig) { c.Sources["a/../b.star"] = []byte("x=1") }},
		{"source dotdot", func(c *ProgramConfig) { c.Sources[".."] = []byte("x=1") }},
		{"source backslash", func(c *ProgramConfig) { c.Sources["a\\b.star"] = []byte("x=1") }},
		{"source not star", func(c *ProgramConfig) { c.Sources["lib.txt"] = []byte("x=1") }},
		{"asset abs", func(c *ProgramConfig) { c.Assets = map[string][]byte{"/etc/x": []byte("a")} }},
		{"asset dup source", func(c *ProgramConfig) { c.Assets = map[string][]byte{"main.star": []byte("a")} }},
		{"ancestor", func(c *ProgramConfig) {
			c.Assets = map[string][]byte{"dir": []byte("a"), "dir/f.txt": []byte("b")}
		}},
		{"missing input schema", func(c *ProgramConfig) { c.InputSchema = nil }},
		{"empty input schema", func(c *ProgramConfig) { c.InputSchema = json.RawMessage("") }},
		{"bad input schema", func(c *ProgramConfig) { c.InputSchema = json.RawMessage(`{"type":"nope"}`) }},
		{"missing output schema", func(c *ProgramConfig) { c.OutputSchema = nil }},
		{"bad output schema", func(c *ProgramConfig) { c.OutputSchema = json.RawMessage(`{"$ref":"#"}`) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base()
			tc.mutate(&cfg)
			_, res := e.PrepareProgram(context.Background(), cfg, deps, creation, ProgramCall{ID: "p-" + tc.name})
			if res.Error == nil || res.Error.Code != "invalid_program" {
				t.Fatalf("expected invalid_program, got %+v", res.Error)
			}
		})
	}
}

func TestProgramLoads(t *testing.T) {
	e := New(4)
	deps, creation := progEnv(t, &spyFactory{}, nil, nil, nil)

	bad := func(cfg ProgramConfig, id string) {
		t.Helper()
		_, res := e.PrepareProgram(context.Background(), cfg, deps, creation, ProgramCall{ID: id})
		if res.Error == nil || res.Error.Code != "invalid_program" {
			t.Fatalf("expected invalid_program for %s, got %+v", id, res.Error)
		}
	}

	// Nested selected file loads and initializes once.
	cfg := greetConfig()
	// Two modules both loading the same dep share its globals: if the
	// dep re-initialized, each would see an empty list.
	cfg.Sources["lib/c.star"] = []byte(`shared = []
GREETING = "hi"`)
	cfg.Sources["a.star"] = []byte(`load("lib/c.star", "shared")
shared.append("a")
x = 1`)
	cfg.Sources["b.star"] = []byte(`load("lib/c.star", "shared")
shared.append("b")
y = 1`)
	cfg.Sources["main.star"] = []byte(`load("a.star", "x")
load("b.star", "y")
load("lib/c.star", "shared", "GREETING")
def main(input):
    return {"greeting": GREETING + " " + input["name"], "tag_count": len(shared)}
`)
	p, _ := prepare(t, e, cfg, deps, creation, ProgramCall{ID: "ld-ok"})
	out := e.InvokeProgram(context.Background(), p, json.RawMessage(`{"name":"x"}`), deps, creation, ProgramCall{ID: "ld-ok-inv"})
	if out.Error != nil {
		t.Fatalf("invoke: %+v", out.Error)
	}
	if !strings.Contains(string(out.Output), `"tag_count":2`) {
		t.Fatalf("module not initialized exactly once: %s", out.Output)
	}

	// Missing / cycle / traversal / external loads all fail.
	for _, tc := range []struct {
		name string
		src  string
	}{
		{"missing", `load("nope.star", "x")`},
		{"cycle-a", `load("b.star", "x")`}, // paired with b.star below
		{"absolute", `load("/abs.star", "x")`},
		{"traversal", `load("../x.star", "x")`},
		{"file-url", `load("file:///etc/passwd", "x")`},
		{"net", `load("https://x.invalid/a.star", "x")`},
		{"no-ext", `load("lib/defs.txt", "x")`},
	} {
		cfg := greetConfig()
		cfg.Sources["main.star"] = []byte(tc.src + "\ndef main(input):\n    return {}")
		if tc.name == "cycle-a" {
			cfg.Sources["b.star"] = []byte(`load("main.star", "main")
x = 1`)
		}
		bad(cfg, "ld-"+tc.name)
	}
}

func TestProgramFreshState(t *testing.T) {
	e := New(4)
	deps, creation := progEnv(t, &spyFactory{}, nil, nil, nil)
	cfg := greetConfig()
	cfg.OutputSchema = json.RawMessage(`{}`)
	cfg.Assets = map[string][]byte{"counter.txt": []byte("seed")}
	cfg.Sources["main.star"] = []byte(`counter = [0]
def main(input):
    counter[0] += 1
    seed = fs.read("counter.txt")
    scratch_seen = fs.exists("scratch.txt")
    fs.write("counter.txt", "mutated")
    fs.write("scratch.txt", "x")
    return {"n": counter[0], "seed": seed, "scratch_seen": scratch_seen}
`)
	p, _ := prepare(t, e, cfg, deps, creation, ProgramCall{ID: "fs-prep"})
	for i, id := range []string{"fs-1", "fs-2", "fs-3"} {
		out := e.InvokeProgram(context.Background(), p, json.RawMessage(`{"name":"x"}`), deps, creation, ProgramCall{ID: id})
		if out.Error != nil {
			t.Fatalf("invoke %d: %+v", i, out.Error)
		}
		got := string(out.Output)
		// Fresh globals and fresh VFS every call: counter is 1, the
		// seeded asset reads "seed" (never the prior mutation), and
		// scratch.txt never survives between calls.
		for _, want := range []string{`"n":1`, `"seed":"seed"`, `"scratch_seen":false`} {
			if !strings.Contains(got, want) {
				t.Fatalf("call %d: missing %s in %s", i, want, got)
			}
		}
	}
}

func TestProgramSourcesNotInFS(t *testing.T) {
	e := New(4)
	deps, creation := progEnv(t, &spyFactory{}, nil, nil, nil)
	cfg := greetConfig()
	cfg.Sources["main.star"] = []byte(`def main(input):
    r = fs.read("main.star")
    return {"greeting": str(r == None), "tag_count": 0}
`)
	p, _ := prepare(t, e, cfg, deps, creation, ProgramCall{ID: "sn-prep"})
	out := e.InvokeProgram(context.Background(), p, json.RawMessage(`{"name":"x"}`), deps, creation, ProgramCall{ID: "sn-1"})
	if out.Error == nil || out.Error.Code != "runtime" {
		t.Fatalf("expected runtime error reading absent source, got %+v", out.Error)
	}
}

func TestProgramAssetSeeded(t *testing.T) {
	e := New(4)
	deps, creation := progEnv(t, &spyFactory{}, nil, nil, nil)
	cfg := greetConfig()
	cfg.Assets = map[string][]byte{"data/blob.txt": []byte("seed")}
	cfg.Sources["main.star"] = []byte(`def main(input):
    return {"greeting": fs.read("data/blob.txt"), "tag_count": 0}
`)
	p, _ := prepare(t, e, cfg, deps, creation, ProgramCall{ID: "as-prep"})
	out := e.InvokeProgram(context.Background(), p, json.RawMessage(`{"name":"x"}`), deps, creation, ProgramCall{ID: "as-1"})
	if out.Error != nil {
		t.Fatalf("invoke: %+v", out.Error)
	}
	if !strings.Contains(string(out.Output), "seed") {
		t.Fatalf("asset not seeded: %s", out.Output)
	}
}

func TestProgramCallerMutation(t *testing.T) {
	e := New(4)
	deps, creation := progEnv(t, &spyFactory{}, nil, nil, nil)
	sources := map[string][]byte{"main.star": []byte(`def main(input):
    return {"greeting": "a", "tag_count": 1, "asset": fs.read("a.txt")}
`)}
	assets := map[string][]byte{"a.txt": []byte("orig")}
	cfg := ProgramConfig{
		Sources:      sources,
		Assets:       assets,
		InputSchema:  json.RawMessage(`{}`),
		OutputSchema: json.RawMessage(`{}`),
	}
	p, _ := prepare(t, e, cfg, deps, creation, ProgramCall{ID: "cm-prep"})
	// Mutate caller-held maps, byte slices AND the raw schema
	// documents in place: none must affect the immutable program.
	src0 := sources["main.star"]
	src0[0] = 'X' // now starts "Xef main..." -- would not even compile
	sources["main.star"] = []byte(`def main(input):
    return "mutated"
`)
	sources["evil.star"] = []byte(`x = 1`)
	a0 := assets["a.txt"]
	a0[0] = 'X'
	assets["a.txt"] = []byte("changed")
	assets["b.txt"] = []byte("new")
	delete(assets, "a.txt")
	cfg.InputSchema[1] = 'x'
	cfg.OutputSchema[1] = 'x'
	out := e.InvokeProgram(context.Background(), p, json.RawMessage(`{}`), deps, creation, ProgramCall{ID: "cm-1"})
	if out.Error != nil {
		t.Fatalf("invoke: %+v", out.Error)
	}
	if string(out.Output) != `{"greeting":"a","tag_count":1,"asset":"orig"}` {
		t.Fatalf("caller mutation leaked: %s", out.Output)
	}
}

func TestProgramInvalidInput(t *testing.T) {
	e := New(4)
	probe := &spyFactory{}
	obs := &recObserver{}
	deps, creation := progEnv(t, probe, obs, nil, nil)
	p, _ := prepare(t, e, greetConfig(), deps, creation, ProgramCall{ID: "ii-prep"})
	news0, _ := probe.counts()
	out := e.InvokeProgram(context.Background(), p, json.RawMessage(`{"name":""}`), deps, creation, ProgramCall{ID: "ii-1"})
	if out.Error == nil || out.Error.Code != "invalid_input" {
		t.Fatalf("expected invalid_input, got %+v", out.Error)
	}
	news, _ := probe.counts()
	if news != news0 {
		t.Fatalf("invalid input built %d sessions", news-news0)
	}
	if len(obs.all()) != 0 {
		t.Fatal("invalid input produced capability records")
	}
}

func TestProgramFrozenInput(t *testing.T) {
	e := New(4)
	deps, creation := progEnv(t, &spyFactory{}, nil, nil, nil)
	cfg := greetConfig()
	cfg.InputSchema = json.RawMessage(`{}`)
	cfg.OutputSchema = json.RawMessage(`{}`)
	cfg.Sources["main.star"] = []byte(`def main(input):
    input["l"].append(2)
    return {}
`)
	p, _ := prepare(t, e, cfg, deps, creation, ProgramCall{ID: "fz-prep"})
	out := e.InvokeProgram(context.Background(), p, json.RawMessage(`{"l":[1]}`), deps, creation, ProgramCall{ID: "fz-1"})
	if out.Error == nil || out.Error.Code != "runtime" {
		t.Fatalf("expected runtime from frozen input, got %+v", out.Error)
	}
}

func TestProgramNumericPrecision(t *testing.T) {
	e := New(4)
	deps, creation := progEnv(t, &spyFactory{}, nil, nil, nil)
	cfg := greetConfig()
	cfg.InputSchema = json.RawMessage(`{}`)
	cfg.OutputSchema = json.RawMessage(`{}`)
	cfg.Sources["main.star"] = []byte(`def main(input):
    return {"big": input["big"], "integ": input["integ"], "sum": input["big"] + 1}
`)
	p, _ := prepare(t, e, cfg, deps, creation, ProgramCall{ID: "np-prep"})
	out := e.InvokeProgram(context.Background(), p, json.RawMessage(`{"big":9007199254740993,"integ":1e0}`), deps, creation, ProgramCall{ID: "np-1"})
	if out.Error != nil {
		t.Fatalf("invoke: %+v", out.Error)
	}
	if !strings.Contains(string(out.Output), `"big":9007199254740993`) {
		t.Fatalf("precision lost: %s", out.Output)
	}
	if !strings.Contains(string(out.Output), `"sum":9007199254740994`) {
		t.Fatalf("int arithmetic lost: %s", out.Output)
	}
	// 1e0 arrives as an exact int, not a float.
	if !strings.Contains(string(out.Output), `"integ":1`) {
		t.Fatalf("integral exponent not int: %s", out.Output)
	}
}

func TestProgramNumberExponentBound(t *testing.T) {
	e := New(4)
	probe := &spyFactory{}
	deps, creation := progEnv(t, probe, nil, nil, nil)
	cfg := greetConfig()
	cfg.InputSchema = json.RawMessage(`{}`)
	p, _ := prepare(t, e, cfg, deps, creation, ProgramCall{ID: "of-prep"})
	news0, _ := probe.counts()
	out := e.InvokeProgram(context.Background(), p, json.RawMessage(`{"n":1.1e1025}`), deps, creation, ProgramCall{ID: "of-1"})
	if out.Error == nil || out.Error.Code != "invalid_input" {
		t.Fatalf("expected invalid_input, got %+v", out.Error)
	}
	news, _ := probe.counts()
	if news != news0 {
		t.Fatal("overflow input reached session construction")
	}
}

func TestProgramInvalidOutput(t *testing.T) {
	e := New(4)
	deps, creation := progEnv(t, &spyFactory{}, nil, nil, nil)
	for _, tc := range []struct {
		name string
		src  string
		in   string
	}{
		{"function", `def main(input):
    return main
`, `{}`},
		{"bytes", `def main(input):
    return b"x"
`, `{}`},
		{"cycle", `def main(input):
    l = []
    l.append(l)
    return l
`, `{}`},
		{"set", `def main(input):
    return set([1])
`, `{}`},
		{"nan", `def main(input):
    return float("nan")
`, `{}`},
		{"schema mismatch", `def main(input):
    return {"greeting": 5, "tag_count": 1}
`, `{"name":"x"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := greetConfig()
			cfg.InputSchema = json.RawMessage(`{}`)
			cfg.Sources["main.star"] = []byte(tc.src)
			p, _ := prepare(t, e, cfg, deps, creation, ProgramCall{ID: "io-" + tc.name})
			out := e.InvokeProgram(context.Background(), p, json.RawMessage(tc.in), deps, creation, ProgramCall{ID: "io-inv-" + tc.name})
			if out.Error == nil || out.Error.Code != "invalid_output" {
				t.Fatalf("expected invalid_output, got %+v", out.Error)
			}
			if out.Output != nil {
				t.Fatal("failure returned output")
			}
		})
	}
}

func TestProgramRedaction(t *testing.T) {
	e := New(4)
	probe := &spyFactory{}
	secs := secrets.NewSet()
	secs.Add("apikey", []byte("s3kr3t-value"), nil)
	deps, creation := progEnv(t, probe, nil, secs, nil)

	// Secret in output is redacted before schema validation.
	cfg := greetConfig()
	cfg.OutputSchema = json.RawMessage(`{}`)
	cfg.Sources["main.star"] = []byte(`def main(input):
    return "token=s3kr3t-value"
`)
	p, _ := prepare(t, e, cfg, deps, creation, ProgramCall{ID: "rd-prep"})
	out := e.InvokeProgram(context.Background(), p, json.RawMessage(`{"name":"x"}`), deps, creation, ProgramCall{ID: "rd-1"})
	if out.Error != nil {
		t.Fatalf("invoke: %+v", out.Error)
	}
	if strings.Contains(string(out.Output), "s3kr3t-value") || !strings.Contains(string(out.Output), "REDACTED") {
		t.Fatalf("output not redacted: %s", out.Output)
	}

	// Output const equal to the original secret: valid before
	// redaction, broken after -- validation runs on redacted bytes.
	cfgC := greetConfig()
	cfgC.OutputSchema = json.RawMessage(`{"const":"s3kr3t-value"}`)
	cfgC.Sources["main.star"] = []byte(`def main(input):
    return "s3kr3t-value"
`)
	pc, _ := prepare(t, e, cfgC, deps, creation, ProgramCall{ID: "rd-prep-c"})
	outC := e.InvokeProgram(context.Background(), pc, json.RawMessage(`{"name":"x"}`), deps, creation, ProgramCall{ID: "rd-c"})
	if outC.Error == nil || outC.Error.Code != "invalid_output" || outC.Output != nil {
		t.Fatalf("expected fail-closed invalid_output, got %+v / %s", outC.Error, outC.Output)
	}

	// A key carrying a secret collides with a literal REDACTED key:
	// post-redaction duplicate keys fail closed at DecodeJSON.
	cfgK := greetConfig()
	cfgK.OutputSchema = json.RawMessage(`{}`)
	cfgK.Sources["main.star"] = []byte(`def main(input):
    return {"s3kr3t-value": 1, "[REDACTED:apikey]": 2}
`)
	pk, _ := prepare(t, e, cfgK, deps, creation, ProgramCall{ID: "rd-prep-k"})
	outK := e.InvokeProgram(context.Background(), pk, json.RawMessage(`{"name":"x"}`), deps, creation, ProgramCall{ID: "rd-k"})
	if outK.Error == nil || outK.Error.Code != "invalid_output" || outK.Output != nil {
		t.Fatalf("expected fail-closed invalid_output, got %+v / %s", outK.Error, outK.Output)
	}

	// Numeric secret inside otherwise-valid output: redaction breaks
	// the JSON itself, never returned.
	secs2 := secrets.NewSet()
	secs2.Add("num", []byte("42"), nil)
	deps2, creation2 := progEnv(t, probe, nil, secs2, nil)
	cfg3 := greetConfig()
	cfg3.Sources["main.star"] = []byte(`def main(input):
    return {"greeting": "x", "tag_count": 42}
`)
	p3, _ := prepare(t, e, cfg3, deps2, creation2, ProgramCall{ID: "rd-prep3"})
	out3 := e.InvokeProgram(context.Background(), p3, json.RawMessage(`{"name":"x"}`), deps2, creation2, ProgramCall{ID: "rd-3"})
	if out3.Error == nil || out3.Error.Code != "invalid_output" || out3.Output != nil {
		t.Fatalf("expected fail-closed invalid_output, got %+v / %s", out3.Error, out3.Output)
	}
}

func TestProgramStdoutSanitized(t *testing.T) {
	e := New(4)
	probe := &spyFactory{}
	secs := secrets.NewSet()
	secs.Add("apikey", []byte("s3kr3t"), nil)
	deps, creation := progEnv(t, probe, nil, secs, nil)
	cfg := greetConfig()
	cfg.OutputSchema = json.RawMessage(`{}`)
	cfg.Sources["main.star"] = []byte(`def main(input):
    print("hello s3kr3t world")
    return {"greeting": "ok", "tag_count": 1}
`)
	p, _ := prepare(t, e, cfg, deps, creation, ProgramCall{ID: "so-prep"})
	out := e.InvokeProgram(context.Background(), p, json.RawMessage(`{"name":"x"}`), deps, creation, ProgramCall{ID: "so-1"})
	if out.Error != nil {
		t.Fatalf("invoke: %+v", out.Error)
	}
	if !strings.Contains(out.Stdout, "hello") || strings.Contains(out.Stdout, "s3kr3t") {
		t.Fatalf("stdout not sanitized: %q", out.Stdout)
	}

	// Runtime error still returns sanitized stdout, generic error.
	cfg.Sources["main.star"] = []byte(`def main(input):
    print("partial s3kr3t")
    return 1 / 0
`)
	p2, _ := prepare(t, e, cfg, deps, creation, ProgramCall{ID: "so-prep2"})
	out2 := e.InvokeProgram(context.Background(), p2, json.RawMessage(`{"name":"x"}`), deps, creation, ProgramCall{ID: "so-2"})
	if out2.Error == nil || out2.Error.Code != "runtime" {
		t.Fatalf("expected runtime, got %+v", out2.Error)
	}
	if strings.Contains(out2.Error.Message, "1 / 0") || strings.Contains(out2.Stdout, "s3kr3t") {
		t.Fatalf("error/stdout leaked: %v / %q", out2.Error, out2.Stdout)
	}
}

func TestProgramPolicyDenied(t *testing.T) {
	e := New(4)
	probe := &spyFactory{}
	obs := &recObserver{}
	veto := vetoFS{}
	deps, creation := progEnv(t, probe, obs, nil, []capability.Hook{veto})
	cfg := greetConfig()
	cfg.Sources["main.star"] = []byte(`def main(input):
    fs.write("/work/x.txt", "d")
    return {}
`)
	p, _ := prepare(t, e, cfg, deps, creation, ProgramCall{ID: "pd-prep"})
	out := e.InvokeProgram(context.Background(), p, json.RawMessage(`{"name":"x"}`), deps, creation, ProgramCall{ID: "pd-1", ActorID: "actor-1", ActorEmail: "a@x"})
	if out.Error == nil || out.Error.Code != "policy_denied" {
		t.Fatalf("expected policy_denied, got %+v", out.Error)
	}
	// The audit record names the actor, not the config owner.
	recs := obs.all()
	found := false
	for _, r := range recs {
		if r.Call.Capability == types.CapFS && r.Call.UserID == "actor-1" && r.Call.ExecID == "pd-1" {
			found = true
		}
	}
	if !found {
		t.Fatal("audit record missing actor identity")
	}
}

// vetoFS denies fs ops, lets everything else through.
type vetoFS struct{}

func (vetoFS) Name() string { return "veto-fs" }
func (vetoFS) Before(ctx context.Context, c *capability.Call) error {
	if c.Capability == types.CapFS {
		return errors.New("fs denied")
	}
	return nil
}
func (vetoFS) After(ctx context.Context, c *capability.Call, r *capability.Result) error { return nil }

func TestProgramStepAndTimeout(t *testing.T) {
	deps, creation := progEnv(t, &spyFactory{}, nil, nil, nil)
	cfg := greetConfig()
	cfg.Sources["main.star"] = []byte(`def main(input):
    x = 0
    while x < 100000000:
        x += 1
    return {}
`)

	// Step cap inside main -> step_limit.
	e := New(4)
	cc := creation
	cc.MaxSteps = 500
	p, _ := prepare(t, e, cfg, deps, cc, ProgramCall{ID: "st-prep"})
	out := e.InvokeProgram(context.Background(), p, json.RawMessage(`{"name":"x"}`), deps, cc, ProgramCall{ID: "st-1"})
	if out.Error == nil || out.Error.Code != "step_limit" {
		t.Fatalf("expected step_limit, got %+v", out.Error)
	}
	if out.Output != nil {
		t.Fatal("output on failure")
	}

	// while True inside main -> timeout with ambiguous-outcome text.
	cfg.Sources["main.star"] = []byte(`def main(input):
    while True:
        pass
    return {}
`)
	cc2 := creation
	cc2.MaxSteps = 1 << 60
	e2 := New(4)
	p2, _ := prepare(t, e2, cfg, deps, cc2, ProgramCall{ID: "to-prep"})
	out2 := e2.InvokeProgram(context.Background(), p2, json.RawMessage(`{"name":"x"}`), deps, cc2, ProgramCall{ID: "to-1", Timeout: 50 * time.Millisecond})
	if out2.Error == nil || out2.Error.Code != "timeout" {
		t.Fatalf("expected timeout, got %+v", out2.Error)
	}
	if !strings.Contains(out2.Error.Message, "unknown") {
		t.Fatalf("timeout message must flag ambiguous outcome: %q", out2.Error.Message)
	}
	if out2.Output != nil {
		t.Fatal("output on timeout")
	}
}

func TestProgramSemWaitDeadline(t *testing.T) {
	// One slot engine + a main that blocks inside a gated op: the
	// queued call deterministically dies waiting for the slot.
	e := New(1)
	probe := &spyFactory{}
	entered := make(chan struct{})
	release := make(chan struct{})
	probe.opBlock = release
	probe.onOp = func() { close(entered); probe.onOp = nil }
	deps, creation := progEnv(t, probe, nil, nil, nil)
	cfg := greetConfig()
	cfg.Sources["main.star"] = []byte(`def main(input):
    probe.go()
    return {}
`)
	cfg.OutputSchema = json.RawMessage(`{}`)
	p, _ := prepare(t, e, cfg, deps, creation, ProgramCall{ID: "sem-prep"})
	done := make(chan ProgramResult, 1)
	go func() {
		done <- e.InvokeProgram(context.Background(), p, json.RawMessage(`{"name":"x"}`), deps, creation, ProgramCall{ID: "sem-blocker", Timeout: 10 * time.Second})
	}()
	<-entered // first call holds the slot inside main
	out := e.InvokeProgram(context.Background(), p, json.RawMessage(`{"name":"x"}`), deps, creation, ProgramCall{ID: "sem-queued", Timeout: 50 * time.Millisecond})
	if out.Error == nil || out.Error.Code != "timeout" {
		t.Fatalf("expected timeout while queued, got %+v", out.Error)
	}
	close(release)
	if r := <-done; r.Error != nil {
		t.Fatalf("blocker: %+v", r.Error)
	}
}

func TestProgramWatchdog(t *testing.T) {
	// Deterministic cancellation through the running registry: a pure
	// init that never terminates is cancelled as memory_limit exactly.
	e := New(4)
	probe := &spyFactory{}
	probe.onNew = func() {
		e.runningMu.Lock()
		c := e.running["wd-prep"]
		e.runningMu.Unlock()
		if c != nil {
			c(ErrMemoryLimit)
		}
	}
	deps, creation := progEnv(t, probe, nil, nil, nil)
	cfg := greetConfig()
	cfg.Sources["main.star"] = []byte(`while True:
    pass
def main(input):
    return {}
`)
	p, res := e.PrepareProgram(context.Background(), cfg, deps, creation, ProgramCall{ID: "wd-prep"})
	if res.Error == nil || res.Error.Code != "memory_limit" {
		t.Fatalf("expected memory_limit, got %+v", res.Error)
	}
	if p != nil {
		t.Fatal("cancelled init must not return a program")
	}
}

func TestProgramConcurrent(t *testing.T) {
	e := New(8)
	deps, creation := progEnv(t, &spyFactory{}, nil, nil, nil)
	p, _ := prepare(t, e, greetConfig(), deps, creation, ProgramCall{ID: "cc-prep"})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("cc-%d", i)
			out := e.InvokeProgram(context.Background(), p, json.RawMessage(`{"name":"n","tags":["t"]}`), deps, creation, ProgramCall{ID: id})
			if out.Error != nil {
				t.Errorf("%s: %+v", id, out.Error)
				return
			}
			if !strings.Contains(string(out.Output), `"tag_count":1`) {
				t.Errorf("%s bad output %s", id, out.Output)
			}
		}(i)
	}
	wg.Wait()
}

func TestProgramDuplicateCallID(t *testing.T) {
	e := New(4)
	deps, creation := progEnv(t, &spyFactory{}, nil, nil, nil)
	p, _ := prepare(t, e, greetConfig(), deps, creation, ProgramCall{ID: "dup-prep"})
	// An in-flight call with the same ID is rejected before touching
	// the running registry.
	e.runningMu.Lock()
	e.running["dup-1"] = func(error) {}
	e.runningMu.Unlock()
	out := e.InvokeProgram(context.Background(), p, json.RawMessage(`{"name":"x"}`), deps, creation, ProgramCall{ID: "dup-1"})
	e.runningMu.Lock()
	delete(e.running, "dup-1")
	e.runningMu.Unlock()
	if out.Error == nil || out.Error.Code != "invalid_program" {
		t.Fatalf("expected invalid_program for duplicate id, got %+v", out.Error)
	}
}

func TestProgramCloserSpy(t *testing.T) {
	e := New(4)
	probe := &spyFactory{}
	deps, creation := progEnv(t, probe, nil, nil, nil)
	p, _ := prepare(t, e, greetConfig(), deps, creation, ProgramCall{ID: "cl-prep"})
	out := e.InvokeProgram(context.Background(), p, json.RawMessage(`{"name":"x"}`), deps, creation, ProgramCall{ID: "cl-ok"})
	if out.Error != nil {
		t.Fatalf("invoke: %+v", out.Error)
	}
	out = e.InvokeProgram(context.Background(), p, json.RawMessage(`{"bad":1}`), deps, creation, ProgramCall{ID: "cl-bad"})
	if out.Error == nil || out.Error.Code != "invalid_input" {
		t.Fatalf("expected invalid_input, got %+v", out.Error)
	}
	// Two sessions built, both closed (prepare + one invoke).
	if got := probe.closedCount(); got < 2 {
		t.Fatalf("expected at least 2 closers closed, got %d", got)
	}
}

func TestProgramCancelledContext(t *testing.T) {
	e := New(4)
	deps, creation := progEnv(t, &spyFactory{}, nil, nil, nil)
	p, _ := prepare(t, e, greetConfig(), deps, creation, ProgramCall{ID: "cx-prep"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out := e.InvokeProgram(ctx, p, json.RawMessage(`{"name":"x"}`), deps, creation, ProgramCall{ID: "cx-1"})
	if out.Error == nil || out.Output != nil {
		t.Fatalf("expected failure with no output, got %+v / %s", out.Error, out.Output)
	}
}

func TestProgramMalformed(t *testing.T) {
	e := New(4)
	deps, creation := progEnv(t, &spyFactory{}, nil, nil, nil)
	if r := e.InvokeProgram(context.Background(), nil, json.RawMessage(`{}`), deps, creation, ProgramCall{ID: "m1"}); r.Error == nil || r.Error.Code != "invalid_program" {
		t.Fatalf("nil program: %+v", r.Error)
	}
	if r := e.InvokeProgram(context.Background(), &Program{}, json.RawMessage(`{}`), deps, creation, ProgramCall{ID: "m2"}); r.Error == nil || r.Error.Code != "invalid_program" {
		t.Fatalf("empty program: %+v", r.Error)
	}
	deps.Registry = nil
	if _, r := e.PrepareProgram(context.Background(), greetConfig(), deps, creation, ProgramCall{ID: "m3"}); r.Error == nil || r.Error.Code != "invalid_program" {
		t.Fatalf("nil registry prepare: %+v", r.Error)
	}
	deps, creation = progEnv(t, &spyFactory{}, nil, nil, nil)
	p, _ := prepare(t, e, greetConfig(), deps, creation, ProgramCall{ID: "m4"})
	bad := deps
	bad.Registry = nil
	if r := e.InvokeProgram(context.Background(), p, json.RawMessage(`{"name":"x"}`), bad, creation, ProgramCall{ID: "m5"}); r.Error == nil || r.Error.Code != "invalid_program" {
		t.Fatalf("nil registry invoke: %+v", r.Error)
	}
}

func TestProgramPrecancelled(t *testing.T) {
	e := New(4)
	probe := &spyFactory{}
	deps, creation := progEnv(t, probe, nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p, res := e.PrepareProgram(ctx, greetConfig(), deps, creation, ProgramCall{ID: "pc-1"})
	if res.Error == nil || res.Error.Code != "timeout" || p != nil {
		t.Fatalf("precancelled prepare: %+v", res.Error)
	}
	p2, _ := prepare(t, e, greetConfig(), deps, creation, ProgramCall{ID: "pc-2"})
	n0, _ := probe.counts()
	out := e.InvokeProgram(ctx, p2, json.RawMessage(`{"name":"x"}`), deps, creation, ProgramCall{ID: "pc-3"})
	if out.Error == nil || out.Error.Code != "timeout" || out.Output != nil {
		t.Fatalf("precancelled invoke: %+v", out.Error)
	}
	if n, _ := probe.counts(); n != n0 {
		t.Fatal("precancelled invoke built a session")
	}
}

func TestProgramCancelPrecedence(t *testing.T) {
	e := New(4)
	probe := &spyFactory{}
	deps, creation := progEnv(t, probe, nil, nil, nil)
	cfg := greetConfig()
	cfg.OutputSchema = json.RawMessage(`{}`)
	cfg.Sources["main.star"] = []byte(`def main(input):
    probe.go()
    return main
`)
	p, _ := prepare(t, e, cfg, deps, creation, ProgramCall{ID: "cp-prep"})
	ctx, cancel := context.WithCancel(context.Background())
	probe.onOp = cancel
	out := e.InvokeProgram(ctx, p, json.RawMessage(`{"name":"x"}`), deps, creation, ProgramCall{ID: "cp-1"})
	probe.onOp = nil
	if out.Error == nil || out.Error.Code != "timeout" || out.Output != nil {
		t.Fatalf("cancel during op must win over invalid_output: %+v", out.Error)
	}
	// memory_limit cause via registry, fired deterministically from
	// the session factory once the call is registered.
	probe.onNew = func() {
		e.runningMu.Lock()
		c := e.running["cp-2"]
		e.runningMu.Unlock()
		if c != nil {
			c(ErrMemoryLimit)
		}
	}
	cfg.Sources["main.star"] = []byte(`while True:
    pass
def main(input):
    return {}
`)
	p2, res2 := e.PrepareProgram(context.Background(), cfg, deps, creation, ProgramCall{ID: "cp-2"})
	if p2 != nil || res2.Error == nil || res2.Error.Code != "memory_limit" {
		t.Fatalf("expected memory_limit + nil program, got %+v", res2.Error)
	}
}

func TestProgramCapsMismatch(t *testing.T) {
	e := New(4)
	probe := &spyFactory{}
	deps, creation := progEnv(t, probe, nil, nil, nil)
	p, _ := prepare(t, e, greetConfig(), deps, creation, ProgramCall{ID: "cmx-prep"})
	c2 := creation
	c2.Capabilities = maps.Clone(creation.Capabilities)
	delete(c2.Capabilities, "probe")
	out := e.InvokeProgram(context.Background(), p, json.RawMessage(`{"name":"x"}`), deps, c2, ProgramCall{ID: "cmx-1"})
	if out.Error == nil || out.Error.Code != "invalid_program" {
		t.Fatalf("expected invalid_program, got %+v", out.Error)
	}
	if _, ops := probe.counts(); ops != 0 {
		t.Fatalf("caps-mismatch call ran %d ops", ops)
	}
}

func TestProgramAssetsWithoutFS(t *testing.T) {
	e := New(4)
	probe := &spyFactory{}
	deps, creation := progEnv(t, probe, nil, nil, nil)
	creation.Capabilities = maps.Clone(creation.Capabilities)
	delete(creation.Capabilities, "fs")
	cfg := greetConfig()
	cfg.Assets = map[string][]byte{"a.txt": []byte("x")}
	_, res := e.PrepareProgram(context.Background(), cfg, deps, creation, ProgramCall{ID: "anf-1"})
	if res.Error == nil || res.Error.Code != "invalid_program" {
		t.Fatalf("expected invalid_program, got %+v", res.Error)
	}
}

func TestProgramConfigBounds(t *testing.T) {
	e := New(4)
	deps, creation := progEnv(t, &spyFactory{}, nil, nil, nil)
	mk := func(n int, nameFn func(i int) string) map[string][]byte {
		m := map[string][]byte{}
		for i := 0; i < n; i++ {
			m[nameFn(i)] = []byte("x=1")
		}
		return m
	}
	tooMany := greetConfig()
	tooMany.Sources = mk(65, func(i int) string { return fmt.Sprintf("s%d.star", i) })
	tooMany.Entrypoint = "s0.star"
	_, r := e.PrepareProgram(context.Background(), tooMany, deps, creation, ProgramCall{ID: "b1"})
	if r.Error == nil || r.Error.Code != "invalid_program" {
		t.Fatalf("65 sources: %+v", r.Error)
	}
	big := greetConfig()
	big.Sources["pad.star"] = make([]byte, programMaxFileBytes+1)
	_, r = e.PrepareProgram(context.Background(), big, deps, creation, ProgramCall{ID: "b2"})
	if r.Error == nil || r.Error.Code != "invalid_program" {
		t.Fatalf("per-file: %+v", r.Error)
	}
	casey := greetConfig()
	casey.Sources["Foo.star"] = []byte("x=1")
	casey.Sources["foo.star"] = []byte("x=1")
	_, r = e.PrepareProgram(context.Background(), casey, deps, creation, ProgramCall{ID: "b3"})
	if r.Error == nil || r.Error.Code != "invalid_program" {
		t.Fatalf("casefold collision: %+v", r.Error)
	}
}

func TestProgramCloserExact(t *testing.T) {
	e := New(4)
	probe := &spyFactory{}
	deps, creation := progEnv(t, probe, nil, nil, nil)
	p, _ := prepare(t, e, greetConfig(), deps, creation, ProgramCall{ID: "ce-prep"})
	e.InvokeProgram(context.Background(), p, json.RawMessage(`{"name":"x"}`), deps, creation, ProgramCall{ID: "ce-1"})
	cfg := greetConfig()
	cfg.Sources["main.star"] = []byte(`def main(input):
    return 1 / 0
`)
	p2, _ := prepare(t, e, cfg, deps, creation, ProgramCall{ID: "ce-prep2"})
	e.InvokeProgram(context.Background(), p2, json.RawMessage(`{"name":"x"}`), deps, creation, ProgramCall{ID: "ce-2"})
	n, _ := probe.counts()
	if c := probe.closedCount(); c != n {
		t.Fatalf("closed %d of %d sessions", c, n)
	}
	if probe.anyArmed() {
		t.Fatal("gate left armed")
	}
}

func TestProgramStdoutBounds(t *testing.T) {
	e := New(4)
	probe := &spyFactory{}
	secs := secrets.NewSet()
	secs.Add("k", []byte("x"), nil)
	deps, creation := progEnv(t, probe, nil, secs, nil)
	creation.MaxOutputBytes = 16
	cfg := greetConfig()
	cfg.OutputSchema = json.RawMessage(`{}`)
	cfg.Sources["main.star"] = []byte(`def main(input):
    print("xxxxxxxxxxxxxxxxxxxxxxxxxx")
    return {}
`)
	p, _ := prepare(t, e, cfg, deps, creation, ProgramCall{ID: "sb-prep"})
	out := e.InvokeProgram(context.Background(), p, json.RawMessage(`{"name":"x"}`), deps, creation, ProgramCall{ID: "sb-1"})
	if out.Error != nil || string(out.Output) != "{}" {
		t.Fatalf("invoke: %+v / %s", out.Error, out.Output)
	}
	if len(out.Stdout) > 16 || strings.Contains(out.Stdout, "xxxxxxxx") {
		t.Fatalf("stdout not safely bounded: %q", out.Stdout)
	}
}

func TestProgramDupPreserves(t *testing.T) {
	e := New(4)
	deps, creation := progEnv(t, &spyFactory{}, nil, nil, nil)
	p, _ := prepare(t, e, greetConfig(), deps, creation, ProgramCall{ID: "dp-prep"})
	fired := make(chan struct{})
	e.runningMu.Lock()
	e.running["dp-1"] = func(error) { close(fired) }
	e.runningMu.Unlock()
	out := e.InvokeProgram(context.Background(), p, json.RawMessage(`{"name":"x"}`), deps, creation, ProgramCall{ID: "dp-1"})
	e.runningMu.Lock()
	c, ok := e.running["dp-1"]
	e.runningMu.Unlock()
	if !ok {
		t.Fatal("rejected dup removed the original registry entry")
	}
	c(errors.New("probe")) // original cancel still works
	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("original cancel not preserved")
	}
	e.runningMu.Lock()
	delete(e.running, "dp-1")
	e.runningMu.Unlock()
	if out.Error == nil || out.Error.Code != "invalid_program" {
		t.Fatalf("dup: %+v", out.Error)
	}
	if len(e.sem) != 0 {
		t.Fatal("rejected dup leaked a sem slot")
	}
}

func TestProgramOversizedOutput(t *testing.T) {
	e := New(4)
	deps, creation := progEnv(t, &spyFactory{}, nil, nil, nil)
	cfg := greetConfig()
	cfg.OutputSchema = json.RawMessage(`{}`)
	cfg.JSONLimits = serving.Limits{MaxBytes: 256, MaxDepth: 32, MaxNodes: 1000}
	cfg.Sources["main.star"] = []byte(`def main(input):
    return "x" * 100000
`)
	p, _ := prepare(t, e, cfg, deps, creation, ProgramCall{ID: "oo-prep"})
	out := e.InvokeProgram(context.Background(), p, json.RawMessage(`{"name":"x"}`), deps, creation, ProgramCall{ID: "oo-1"})
	if out.Error == nil || out.Error.Code != "invalid_output" || out.Output != nil {
		t.Fatalf("oversized: %+v / %d", out.Error, len(out.Output))
	}
}

func TestProgramMetrics(t *testing.T) {
	e := New(4)
	probe := &spyFactory{delay: 20 * time.Millisecond}
	deps, creation := progEnv(t, probe, nil, nil, nil)
	p, res := prepare(t, e, greetConfig(), deps, creation, ProgramCall{ID: "mt-prep"})
	if res.DurationMs < 10 {
		t.Fatalf("prepare duration %d", res.DurationMs)
	}
	out := e.InvokeProgram(context.Background(), p, json.RawMessage(`{"name":"x"}`), deps, creation, ProgramCall{ID: "mt-1"})
	if out.DurationMs < 10 || out.Steps <= 0 {
		t.Fatalf("invoke metrics: %dms %dsteps", out.DurationMs, out.Steps)
	}
	// Failure paths still report steps.
	cfg := greetConfig()
	cfg.Sources["main.star"] = []byte(`def main(input):
    return 1 / 0
`)
	p2, _ := prepare(t, e, cfg, deps, creation, ProgramCall{ID: "mt-prep2"})
	out2 := e.InvokeProgram(context.Background(), p2, json.RawMessage(`{"name":"x"}`), deps, creation, ProgramCall{ID: "mt-2"})
	if out2.Error == nil || out2.Error.Code != "runtime" || out2.Steps <= 0 {
		t.Fatalf("runtime failure metrics: %+v steps=%d", out2.Error, out2.Steps)
	}
	// Pure-init step exhaustion reports step_limit with steps.
	cc := creation
	cc.MaxSteps = 100
	cfg.Sources["main.star"] = []byte(`x = 0
while x < 10000:
    x += 1
def main(input):
    return {}
`)
	_, res3 := e.PrepareProgram(context.Background(), cfg, deps, cc, ProgramCall{ID: "mt-prep3"})
	if res3.Error == nil || res3.Error.Code != "step_limit" || res3.Steps <= 0 {
		t.Fatalf("init step limit: %+v steps=%d", res3.Error, res3.Steps)
	}
}

func TestProgramFactoryCancel(t *testing.T) {
	// A session factory that cancels the call ctx mid-construction:
	// the result must be timeout with no main side effects.
	e := New(4)
	probe := &spyFactory{}
	ctx, cancel := context.WithCancel(context.Background())
	probe.onNew = cancel
	deps, creation := progEnv(t, probe, nil, nil, nil)
	cfg := greetConfig()
	cfg.Sources["main.star"] = []byte(`def main(input):
    probe.go()
    return {}
`)
	_, res := e.PrepareProgram(ctx, cfg, deps, creation, ProgramCall{ID: "fc-prep"})
	if res.Error == nil || res.Error.Code != "timeout" {
		t.Fatalf("expected timeout, got %+v", res.Error)
	}
	if _, ops := probe.counts(); ops != 0 {
		t.Fatalf("cancelled call ran %d ops", ops)
	}
	if n, c := probe.newCalls, probe.closedCount(); n != c {
		t.Fatalf("%d built vs %d closed", n, c)
	}
	probe.onNew = nil
	cancel()
	_ = ctx
}

func TestProgramAssetSeedFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		fs   string
	}{
		{"readonly", `{"read_only":true}`},
		{"quota", `{"quota_bytes":1}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := New(4)
			probe := &spyFactory{}
			reg := capability.NewRegistry()
			reg.Register(capfs.Factory())
			reg.Register(probe)
			fsCfg, err := capfs.Factory().Validate(json.RawMessage(tc.fs), capability.ServerLimits{MaxFSQuotaBytes: 256 << 20})
			if err != nil {
				t.Fatal(err)
			}
			probeCfg, _ := probe.Validate(nil, capability.ServerLimits{})
			deps := SessionDeps{Context: context.Background(), Registry: reg, Limits: capability.ServerLimits{MaxFSQuotaBytes: 256 << 20}}
			creation := SessionCreation{InstanceID: "ins", Capabilities: map[string]any{"fs": fsCfg, "probe": probeCfg}}
			cfg := greetConfig()
			cfg.Assets = map[string][]byte{"a.txt": []byte("seedbytes")}
			_, res := e.PrepareProgram(context.Background(), cfg, deps, creation, ProgramCall{ID: "as-" + tc.name})
			if res.Error == nil || res.Error.Code != "invalid_program" {
				t.Fatalf("expected invalid_program, got %+v", res.Error)
			}
			if n, c := probe.newCalls, probe.closedCount(); n != c {
				t.Fatalf("%d built vs %d closed", n, c)
			}
		})
	}
}

func TestProgramNoPathEcho(t *testing.T) {
	e := New(4)
	deps, creation := progEnv(t, &spyFactory{}, nil, nil, nil)
	cfg := greetConfig()
	cfg.Sources["s3kr3t-file.star"] = []byte("def (:")
	cfg.Sources["main.star"] = []byte(`def main(input):
    return {}
`)
	_, res := e.PrepareProgram(context.Background(), cfg, deps, creation, ProgramCall{ID: "ne-1"})
	if res.Error == nil || strings.Contains(res.Error.Message, "s3kr3t") {
		t.Fatalf("error echoed filename: %+v", res.Error)
	}
}

func TestProgramTotalBounds(t *testing.T) {
	e := New(4)
	deps, creation := progEnv(t, &spyFactory{}, nil, nil, nil)
	// 129 total files
	cfg := greetConfig()
	cfg.Assets = map[string][]byte{}
	for i := 0; i < 128; i++ {
		cfg.Assets[fmt.Sprintf("a%d.txt", i)] = []byte("x")
	}
	// sources=1 + assets=129 > 128
	_, r := e.PrepareProgram(context.Background(), cfg, deps, creation, ProgramCall{ID: "tb-1"})
	if r.Error == nil || r.Error.Code != "invalid_program" {
		t.Fatalf("129 files: %+v", r.Error)
	}
	// aggregate sources > 1MiB
	cfg = greetConfig()
	big := make([]byte, 600<<10)
	for i := 0; i < 2; i++ {
		cfg.Sources[fmt.Sprintf("big%d.star", i)] = big
	}
	_, r = e.PrepareProgram(context.Background(), cfg, deps, creation, ProgramCall{ID: "tb-2"})
	if r.Error == nil || r.Error.Code != "invalid_program" {
		t.Fatalf("sources total: %+v", r.Error)
	}
	// aggregate assets > 8MiB
	cfg = greetConfig()
	cfg.Assets = map[string][]byte{}
	for i := 0; i < 9; i++ {
		cfg.Assets[fmt.Sprintf("a%d.bin", i)] = make([]byte, 1<<20)
	}
	_, r = e.PrepareProgram(context.Background(), cfg, deps, creation, ProgramCall{ID: "tb-3"})
	if r.Error == nil || r.Error.Code != "invalid_program" {
		t.Fatalf("assets total: %+v", r.Error)
	}
}

func TestProgramWatchdogDeterministic(t *testing.T) {
	// onNew fires once the call is registered: cancel it with a
	// memory-limit cause; pure init never returns a program.
	e := New(4)
	probe := &spyFactory{}
	probe.onNew = func() {
		e.runningMu.Lock()
		c := e.running["wd-det"]
		e.runningMu.Unlock()
		if c != nil {
			c(ErrMemoryLimit)
		}
	}
	deps, creation := progEnv(t, probe, nil, nil, nil)
	cfg := greetConfig()
	cfg.Sources["main.star"] = []byte(`while True:
    pass
def main(input):
    return {}
`)
	p, res := e.PrepareProgram(context.Background(), cfg, deps, creation, ProgramCall{ID: "wd-det"})
	if p != nil || res.Error == nil || res.Error.Code != "memory_limit" {
		t.Fatalf("expected memory_limit, got %+v", res.Error)
	}
}
