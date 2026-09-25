package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	starjson "go.starlark.net/lib/json"
	"go.starlark.net/starlark"

	"calcside/internal/capability"
	capfs "calcside/internal/capability/fs"
	capio "calcside/internal/capability/io"
)

type denyAll struct{}

func (denyAll) Name() string { return "denyall" }
func (denyAll) Before(ctx context.Context, c *capability.Call) error {
	if c.Capability == "fs" {
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
	fsv, _, err := fsF.New(cfg, gate)
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
	if res.Error == nil || res.Error.Type != "step_limit" {
		t.Fatalf("expected step_limit, got %+v", res.Error)
	}
}

func TestTimeout(t *testing.T) {
	e := New(4)
	s, buf := newSession(t, nil, 1<<20)
	res := e.Exec(context.Background(), s, "x", "while True:\n  pass", 50*time.Millisecond, 1<<60, buf.String)
	if res.Error == nil || res.Error.Type != "timeout" {
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
	if res.Error == nil || res.Error.Type != "syntax" || !strings.Contains(res.Error.Message, "net") {
		t.Fatalf("expected undefined-name resolve error, got %+v", res.Error)
	}
}

func TestSyntaxErrorType(t *testing.T) {
	e := New(4)
	s, buf := newSession(t, nil, 1<<20)
	res := e.Exec(context.Background(), s, "x", "def f(:\n", 0, 1<<20, buf.String)
	if res.Error == nil || res.Error.Type != "syntax" {
		t.Fatalf("expected syntax, got %+v", res.Error)
	}
}

func TestPolicyDeniedType(t *testing.T) {
	e := New(4)
	s, buf := newSession(t, []capability.Hook{denyAll{}}, 1<<20)
	res := e.Exec(context.Background(), s, "x", "fs.read('a')", 0, 1<<20, buf.String)
	if res.Error == nil || res.Error.Type != "policy_denied" {
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

func TestMemoryWatchdog(t *testing.T) {
	e := New(4,
		WithMemoryLimit(1), // any heap read trips it
		WithHeapSampler(func() uint64 { return 1 << 30 }),
		WithWatchdogInterval(5*time.Millisecond),
	)
	defer e.Close()
	s, buf := newSession(t, nil, 1<<20)
	res := e.Exec(context.Background(), s, "x", "while True:\n  pass", 30*time.Second, 1<<60, buf.String)
	if res.Error == nil || res.Error.Type != "memory_limit" {
		t.Fatalf("expected memory_limit, got %+v", res.Error)
	}
}

func TestMemoryWatchdogRealHeap(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	e := New(4,
		WithMemoryLimit(64<<20),
		WithWatchdogInterval(10*time.Millisecond),
	)
	defer e.Close()
	s, buf := newSession(t, nil, 1<<20)
	code := "x = []\nwhile True:\n  x.append(\"a\" * 1000000)"
	res := e.Exec(context.Background(), s, "x", code, 60*time.Second, 1<<60, buf.String)
	if res.Error == nil || res.Error.Type != "memory_limit" {
		t.Fatalf("expected memory_limit, got %+v", res.Error)
	}
}
