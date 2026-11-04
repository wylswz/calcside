package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"calcside/internal/extartifact"
	"calcside/internal/module"
	"calcside/internal/types"
)

// modTree builds a pinned module tree for session attachments.
func modTree(t *testing.T, name string, version int64, files map[string]string) module.Tree {
	t.Helper()
	fb := map[string][]byte{}
	for n, s := range files {
		fb[n] = []byte(s)
	}
	sum, err := extartifact.TreeHash(fb)
	if err != nil {
		t.Fatal(err)
	}
	return module.Tree{Name: name, Version: version, Sum: sum, Files: fb}
}

func sessionWithModules(t *testing.T, mounts map[string]module.Tree) *Session {
	t.Helper()
	probe := &spyFactory{}
	deps, creation := progEnv(t, probe, nil, nil, nil)
	creation.Modules = mounts
	s, err := NewSession(deps, creation)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

func execOK(t *testing.T, e *Engine, s *Session, code string) Result {
	t.Helper()
	res := e.Exec(context.Background(), s, "exe_t", code, 0, 0, nil)
	if res.Error != nil {
		t.Fatalf("exec %q failed: %s %s", code, res.Error.Type, res.Error.Message)
	}
	return res
}

func TestExecModuleLoadFull(t *testing.T) {
	e := New(4)
	defer e.Close()
	s := sessionWithModules(t, map[string]module.Tree{
		"strkit": modTree(t, "strkit", 3, map[string]string{
			"util.star": `load("lib/inner.star", "rootfile")

def shout(s):
    return s.upper() + str(rootfile())`,
			"lib/inner.star": "def rootfile():\n    return 1",
		}),
	})
	res := e.Exec(context.Background(), s, "exe_t", `load("@strkit/util.star", "shout")
print(shout("hi"))
print(shout("yo"))`, 0, 0, func() string { return s.Out.String() })
	if res.Error != nil {
		t.Fatalf("load failed: %s", res.Error.Message)
	}
	if !strings.Contains(res.Output, "HI1") || !strings.Contains(res.Output, "YO1") {
		t.Fatalf("expected HI1/YO1 in output, got %q", res.Output)
	}
}

func TestExecModuleOncePerSession(t *testing.T) {
	e := New(4)
	defer e.Close()
	s := sessionWithModules(t, map[string]module.Tree{
		"m": modTree(t, "m", 1, map[string]string{
			"counter.star": "COUNT = [0]\ndef incr():\n    COUNT[0] += 1\n    return COUNT[0]",
		}),
	})
	res := e.Exec(context.Background(), s, "exe_1", `load("@m/counter.star", "incr")
print(incr())`, 0, 0, func() string { return s.Out.String() })
	if res.Error != nil || !strings.Contains(res.Output, "1") {
		t.Fatalf("first incr: %+v", res)
	}
	s.Out.Reset()
	res = e.Exec(context.Background(), s, "exe_2", `load("@m/counter.star", "incr")
print(incr())`, 0, 0, func() string { return s.Out.String() })
	if res.Error != nil {
		t.Fatal(res.Error.Message)
	}
	// A re-init would reset COUNT; shared per-session cache must reach 2.
	if !strings.Contains(res.Output, "2") {
		t.Fatalf("module re-initialized: output %q", res.Output)
	}
}

func TestExecModuleTwoAliasesIsolated(t *testing.T) {
	e := New(4)
	defer e.Close()
	tr := modTree(t, "m", 1, map[string]string{
		"c.star": "COUNT = [0]\ndef incr():\n    COUNT[0] += 1\n    return COUNT[0]",
	})
	s := sessionWithModules(t, map[string]module.Tree{"a": tr, "b": tr})
	res := e.Exec(context.Background(), s, "exe_1", `load("@a/c.star", "incr")
load("@b/c.star", incr_b="incr")
print(incr(), incr_b(), incr())`, 0, 0, func() string { return s.Out.String() })
	if res.Error != nil {
		t.Fatal(res.Error.Message)
	}
	// Each mount owns its own initialized tree: 1, 1, 2.
	if !strings.Contains(res.Output, "1 1 2") {
		t.Fatalf("aliases not isolated: %q", res.Output)
	}
}

func TestExecModuleLoadErrors(t *testing.T) {
	e := New(4)
	defer e.Close()
	s := sessionWithModules(t, map[string]module.Tree{
		"m": modTree(t, "m", 1, map[string]string{"u.star": "x = 1"}),
	})
	cases := []struct{ name, code, want string }{
		{"missing mount", `load("@z/u.star", "x")`, "unknown module mount"},
		{"missing file", `load("@m/nope.star", "x")`, "has no file"},
		{"ordinary load", `load("u.star", "x")`, "no source namespace"},
		{"bad ref", `load("@m", "x")`, "invalid module load"},
	}
	for _, tc := range cases {
		res := e.Exec(context.Background(), s, "exe_e", tc.code, 0, 0, nil)
		if res.Error == nil || !strings.Contains(res.Error.Message, tc.want) {
			t.Errorf("%s: want error containing %q, got %+v", tc.name, tc.want, res.Error)
		}
	}
}

func TestSessionModuleStaticRejects(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"cycle", map[string]string{
			"a.star": `load("b.star", "b")`,
			"b.star": `load("a.star", "a")`,
		}, "cyclic"},
		{"at load inside module", map[string]string{
			"a.star": `load("@m/b.star", "b")`,
			"b.star": `x = 1`,
		}, "@ load"},
		{"missing file", map[string]string{
			"a.star": `load("missing.star", "x")`,
		}, "missing"},
		{"non-star load", map[string]string{
			"a.star":    `load("notes.txt", "x")`,
			"notes.txt": "x = 1",
		}, ".star"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			probe := &spyFactory{}
			deps, creation := progEnv(t, probe, nil, nil, nil)
			creation.Modules = map[string]module.Tree{
				"m": modTree(t, "m", 1, tc.files),
			}
			s, err := NewSession(deps, creation)
			if err == nil {
				s.Close()
				t.Fatalf("expected error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q in error, got %v", tc.want, err)
			}
		})
	}
}

