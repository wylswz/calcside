package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	starjson "go.starlark.net/lib/json"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"calcside/internal/capability"
	capio "calcside/internal/capability/io"
	"calcside/internal/serving"
	"calcside/internal/stdlib"
	"calcside/internal/stream"
	"calcside/internal/types"
)

// streamSession builds a session whose predeclared includes the full
// stdlib module set (stream, json guard) plus io/print.
func streamSession(t *testing.T, hooks []capability.Hook, obs capability.Observer) (*Session, *capio.Buffer) {
	t.Helper()
	gate := capability.NewGate(capability.GateOwner{}, hooks, obs)
	buf := capio.NewBuffer(1 << 20)
	pre := stdlib.Modules()
	pre["io"] = capio.BindBuffer(buf, gate)
	pre["print"] = capio.PrintBuiltin(buf, gate)
	s := &Session{
		Gate: gate, Predeclared: pre, Globals: starlark.StringDict{},
		InstanceID: "ins_t", UserID: "u", UserEmail: "u@x",
	}
	return s, buf
}

func TestStreamBasicIteration(t *testing.T) {
	s, buf := streamSession(t, nil, nil)
	e := New(4)
	res := e.Exec(context.Background(), s, "x", `
items = [1, 2, 3]
def nxt():
    if len(items) == 0:
        return (False, None)
    return (True, items.pop(0))
s = stream.new(nxt)
for x in s:
    print(x)
`, 0, 1<<20, buf.String)
	if res.Error != nil {
		t.Fatalf("exec: %+v", res.Error)
	}
	if !strings.Contains(res.Output, "1") || !strings.Contains(res.Output, "3") {
		t.Fatalf("output %q", res.Output)
	}
}

func TestStreamLaziness(t *testing.T) {
	s, buf := streamSession(t, nil, nil)
	e := New(4)
	res := e.Exec(context.Background(), s, "x", `
calls = []
def nxt():
    calls.append(1)
    return (False, None)
s = stream.new(nxt)
print("calls", len(calls))
`, 0, 1<<20, buf.String)
	if res.Error != nil {
		t.Fatal(res.Error)
	}
	if !strings.Contains(res.Output, "calls 0") {
		t.Fatalf("not lazy: %q", res.Output)
	}
}

func TestStreamStickyErrorFailsExec(t *testing.T) {
	s, buf := streamSession(t, nil, nil)
	e := New(4)
	// consumption inside list() — a Go builtin — must still fail the exec
	res := e.Exec(context.Background(), s, "x", `
def nxt():
    fail("source exploded")
s = stream.new(nxt)
list(s)
`, 0, 1<<20, buf.String)
	if res.Error == nil {
		t.Fatal("stream failure reported success")
	}
	if res.Error.Type != types.ErrRuntime {
		t.Fatalf("type %s", res.Error.Type)
	}
	if !strings.Contains(res.Error.Message, "source exploded") {
		t.Fatalf("message %q", res.Error.Message)
	}
}

func TestStreamErrorJustBeforeReturn(t *testing.T) {
	s, buf := streamSession(t, nil, nil)
	e := New(4)
	res := e.Exec(context.Background(), s, "x", `
def nxt():
    fail("late boom")
def run():
    s = stream.new(nxt)
    v = s.next()
    return "ok"
run()
`, 0, 1<<20, buf.String)
	if res.Error == nil || !strings.Contains(res.Error.Message, "late boom") {
		t.Fatalf("err %+v", res.Error)
	}
}

func TestStaleStreamFaultsNextExec(t *testing.T) {
	s, buf := streamSession(t, nil, nil)
	e := New(4)
	res := e.Exec(context.Background(), s, "x", `
s = stream.new(lambda: (True, 1))
`, 0, 1<<20, buf.String)
	if res.Error != nil {
		t.Fatal(res.Error)
	}
	// iterating the stale handle in a later exec must fault THIS exec
	res = e.Exec(context.Background(), s, "y", `for x in s:
    print(x)
`, 0, 1<<20, buf.String)
	if res.Error == nil {
		t.Fatal("stale iteration succeeded")
	}
	if res.Error.Type != types.ErrOutOfScope {
		t.Fatalf("type %s", res.Error.Type)
	}
	// direct .next() on the stale handle faults too
	res = e.Exec(context.Background(), s, "z", `s.next()`, 0, 1<<20, buf.String)
	if res.Error == nil || res.Error.Type != types.ErrOutOfScope {
		t.Fatalf("direct .next: %+v", res.Error)
	}
}

