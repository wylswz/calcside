package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	starjson "go.starlark.net/lib/json"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"calcside/internal/capability"
	capfs "calcside/internal/capability/fs"
	capio "calcside/internal/capability/io"
	"calcside/internal/secrets"
	"calcside/internal/types"
)

type denyAll struct{}

func (denyAll) Name() string { return "denyall" }
func (denyAll) Before(ctx context.Context, c *capability.Call) error {
	if c.Capability == types.CapFS {
		return &denyErr{"no fs allowed"}
	}
	return nil
}
func (denyAll) After(ctx context.Context, c *capability.Call, r *capability.Result) error { return nil }

type denyErr struct{ s string }

func (e *denyErr) Error() string { return e.s }

func newSession(t *testing.T, hooks []capability.Hook, fsQuota int64) (*Session, *capio.Buffer) {
	t.Helper()
	gate := capability.NewGate(capability.GateOwner{}, hooks, nil)
	fsF := capfs.Factory()
	cfg, err := fsF.Validate([]byte(`{}`), capability.ServerLimits{MaxFSQuotaBytes: 256 << 20})
	if err != nil {
		t.Fatal(err)
	}
	fsv, _, err := fsF.New(cfg, capability.InstanceEnv{Gate: gate})
	if err != nil {
		t.Fatal(err)
	}
	buf := capio.NewBuffer(fsQuota)
	pre := starlark.StringDict{
		"fs":    fsv,
		"json":  starjson.Module,
		"io":    ioModule(buf, gate),
		"print": capio.PrintBuiltin(buf, gate),
	}
	s := &Session{
		Gate: gate, Predeclared: pre, Globals: starlark.StringDict{},
		InstanceID: "ins_t", UserID: "u", UserEmail: "u@x",
	}
	return s, buf
}

func ioModule(buf *capio.Buffer, gate *capability.Gate) starlark.Value {
	return capio.BindBuffer(buf, gate)
}

func TestStepLimit(t *testing.T) {
	e := New(4)
	s, buf := newSession(t, nil, 1<<20)
	res := e.Exec(context.Background(), s, "x", "while True:\n  pass", 0, 1000, buf.String)
	if res.Error == nil || res.Error.Type != types.ErrStepLimit {
		t.Fatalf("expected step_limit, got %+v", res.Error)
	}
}

func TestTimeout(t *testing.T) {
	e := New(4)
	s, buf := newSession(t, nil, 1<<20)
	res := e.Exec(context.Background(), s, "x", "while True:\n  pass", 50*time.Millisecond, 1<<60, buf.String)
	if res.Error == nil || res.Error.Type != types.ErrTimeout {
		t.Fatalf("expected timeout, got %+v", res.Error)
	}
}

func TestOutputTruncation(t *testing.T) {
	e := New(4)
	s, buf := newSession(t, nil, 20)
	res := e.Exec(context.Background(), s, "x", "print('x'*100)", 0, 1<<20, buf.String)
	if res.Error != nil {
		t.Fatal(res.Error)
	}
	if !strings.Contains(res.Output, "[output truncated]") || strings.HasSuffix(res.Output, "x") && len(res.Output) > 100 {
		t.Fatalf("bad output %q", res.Output)
	}
}

func TestGlobalsPersist(t *testing.T) {
	e := New(4)
	s, buf := newSession(t, nil, 1<<20)
	res := e.Exec(context.Background(), s, "x", "counter = 41", 0, 1<<20, buf.String)
	if res.Error != nil {
		t.Fatal(res.Error)
	}
	res = e.Exec(context.Background(), s, "y", "print(counter + 1)", 0, 1<<20, buf.String)
	if res.Error != nil {
		t.Fatal(res.Error)
	}
	if res.Output != "42\n" {
		t.Fatalf("output %q", res.Output)
	}
}

func TestHelperDefUsesFSAcrossExecs(t *testing.T) {
	e := New(4)
	s, buf := newSession(t, nil, 1<<20)
	res := e.Exec(context.Background(), s, "x",
		"def save():\n  fs.write('a.txt', 'data')\nsave()", 0, 1<<20, buf.String)
	if res.Error != nil {
		t.Fatal(res.Error)
	}
	res = e.Exec(context.Background(), s, "y",
		"def save2():\n  return fs.read('a.txt')\nprint(save2())", 0, 1<<20, buf.String)
	if res.Error != nil {
		t.Fatal(res.Error)
	}
	if res.Output != "data\n" {
		t.Fatalf("output %q", res.Output)
	}
}