func TestSessionModuleTamperedHash(t *testing.T) {
	tr := modTree(t, "m", 1, map[string]string{"u.star": "x = 1"})
	tr.Sum = "h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	probe := &spyFactory{}
	deps, creation := progEnv(t, probe, nil, nil, nil)
	creation.Modules = map[string]module.Tree{"m": tr}
	s, err := NewSession(deps, creation)
	if err == nil {
		s.Close()
		t.Fatal("tampered tree accepted")
	}
	if !strings.Contains(err.Error(), "hash") {
		t.Fatalf("want hash error, got %v", err)
	}
}

func TestModuleCapabilityBoundary(t *testing.T) {
	// A module calling probe.go() at top level inherits the caller's
	// gate state: armed inside an exec load it runs, disarmed during
	// program pure-init it denies.
	e := New(4)
	defer e.Close()
	probe := &spyFactory{}
	deps, creation := progEnv(t, probe, nil, nil, nil)
	creation.Modules = map[string]module.Tree{
		"m": modTree(t, "m", 1, map[string]string{
			"u.star": "probe.go()\ndef go():\n    probe.go()",
		}),
	}
	s, err := NewSession(deps, creation)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	res := e.Exec(context.Background(), s, "exe_1", `load("@m/u.star", "go")`, 0, 0, nil)
	if res.Error != nil {
		t.Fatalf("armed module init denied: %s", res.Error.Message)
	}
	if _, ops := probe.counts(); ops != 1 {
		t.Fatalf("expected 1 gated op, got %d", ops)
	}

	// Same tree on the program path: pure-init gate is disarmed.
	probe2 := &spyFactory{}
	deps2, creation2 := progEnv(t, probe2, nil, nil, nil)
	creation2.Modules = creation.Modules
	cfg := ProgramConfig{
		Sources: map[string][]byte{"main.star": []byte(`load("@m/u.star", "go")
def main(input):
    return {"ok": True}`)},
		InputSchema:  json.RawMessage(`{"type":"object"}`),
		OutputSchema: json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"]}`),
	}
	_, res2 := e.PrepareProgram(context.Background(), cfg, deps2, creation2, ProgramCall{ID: "p1"})
	if res2.Error == nil || res2.Error.Code != "invalid_program" {
		t.Fatalf("pure-init op should deny, got %+v", res2.Error)
	}
	if _, ops := probe2.counts(); ops != 0 {
		t.Fatalf("disarmed gate ran %d ops", ops)
	}
}