func TestStreamNeverConsumedCleanup(t *testing.T) {
	s, buf := streamSession(t, nil, nil)
	e := New(4)
	res := e.Exec(context.Background(), s, "x", `
s1 = stream.new(lambda: (True, 1))
s2 = stream.sse(stream.new(lambda: (True, b"data: x\\n\\n")))
`, 0, 1<<20, buf.String)
	if res.Error != nil {
		t.Fatal(res.Error)
	}
	if s.streamScope != nil {
		t.Fatal("scope still bound after exec")
	}
}

func TestStreamJSONRejected(t *testing.T) {
	s, buf := streamSession(t, nil, nil)
	e := New(4)
	res := e.Exec(context.Background(), s, "x", `
s = stream.new(lambda: (False, None))
json.encode({"k": s})
`, 0, 1<<20, buf.String)
	if res.Error == nil || !strings.Contains(res.Error.Message, "stream") {
		t.Fatalf("json.encode(stream): %+v", res.Error)
	}
}

func TestStreamInspectOpaque(t *testing.T) {
	s, buf := streamSession(t, nil, nil)
	e := New(4)
	res := e.Exec(context.Background(), s, "x", `s = stream.new(lambda: (False, None))`, 0, 1<<20, buf.String)
	if res.Error != nil {
		t.Fatal(res.Error)
	}
	out := s.Inspect(context.Background())
	got, ok := out.Variables["s"]
	if !ok {
		t.Fatal("stream not in globals")
	}
	if !strings.HasPrefix(got, "<stream") {
		t.Fatalf("repr %q", got)
	}
}

func TestStreamBreakThenResume(t *testing.T) {
	s, buf := streamSession(t, nil, nil)
	e := New(4)
	// break releases the cursor only: the same stream resumes via
	// .next inside the same exec and is NOT closed until explicit
	// .close() (or exec teardown).
	res := e.Exec(context.Background(), s, "x", `
i = [0]
def nxt():
    i[0] += 1
    return (i[0] < 4, i[0])
s = stream.new(nxt)
for x in s:
    print(x)
    break
has, v = s.next()
print(has, v)
s.close()
`, 0, 1<<20, buf.String)
	if res.Error != nil {
		t.Fatal(res.Error)
	}
	if !strings.Contains(res.Output, "2") {
		t.Fatalf("cursor did not resume after break: %q", res.Output)
	}
}

func TestStreamNestedIteration(t *testing.T) {
	s, buf := streamSession(t, nil, nil)
	e := New(4)
	res := e.Exec(context.Background(), s, "x", `
i = [0]
def nxt():
    i[0] += 1
    return (i[0] < 4, i[0])
s = stream.new(nxt)
for a in s:
    for b in s:
        pass
`, 0, 1<<20, buf.String)
	if res.Error == nil {
		t.Fatal("nested iteration succeeded")
	}
}

func TestStreamTimeoutOutranksStreamFailure(t *testing.T) {
	s, buf := streamSession(t, nil, nil)
	e := New(4)
	res := e.Exec(context.Background(), s, "x", `
s = stream.new(lambda: (False, None))
while True:
    pass
`, 50*time.Millisecond, 1<<60, buf.String)
	if res.Error == nil || res.Error.Type != types.ErrTimeout {
		t.Fatalf("expected timeout, got %+v", res.Error)
	}
}