func TestMissingCapability(t *testing.T) {
	e := New(4)
	s, buf := newSession(t, nil, 1<<20)
	res := e.Exec(context.Background(), s, "x", "net.get('http://x')", 0, 1<<20, buf.String)
	// Undefined names are resolve.ErrorList, classified as "syntax" (a
	// static error): an absent capability fails at resolve time.
	if res.Error == nil || res.Error.Type != types.ErrSyntax || !strings.Contains(res.Error.Message, "net") {
		t.Fatalf("expected undefined-name resolve error, got %+v", res.Error)
	}
}

func TestSyntaxErrorType(t *testing.T) {
	e := New(4)
	s, buf := newSession(t, nil, 1<<20)
	res := e.Exec(context.Background(), s, "x", "def f(:\n", 0, 1<<20, buf.String)
	if res.Error == nil || res.Error.Type != types.ErrSyntax {
		t.Fatalf("expected syntax, got %+v", res.Error)
	}
}

func TestPolicyDeniedType(t *testing.T) {
	e := New(4)
	s, buf := newSession(t, []capability.Hook{denyAll{}}, 1<<20)
	res := e.Exec(context.Background(), s, "x", "fs.read('a')", 0, 1<<20, buf.String)
	if res.Error == nil || res.Error.Type != types.ErrPolicyDenied {
		t.Fatalf("expected policy_denied, got %+v", res.Error)
	}
}

func TestMutableGlobalsAcrossExecs(t *testing.T) {
	e := New(4)
	s, buf := newSession(t, nil, 1<<20)
	for i := 0; i < 3; i++ {
		var res Result
		if i == 0 {
			res = e.Exec(context.Background(), s, "x", "lst = []", 0, 1<<20, buf.String)
		} else {
			res = e.Exec(context.Background(), s, "x", "lst.append("+itoa(i)+")", 0, 1<<20, buf.String)
		}
		if res.Error != nil {
			t.Fatalf("exec %d: %+v", i, res.Error)
		}
	}
	res := e.Exec(context.Background(), s, "x", "print(lst)", 0, 1<<20, buf.String)
	if res.Error != nil {
		t.Fatal(res.Error)
	}
	if res.Output != "[1, 2]\n" {
		t.Fatalf("output %q", res.Output)
	}
}

func itoa(i int) string {
	return string(rune('0' + i))
}

func TestCapabilityShadowResetEachExec(t *testing.T) {
	e := New(4)
	s, buf := newSession(t, nil, 1<<20)
	// Shadow fs in exec 1; exec 2 must still see the real binding.
	res := e.Exec(context.Background(), s, "x", "fs = None", 0, 1<<20, buf.String)
	if res.Error != nil {
		t.Fatal(res.Error)
	}
	res = e.Exec(context.Background(), s, "y", "fs.write('a.txt', 'ok')", 0, 1<<20, buf.String)
	if res.Error != nil {
		t.Fatalf("fs shadow persisted: %+v", res.Error)
	}
}