func TestModuleUngrantedCapabilityFailsCreate(t *testing.T) {
	// Module code sees only the consumer's predeclared set: a net call
	// does not compile without the grant.
	probe := &spyFactory{}
	deps, creation := progEnv(t, probe, nil, nil, nil)
	creation.Modules = map[string]module.Tree{
		"m": modTree(t, "m", 1, map[string]string{
			"u.star": "def f():\n    net.get('http://x')",
		}),
	}
	s, err := NewSession(deps, creation)
	if err == nil {
		s.Close()
		t.Fatal("ungranted capability compiled")
	}
	if !strings.Contains(err.Error(), "compile") {
		t.Fatalf("want compile error, got %v", err)
	}
}

func TestProgramModuleLoads(t *testing.T) {
	e := New(4)
	defer e.Close()
	probe := &spyFactory{}
	deps, creation := progEnv(t, probe, nil, nil, nil)
	creation.Modules = map[string]module.Tree{
		"strkit": modTree(t, "strkit", 2, map[string]string{
			"u.star": `load("inner.star", "shout_inner")
def shout(s):
    return s.upper() + shout_inner()`,
			"inner.star":   "def shout_inner():\n    return '!'",
			"counter.star": "COUNT = [0]\ndef incr():\n    COUNT[0] += 1\n    return COUNT[0]",
		}),
	}
	cfg := ProgramConfig{
		Sources: map[string][]byte{
			"main.star": []byte(`load("@strkit/u.star", "shout")
load("@strkit/counter.star", "incr")
def main(input):
    return {"msg": shout(input["name"]), "n": incr()}`),
		},
		InputSchema:  json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`),
		OutputSchema: json.RawMessage(`{"type":"object","properties":{"msg":{"type":"string"},"n":{"type":"integer"}},"required":["msg","n"]}`),
	}
	p, res := e.PrepareProgram(context.Background(), cfg, deps, creation, ProgramCall{ID: "p1"})
	if res.Error != nil {
		t.Fatalf("prepare: %+v", res.Error)
	}
	out := e.InvokeProgram(context.Background(), p, json.RawMessage(`{"name":"ann"}`), deps, creation, ProgramCall{ID: "inv1"})
	if out.Error != nil {
		t.Fatalf("invoke: %+v", out.Error)
	}
	if string(out.Output) != `{"msg":"ANN!","n":1}` {
		t.Fatalf("bad output %s", out.Output)
	}
	// Per-invocation init: the second invoke re-initializes, never shares.
	out = e.InvokeProgram(context.Background(), p, json.RawMessage(`{"name":"ann"}`), deps, creation, ProgramCall{ID: "inv2"})
	if out.Error != nil {
		t.Fatalf("invoke2: %+v", out.Error)
	}
	if string(out.Output) != `{"msg":"ANN!","n":1}` {
		t.Fatalf("module cache leaked across invocations: %s", out.Output)
	}
}

func TestProgramModuleStaticErrors(t *testing.T) {
	e := New(4)
	defer e.Close()
	cases := []struct {
		name    string
		sources map[string][]byte
		mounts  map[string]module.Tree
		want    string
	}{
		{
			"missing mount",
			map[string][]byte{"main.star": []byte(`load("@z/u.star", "x")
def main(input):
    return {}`)},
			nil,
			"invalid_program",
		},
		{
			"missing module file",
			map[string][]byte{"main.star": []byte(`load("@m/nope.star", "x")
def main(input):
    return {}`)},
			map[string]module.Tree{"m": modTree(t, "m", 1, map[string]string{"u.star": "x=1"})},
			"invalid_program",
		},
		{
			"module cycle",
			map[string][]byte{"main.star": []byte(`load("@m/a.star", "x")
def main(input):
    return {}`)},
			map[string]module.Tree{"m": modTree(t, "m", 1, map[string]string{
				"a.star": `load("b.star", "b")`,
				"b.star": `load("a.star", "a")`,
			})},
			"invalid_program",
		},
		{
			"@ source name",
			map[string][]byte{"@evil.star": []byte("x=1"), "main.star": []byte(`def main(input):
    return {}`)},
			nil,
			"invalid_program",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			probe := &spyFactory{}
			deps, creation := progEnv(t, probe, nil, nil, nil)
			creation.Modules = tc.mounts
			cfg := ProgramConfig{
				Sources:      tc.sources,
				InputSchema:  json.RawMessage(`{"type":"object"}`),
				OutputSchema: json.RawMessage(`{"type":"object"}`),
			}
			_, res := e.PrepareProgram(context.Background(), cfg, deps, creation, ProgramCall{ID: "p-" + tc.name})
			if res.Error == nil || res.Error.Code != tc.want {
				t.Fatalf("want %s, got %+v", tc.want, res.Error)
			}
		})
	}
}

func TestValidateModuleSources(t *testing.T) {
	pre := func(name string) bool { return name == "json" || name == "net" }
	if err := ValidateModuleSources(map[string][]byte{
		"a.star": []byte(`load("b.star", "x")
def f():
    return net.get("http://x")`),
		"b.star": []byte("x = 1"),
	}, pre); err != nil {
		t.Fatal(err)
	}
	if err := ValidateModuleSources(map[string][]byte{
		"a.star": []byte(`def f():
    return fs.read("x")`),
	}, pre); err == nil {
		t.Fatal("unlisted predeclared name compiled")
	}
}

func TestModuleInitStepBudget(t *testing.T) {
	// Module top-level code consumes the caller's step budget: an
	// unbounded loop at init hits the exec's step limit.
	e := New(4)
	defer e.Close()
	s := sessionWithModules(t, map[string]module.Tree{
		"m": modTree(t, "m", 1, map[string]string{
			"loop.star": "while True:\n    pass",
		}),
	})
	res := e.Exec(context.Background(), s, "exe_1", `load("@m/loop.star", "x")`, 0, 5000, nil)
	if res.Error == nil || res.Error.Type != types.ErrStepLimit {
		t.Fatalf("module init escaped step budget: %+v", res.Error)
	}
}

func TestModuleInitCancellation(t *testing.T) {
	e := New(4)
	defer e.Close()
	s := sessionWithModules(t, map[string]module.Tree{
		"m": modTree(t, "m", 1, map[string]string{
			"loop.star": "while True:\n    pass",
		}),
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-time.After(30 * time.Millisecond); cancel() }()
	res := e.Exec(ctx, s, "exe_1", `load("@m/loop.star", "x")`, 0, 0, nil)
	if res.Error == nil || res.Error.Type != types.ErrTimeout {
		t.Fatalf("module init ignored cancellation: %+v", res.Error)
	}
}

func TestModuleManifestAccepted(t *testing.T) {
	// A tree with module.yaml passes tree + compile validation; the
	// manifest file itself is never compiled.
	s := sessionWithModules(t, map[string]module.Tree{
		"m": modTree(t, "m", 1, map[string]string{
			"module.yaml": "description: helpers\nrequires:\n  capabilities: [fs]\n",
			"u.star":      "x = 1",
		}),
	})
	_ = s
}