func TestStreamMarshalJSONRejected(t *testing.T) {
	s, buf := streamSession(t, nil, nil)
	e := New(4)
	res := e.Exec(context.Background(), s, "x", `s = stream.new(lambda: (False, None))`, 0, 1<<20, buf.String)
	if res.Error != nil {
		t.Fatal(res.Error)
	}
	st := s.Globals["s"]
	if _, err := MarshalJSONValue(st, defaultJSONLimits()); err == nil {
		t.Fatal("MarshalJSONValue accepted a stream")
	}
	// nested inside output-shaped containers too
	l := starlark.NewList([]starlark.Value{st})
	if _, err := MarshalJSONValue(l, defaultJSONLimits()); err == nil {
		t.Fatal("MarshalJSONValue accepted a nested stream")
	}
}

func defaultJSONLimits() (l serving.Limits) {
	return l.Effective()
}

func TestStreamModuleInitScopeAbsent(t *testing.T) {
	s, buf := streamSession(t, nil, nil)
	// stream.new outside an armed exec — no exec is running here, so a
	// bare starlark eval on the session thread has no scope. Instead,
	// verify directly: a scope-less thread cannot create streams.
	thread := &starlark.Thread{Name: "init"}
	v := s.Predeclared["stream"]
	newFn, err := v.(starlark.HasAttrs).Attr("new")
	if err != nil {
		t.Fatal(err)
	}
	fn := starlark.NewBuiltin("n", func(t *starlark.Thread, b *starlark.Builtin, a starlark.Tuple, k []starlark.Tuple) (starlark.Value, error) {
		return starlark.Tuple{starlark.False, starlark.None}, nil
	})
	if _, err := starlark.Call(thread, newFn, starlark.Tuple{fn}, nil); err == nil {
		t.Fatal("stream.new succeeded with no active scope")
	}
	_ = buf
}