func TestNewSessionFromValues(t *testing.T) {
	reg := capability.NewRegistry()
	reg.Register(capfs.Factory())
	reg.Register(capio.Factory())
	limits := capability.ServerLimits{MaxFSQuotaBytes: 256 << 20, MaxOutputBytes: 1 << 20}
	fsCfg, err := capfs.Factory().Validate([]byte(`{}`), limits)
	if err != nil {
		t.Fatal(err)
	}
	set := secrets.NewSet()
	set.Add("API_KEY", []byte("s3cret"), nil)

	s, err := NewSession(SessionDeps{Registry: reg, Limits: limits, Secrets: set}, SessionCreation{
		InstanceID:     "ins_t",
		UserID:         "u",
		Capabilities:   map[string]any{"fs": fsCfg},
		Env:            map[string]string{"A": "1"},
		MaxSteps:       1 << 20,
		MaxOutputBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, ok := s.Capabilities["io"]; !ok {
		t.Fatalf("io not implicitly granted: %v", s.Capabilities)
	}
	if _, ok := s.Closers[types.CapFS]; !ok || s.Out == nil {
		t.Fatalf("fs closer / output buffer not wired")
	}

	e := New(4)
	code := "fs.write('/work/a.txt', 'x')\nprint(env['A'], secrets.names(), fs.read('/work/a.txt'))"
	res := e.Exec(context.Background(), s, "x", code, 0, 1<<20, s.Out.String)
	if res.Error != nil || res.Output != "1 [\"API_KEY\"] x\n" {
		t.Fatalf("exec: err=%+v out=%q", res.Error, res.Output)
	}
}

func TestNewSessionUnknownCapability(t *testing.T) {
	reg := capability.NewRegistry()
	reg.Register(capio.Factory())
	_, err := NewSession(SessionDeps{Registry: reg}, SessionCreation{
		Capabilities: map[string]any{"nope": nil},
	})
	if err == nil || !strings.Contains(err.Error(), "unknown capability nope") {
		t.Fatalf("want unknown capability error, got %v", err)
	}
}

func TestInspectLargeValues(t *testing.T) {
	large := starlark.String(strings.Repeat("a", 1<<20))
	dict := starlark.NewDict(1)
	if err := dict.SetKey(starlark.String("x"), large); err != nil {
		t.Fatal(err)
	}
	set := starlark.NewSet(1)
	if err := set.Insert(large); err != nil {
		t.Fatal(err)
	}
	keyDict := starlark.NewDict(1)
	if err := keyDict.SetKey(large, starlark.None); err != nil {
		t.Fatal(err)
	}
	wide := starlark.NewList(nil)
	for range 8192 {
		if err := wide.Append(starlark.None); err != nil {
			t.Fatal(err)
		}
	}
	cycle := starlark.NewList(nil)
	if err := cycle.Append(cycle); err != nil {
		t.Fatal(err)
	}
	var deep starlark.Value = starlark.None
	for range 100 {
		deep = starlark.Tuple{deep}
	}
	var shared starlark.Value = starlark.None
	for range 16 {
		shared = starlark.Tuple{shared, shared}
	}
	for name, value := range map[string]starlark.Value{
		"string":             large,
		"bytes":              starlark.Bytes(large),
		"list":               starlark.NewList([]starlark.Value{large}),
		"tuple":              starlark.Tuple{large},
		"dict":               dict,
		"set":                set,
		"struct":             starlarkstruct.FromStringDict(starlark.String("struct"), starlark.StringDict{"x": large}),
		"struct constructor": starlarkstruct.FromStringDict(large, nil),
		"struct field name":  starlarkstruct.FromStringDict(starlark.String("struct"), starlark.StringDict{string(large): starlark.None}),
		"module":             &starlarkstruct.Module{Name: string(large)},
		"dict key":           keyDict,
		"int":                starlark.MakeInt(1).Lsh(maxInspectWork + 1),
		"negative int":       starlark.MakeInt(-1).Lsh(maxInspectWork + 1),
		"wide":               wide,
		"deep":               deep,
		"shared":             shared,
		"cycle":              cycle,
		"opaque":             opaqueCompletionValue{starlark.String("private")},
	} {
		t.Run(name, func(t *testing.T) {
			s := &Session{Globals: starlark.StringDict{"x": value}}
			got := s.Inspect(context.Background()).Variables["x"]
			if got != "<value omitted: inspection limit>" {
				t.Fatalf("large value was not omitted: rendered %d bytes", len(got))
			}
		})
	}
}

func TestInspectSmallValues(t *testing.T) {
	s, buf := newSession(t, nil, 1<<20)
	res := New(1).Exec(context.Background(), s, "inspect", `
x = "hello\nworld"
y = [1, True, None, (2,), {"k": 3.5}]
z = 1 << 100
f = len
m = json
b = b"hello"
method = ("x" * (1 << 20)).upper
def fn(x):
  return x
`, 0, 1<<20, buf.String)
	if res.Error != nil {
		t.Fatal(res.Error)
	}
	s.Globals["struct"] = starlarkstruct.FromStringDict(starlark.String("struct"), starlark.StringDict{"x": starlark.String("small")})
	s.Globals["set"] = starlark.NewSet(0)
	s.Globals["escaped"] = starlark.String(strings.Repeat("\x00", (maxInspectWork-32)/4))
	got := s.Inspect(context.Background()).Variables
	for name, value := range s.Globals {
		if got[name] != value.String() {
			t.Errorf("%s = %q, want %q", name, got[name], value.String())
		}
	}
}

func TestInspectAllocations(t *testing.T) {
	s := &Session{Globals: starlark.StringDict{"x": starlark.String(strings.Repeat("a", 1<<20))}}
	result := testing.Benchmark(func(b *testing.B) {
		for b.Loop() {
			s.Inspect(context.Background())
		}
	})
	if got := result.AllocedBytesPerOp(); got > 64<<10 {
		t.Fatalf("inspect allocated %d bytes for an omitted value, want at most 64 KiB", got)
	}
}
