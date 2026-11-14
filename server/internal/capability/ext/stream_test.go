package ext

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"go.starlark.net/starlark"

	"calcside/internal/capability"
	"calcside/internal/stream"
	"calcside/internal/types"
)

// scopedCall builds the fixture ext, arms the gate, activates an
// execution scope on a fresh thread and calls ext.<alias>.<op>.
func scopedCall(t *testing.T, hooks []capability.Hook, obs capability.Observer, alias, op string, args ...starlark.Value) (starlark.Value, *stream.Scope, *starlark.Thread, *capability.Gate, error) {
	t.Helper()
	dir := writeExt(t, map[string]string{
		"capability.yaml": `name: s
version: 0.1.0
ops: [{name: make_stream}]
`,
		"main.star": `def make_stream():
    items = [1, 2]
    def nxt():
        if len(items) == 0:
            return (False, None)
        return (True, items.pop(0))
    return stream.new(nxt)
`,
	})
	f := Factory(Options{LocalRoots: []string{localRoot(dir)}})
	spec, _ := json.Marshal(map[string]any{
		alias: map[string]any{"source": localID(dir)},
	})
	cfg, err := f.Validate(spec, capability.ServerLimits{})
	if err != nil {
		t.Fatal(err)
	}
	gate := capability.NewGate(capability.GateOwner{InstanceID: "ins"}, hooks, obs)
	v, closer, err := f.New(cfg, capability.InstanceEnv{
		Context:  context.Background(),
		Gate:     gate,
		Bindings: map[types.CapabilityName]starlark.Value{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if closer != nil {
		t.Cleanup(func() { closer.Close() })
	}
	extMod, err := v.(starlark.HasAttrs).Attr(alias)
	if err != nil {
		t.Fatal(err)
	}
	opv, err := extMod.(starlark.HasAttrs).Attr(op)
	if err != nil {
		t.Fatal(err)
	}
	thread := &starlark.Thread{Name: "exec"}
	sc := stream.NewScope(context.Background(), thread, nil)
	sc.Activate()
	thread.SetLocal(stream.ThreadKey, sc)
	thread.SetLocal(capability.ContextKey, sc.Context())
	gate.Arm(capability.ExecContext{ExecID: "e"})
	out, err := starlark.Call(thread, opv, starlark.Tuple(args), nil)
	return out, sc, thread, gate, err
}

func TestExtOpReturnsWrappedStream(t *testing.T) {
	obs := &recorder{}
	v, sc, thread, _, err := scopedCall(t, nil, obs, "s", "make_stream")
	if err != nil {
		t.Fatal(err)
	}
	w, ok := v.(*stream.Stream)
	if !ok {
		t.Fatalf("op returned %T", v)
	}
	// pulls flow through the same ext.s.make_stream gate
	got, ok, err := w.Pull(thread)
	if err != nil || !ok || got.String() != "1" {
		t.Fatalf("pull %v %v %v", got, ok, err)
	}
	var hasPull bool
	for _, r := range obs.recs {
		if r.Call.Op == "s.make_stream" && r.Call.Args[capability.StreamActionArg] == "pull" {
			hasPull = true
		}
	}
	if !hasPull {
		t.Fatal("no gated pull record")
	}
	if _, _, err := w.Pull(thread); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := w.Pull(thread); ok {
		t.Fatal("expected EOF")
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	var hasClose bool
	for _, r := range obs.recs {
		if r.Call.Args[capability.StreamActionArg] == "close" {
			hasClose = true
		}
	}
	if !hasClose {
		t.Fatal("no terminal close record")
	}
	_ = sc
}

type denyAfter struct{ reason string }

func (denyAfter) Name() string                                   { return "denyafter" }
func (denyAfter) Before(context.Context, *capability.Call) error { return nil }
func (d denyAfter) After(_ context.Context, c *capability.Call, r *capability.Result) error {
	if a, _ := c.Args[capability.StreamActionArg].(string); a == "open" {
		return errors.New(d.reason)
	}
	return nil
}

func TestExtOpenAfterDenyCloses(t *testing.T) {
	obs := &recorder{}
	v, sc, _, _, err := scopedCall(t, []capability.Hook{denyAfter{"open denied"}}, obs, "s", "make_stream")
	if err == nil {
		t.Fatal("open denial not surfaced")
	}
	if v != nil {
		t.Fatalf("value %v", v)
	}
	// the just-acquired source was closed immediately; the wrapper
	// registered in scope (if any) is also torn down
	_ = sc.Close()
	for _, r := range obs.recs {
		if r.Call.Op != "s.make_stream" {
			t.Fatalf("attribution %v", r.Call.Op)
		}
	}
}

type recorder struct {
	recs []capability.Record
}

func (r *recorder) Observe(rec capability.Record) { r.recs = append(r.recs, rec) }

func TestValidateProviderStreamShapes(t *testing.T) {
	thread := &starlark.Thread{Name: "exec"}
	sc := stream.NewScope(context.Background(), thread, nil)
	sc.Activate()
	thread.SetLocal(stream.ThreadKey, sc)

	mkstream := func(items ...starlark.Value) *stream.Stream {
		i := 0
		st, err := stream.New(sc, func(_ *starlark.Thread) (starlark.Value, bool, error) {
			if i >= len(items) {
				return starlark.None, false, nil
			}
			v := items[i]
			i++
			return v, true, nil
		}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	ev := func(m map[string]starlark.Value) *starlark.Dict {
		d := starlark.NewDict(len(m))
		for k, v := range m {
			_ = d.SetKey(starlark.String(k), v)
		}
		return d
	}
	delta := func(txt string) *starlark.Dict {
		return ev(map[string]starlark.Value{"type": starlark.String("text_delta"), "text": starlark.String(txt)})
	}
	done := func(fr string, usage starlark.Value) *starlark.Dict {
		return ev(map[string]starlark.Value{"type": starlark.String("done"),
			"finish_reason": starlark.String(fr), "usage": usage})
	}

	// non-stream return rejected
	if _, err := validateProviderStream(thread, "p", "m", starlark.NewList(nil)); err == nil {
		t.Fatal("list accepted as provider stream")
	}
	// EOF before done fails
	src := mkstream(delta("hi"))
	w, err := validateProviderStream(thread, "p", "m", src)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := w.Pull(thread); !ok {
		t.Fatal("delta lost")
	}
	if _, _, err := w.Pull(thread); err == nil {
		t.Fatal("EOF without done accepted")
	}
	// done then EOF
	u := starlark.NewDict(1)
	_ = u.SetKey(starlark.String("input_tokens"), starlark.MakeInt(7))
	src = mkstream(delta("x"), done("stop", u))
	w, err = validateProviderStream(thread, "p", "m", src)
	if err != nil {
		t.Fatal(err)
	}
	_, ok, _ := w.Pull(thread)
	if !ok {
		t.Fatal("delta lost")
	}
	v, ok, err := w.Pull(thread)
	if !ok || err != nil {
		t.Fatalf("done lost: %v %v", ok, err)
	}
	_ = v
	if _, ok, _ := w.Pull(thread); ok {
		t.Fatal("post-done yield")
	}
	tm := w.TerminalMeta()
	if tm == nil || tm["provider"] != "p" {
		t.Fatalf("terminal meta %v", tm)
	}
	// bad events
	bad := []*starlark.Dict{
		ev(map[string]starlark.Value{"type": starlark.String("bogus")}),
		ev(map[string]starlark.Value{"type": starlark.String("done")}), // no finish_reason
		ev(map[string]starlark.Value{"type": starlark.String("text_delta"), "text": starlark.MakeInt(1)}),
		done("ok", starlark.String("notadict")),
	}
	for _, b := range bad {
		src := mkstream(b)
		w, err := validateProviderStream(thread, "p", "m", src)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := w.Pull(thread); err == nil {
			t.Fatalf("bad event accepted: %v", b)
		}
	}
}