func TestStreamNewKwarg(t *testing.T) {
	s, buf := streamSession(t, nil, nil)
	e := New(4)
	res := e.Exec(context.Background(), s, "x", `
def nxt():
    return (False, None)
s = stream.new(next_fn=nxt)
s2 = stream.new(nxt)
print("ok")
`, 0, 1<<20, buf.String)
	if res.Error != nil {
		t.Fatal(res.Error)
	}
	for _, bad := range []string{`stream.new(nxt, s)`, `stream.new(nxt, source=s)`, `stream.new(nxt, bogus=1)`} {
		res = e.Exec(context.Background(), s, "y", bad, 0, 1<<20, buf.String)
		if res.Error == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}

// ---- program-path regressions (finding 7) ----

func progStreamConfig(code string) ProgramConfig {
	return ProgramConfig{
		Sources:      map[string][]byte{"main.star": []byte(code)},
		InputSchema:  json.RawMessage(`{"type":"object"}`),
		OutputSchema: json.RawMessage(`{"type":"object"}`),
	}
}

func TestProgramStreamAggregate(t *testing.T) {
	e := New(4)
	deps, creation := progEnv(t, &spyFactory{}, nil, nil, nil)
	cfg := progStreamConfig(`def main(input):
    items = ["a", "b"]
    def nxt():
        if len(items) == 0:
            return (False, None)
        v = items[0]
        items.pop(0)
        return (True, v)
    s = stream.new(nxt)
    out = ""
    for v in s:
        out += v
    return {"greeting": out}
`)
	p, _ := prepare(t, e, cfg, deps, creation, ProgramCall{ID: "prep-1"})
	res := e.InvokeProgram(context.Background(), p, json.RawMessage(`{}`), deps, creation, ProgramCall{ID: "inv-1"})
	if res.Error != nil {
		t.Fatalf("invoke: %+v", res.Error)
	}
	if string(res.Output) != `{"greeting":"ab"}` {
		t.Fatalf("output %s", res.Output)
	}
}

// An unused gated stream whose close phase is denied after
// interrupt.wait must produce Error, not a waiting result.
func TestProgramStreamWaitStickyBeatsWaiting(t *testing.T) {
	e := New(4)
	hook := denyStreamClose{}
	deps, creation := progEnv(t, &spyFactory{}, nil, nil, []capability.Hook{hook})
	cfg := progStreamConfig(`def main(input):
    s = probe.stream()
    rid = interrupt.run_id()
    req = interrupt.new(expires_in_s = 60)
    interrupt.wait(req["id"], handler = "on_ok", on_expire = "on_exp", context = {})

def on_ok(ctx):
    return {"status": "ok"}

def on_exp(ctx):
    return {"status": "expired"}
`)
	p, _ := prepare(t, e, cfg, deps, creation, ProgramCall{ID: "prep-1"})
	res := e.InvokeProgram(context.Background(), p, json.RawMessage(`{}`), deps, creation, invokeCall("inv-1"))
	if res.Error == nil {
		t.Fatalf("expected sticky close-deny error, got waiting=%v", res.Waiting)
	}
	if res.Waiting != nil {
		t.Fatal("Waiting must be nil when the sticky stream error wins")
	}
	if res.Error.Code != "policy_denied" && res.Error.Code != "runtime" {
		t.Fatalf("code %s: %s", res.Error.Code, res.Error.Message)
	}
}

func TestProgramStreamInvalidOutput(t *testing.T) {
	e := New(4)
	deps, creation := progEnv(t, &spyFactory{}, nil, nil, nil)
	cfg := progStreamConfig(`def main(input):
    def nxt():
        return (False, None)
    return {"greeting": stream.new(nxt)}
`)
	p, _ := prepare(t, e, cfg, deps, creation, ProgramCall{ID: "prep-1"})
	res := e.InvokeProgram(context.Background(), p, json.RawMessage(`{}`), deps, creation, ProgramCall{ID: "inv-1"})
	if res.Error == nil || res.Error.Code != "invalid_output" {
		t.Fatalf("expected invalid_output, got %+v", res.Error)
	}
}

func TestProgramStreamInterruptContextRejected(t *testing.T) {
	e := New(4)
	deps, creation := progEnv(t, &spyFactory{}, nil, nil, nil)
	cfg := progStreamConfig(`def main(input):
    def nxt():
        return (False, None)
    s = stream.new(nxt)
    req = interrupt.new(expires_in_s = 60)
    interrupt.wait(req["id"], handler = "on_ok", on_expire = "on_exp", context = {"s": s})

def on_ok(ctx):
    return {}

def on_exp(ctx):
    return {}
`)
	p, _ := prepare(t, e, cfg, deps, creation, ProgramCall{ID: "prep-1"})
	res := e.InvokeProgram(context.Background(), p, json.RawMessage(`{}`), deps, creation, invokeCall("inv-1"))
	if res.Error == nil {
		t.Fatal("stream in interrupt context accepted")
	}
	if res.Waiting != nil {
		t.Fatal("must not reach waiting")
	}
}

// Streams cannot be created during pure module init (scope inactive).
func TestProgramStreamInitRejected(t *testing.T) {
	e := New(4)
	deps, creation := progEnv(t, &spyFactory{}, nil, nil, nil)
	cfg := progStreamConfig(`def nxt():
    return (False, None)

s = stream.new(nxt)

def main(input):
    return {"ok": True}
`)
	_, res := e.PrepareProgram(context.Background(), cfg, deps, creation, ProgramCall{ID: "prep-1"})
	if res.Error == nil {
		t.Fatal("stream creation in init accepted")
	}
}

// denyStreamClose denies gate close phases (used to prove sticky errors
// beat interrupt.wait acceptance).
type denyStreamClose struct{}

func (denyStreamClose) Name() string { return "deny-close" }
func (denyStreamClose) Before(_ context.Context, c *capability.Call) error {
	if c.Args[capability.StreamActionArg] == "close" {
		return errors.New("close denied")
	}
	return nil
}
func (denyStreamClose) After(context.Context, *capability.Call, *capability.Result) error {
	return nil
}

// json.Marshaler rejection covers any nesting depth, structs and
// encode_indent — and must not pull the source.
func TestStreamJSONDeepAndStruct(t *testing.T) {
	s, buf := streamSession(t, nil, nil)
	e := New(4)
	var res Result
	for _, code := range []string{
		`v = stream.new(nxt)
for _ in range(40):
    v = [v]
json.encode(v)`,
		`json.encode(struct(s = stream.new(nxt)))`,
		`json.encode_indent(stream.new(nxt))`,
	} {
		res = e.Exec(context.Background(), s, "x", "def nxt():\n    return (True, 1)\n"+code, 0, 1<<20, buf.String)
		if res.Error == nil {
			t.Fatalf("stream JSON-encodable: %s", code)
		}
	}
	// the source callback never ran — encoding must not pull
	res = e.Exec(context.Background(), s, "y", `
pulled = [0]
def spy():
    pulled[0] += 1
    return (False, None)
json.encode(stream.new(spy))
`, 0, 1<<20, buf.String)
	if res.Error == nil {
		t.Fatal("encode accepted a stream")
	}
	res = e.Exec(context.Background(), s, "z", `if pulled[0] != 0:
    fail("JSON encoding pulled the stream")`, 0, 1<<20, buf.String)
	if res.Error != nil {
		t.Fatal(res.Error)
	}
}

// json.Marshaler rejection inside a real struct value (the vendored
// encoder traverses HasAttrs before Iterable) — must fail with a
// stream-specific error, not a resolve error.
func TestStreamJSONInStruct(t *testing.T) {
	thread := &starlark.Thread{Name: "t"}
	sc := stream.NewScope(context.Background(), thread, nil)
	sc.Activate()
	st, err := stream.New(sc, func(_ *starlark.Thread) (starlark.Value, bool, error) {
		return starlark.None, false, nil
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	sv := starlarkstruct.FromStringDict(starlarkstruct.Default,
		starlark.StringDict{"s": st})
	enc, _ := starjson.Module.Attr("encode")
	if _, err := starlark.Call(thread, enc, starlark.Tuple{sv}, nil); err == nil ||
		!strings.Contains(err.Error(), "stream") {
		t.Fatalf("struct stream encoding: %v", err)
	}
}

// A failed exec still reclaims every registered stream deterministically
// before the gate disarms — the close record is observable.
func TestStreamExecErrorStillTearsDown(t *testing.T) {
	obs := &recObserver{}
	deps, creation := progEnv(t, &spyFactory{}, obs, nil, nil)
	s, err := NewSession(deps, creation)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	res := New(4).Exec(context.Background(), s, "x", `
s = probe.stream()
fail("boom")
`, 0, 1<<20, func() string { return s.Out.String() })
	if res.Error == nil {
		t.Fatal("expected error")
	}
	found := false
	for _, r := range obs.all() {
		if r.Call.Args[capability.StreamActionArg] == "close" {
			found = true
		}
	}
	if !found {
		t.Fatal("stream not torn down on exec failure")
	}
}

// A native panic inside the exec still reclaims every registered stream
// via the deferred teardown — the scope close runs while the gate is
// still armed and the panic propagates.
func TestStreamPanicStillTearsDown(t *testing.T) {
	obs := &recObserver{}
	deps, creation := progEnv(t, &spyFactory{}, obs, nil, nil)
	s, err := NewSession(deps, creation)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// Recover at the test boundary so teardown can be inspected after
	// the native panic unwinds the execution.
	func() {
		defer func() { _ = recover() }()
		New(4).Exec(context.Background(), s, "x", `
s = probe.stream()
probe.panic()
`, 0, 1<<20, func() string { return s.Out.String() })
	}()
	closes := 0
	for _, r := range obs.all() {
		if r.Call.Args[capability.StreamActionArg] == "close" {
			closes++
		}
	}
	if closes != 1 {
		t.Fatalf("expected exactly one close, got %d", closes)
	}
	if s.streamScope != nil {
		t.Fatal("streamScope not cleared on panic unwind")
	}
	if s.Gate.Armed() {
		t.Fatal("gate still armed after panic unwind")
	}
}

func TestStreamMap(t *testing.T) {
	code := `
def next():
    i = [0]
    def _next():
	    if i[0] < 5:
		    i[0] += 1
		    return True, i[0]
	    return False, None
    return _next
s = stream.new(next()).map(lambda x: x * 2).map(lambda x: print(x))
for x in s:
    pass
`
	e, err := NewEngine(t)
	if err != nil {
		t.Fatal(err)
	}
	result, starErr := e.Exec(t, code)
	assert.Nil(t, starErr)
	assert.Equal(t, "2\n4\n6\n8\n10\n", result)
}
