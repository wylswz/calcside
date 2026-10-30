package interrupt

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"go.starlark.net/starlark"

	"calcside/internal/capability"
	"calcside/internal/secrets"
	"calcside/internal/types"
)

// recObserver collects gate observations.
type recObserver struct{ recs []capability.Record }

func (o *recObserver) Observe(r capability.Record) { o.recs = append(o.recs, r) }

// toJSON is a minimal bounded converter for tests (real callers inject
// the engine's MarshalJSONValue).
func toJSON(v starlark.Value) ([]byte, error) {
	var conv func(v starlark.Value) (any, error)
	conv = func(v starlark.Value) (any, error) {
		switch t := v.(type) {
		case starlark.NoneType:
			return nil, nil
		case starlark.Bool:
			return bool(t), nil
		case starlark.String:
			return string(t), nil
		case starlark.Int:
			i, _ := t.Int64()
			return i, nil
		case starlark.Float:
			return float64(t), nil
		case *starlark.List:
			out := make([]any, t.Len())
			for i := range out {
				e, err := conv(t.Index(i))
				if err != nil {
					return nil, err
				}
				out[i] = e
			}
			return out, nil
		case *starlark.Dict:
			out := map[string]any{}
			for _, k := range t.Keys() {
				ks, ok := k.(starlark.String)
				if !ok {
					return nil, errors.New("non-string key")
				}
				v2, ok2, err2 := t.Get(k)
				if err2 != nil || !ok2 {
					return nil, errors.New("missing key")
				}
				e, err := conv(v2)
				if err != nil {
					return nil, err
				}
				out[string(ks)] = e
			}
			return out, nil
		default:
			return nil, errors.New("non-JSON value")
		}
	}
	jv, err := conv(v)
	if err != nil {
		return nil, err
	}
	return json.Marshal(jv)
}

func armedGate(obs capability.Observer) *capability.Gate {
	g := capability.NewGate(capability.GateOwner{}, nil, obs)
	g.Arm(capability.ExecContext{ExecID: "e1"})
	return g
}

func host(handlers ...string) *Host {
	return &Host{
		RunID:     "run_1",
		Step:      0,
		IsHandler: func(n string) bool { return slices.Contains(handlers, n) },
		Secrets:   secrets.NewSet(),
		ToJSON:    toJSON,
		Slots: []Slot{
			{ID: "int_1", ResumeURL: "https://x/int_1/resume", Token: "tok_1"},
			{ID: "int_2", ResumeURL: "https://x/int_2/resume", Token: "tok_2"},
		},
	}
}

func attr(t *testing.T, mod starlark.Value, name string) starlark.Value {
	t.Helper()
	v, err := mod.(starlark.HasAttrs).Attr(name)
	if err != nil || v == nil {
		t.Fatalf("missing attr %s: %v", name, err)
	}
	return v
}

// callModule invokes a module builtin by name with the thread ctx set.
func callModule(t *testing.T, mod starlark.Value, thread *starlark.Thread, name string, args ...any) (starlark.Value, error) {
	t.Helper()
	fn := attr(t, mod, name)
	tuple := make(starlark.Tuple, len(args))
	for i, a := range args {
		v, ok := a.(starlark.Value)
		if !ok {
			t.Fatalf("arg %d is not a starlark.Value", i)
		}
		tuple[i] = v
	}
	return starlark.Call(thread, fn, tuple, nil)
}

func dict(t *testing.T, pairs ...any) *starlark.Dict {
	t.Helper()
	d := starlark.NewDict(len(pairs) / 2)
	for i := 0; i < len(pairs); i += 2 {
		if err := d.SetKey(starlark.String(pairs[i].(string)), pairs[i+1].(starlark.Value)); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func TestRunIDAndNew(t *testing.T) {
	h := host()
	obs := &recObserver{}
	mod := Binding(h, armedGate(obs))
	thread := &starlark.Thread{Name: "t"}
	thread.SetLocal(capability.ContextKey, context.Background())

	rid, err := callModule(t, mod, thread, "run_id")
	if err != nil || rid.(starlark.String) != "run_1" {
		t.Fatalf("run_id: %v %v", rid, err)
	}
	newFn := attr(t, mod, "new")
	v, err := starlark.Call(thread, newFn, nil, []starlark.Tuple{{starlark.String("expires_in_s"), starlark.MakeInt(60)}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	d := v.(*starlark.Dict)
	get := func(k string) starlark.Value {
		v, ok, _ := d.Get(starlark.String(k))
		if !ok {
			return nil
		}
		return v
	}
	if get("id") != starlark.String("int_1") || get("resume_url") != starlark.String("https://x/int_1/resume") ||
		get("resume_token") != starlark.String("tok_1") || get("kind") != nil {
		t.Fatalf("bad new result: %v", d)
	}
	if e, ok := get("expires_in_s").(starlark.Int); !ok {
		t.Fatalf("expires_in_s not echoed: %v", get("expires_in_s"))
	} else if i, _ := e.Int64(); i != 60 {
		t.Fatalf("expires_in_s=%d", i)
	}
	if _, err := starlark.Call(thread, newFn, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := starlark.Call(thread, newFn, nil, nil); err == nil ||
		!strings.Contains(err.Error(), "interrupt limit for this step reached") {
		t.Fatalf("expected slot limit error, got %v", err)
	}
	var ops []string
	for _, r := range obs.recs {
		if r.Call.Capability != Name || r.Decision != types.DecisionAllow {
			t.Fatalf("bad record: %+v", r)
		}
		ops = append(ops, string(r.Call.Op))
	}
	if strings.Join(ops, ",") != "run_id,new,new,new" {
		t.Fatalf("ops: %v", ops)
	}
}

func TestNewValidation(t *testing.T) {
	mod := Binding(host(), armedGate(nil))
	thread := &starlark.Thread{Name: "t"}
	kw := func(k string, v starlark.Value) starlark.Tuple { return starlark.Tuple{starlark.String(k), v} }
	for _, bad := range []starlark.Tuple{
		kw("expires_in_s", starlark.MakeInt(0)),
		kw("expires_in_s", starlark.MakeInt(2592001)),
		kw("expires_in_s", starlark.String("1")),
		kw("fire_in_s", starlark.MakeInt(5)),
		kw("kind", starlark.String("approval")),
	} {
		if _, err := starlark.Call(thread, attr(t, mod, "new"), nil, []starlark.Tuple{bad}); err == nil {
			t.Fatalf("accepted %v", bad)
		}
	}
	empty := Binding(&Host{RunID: "run_1", Secrets: secrets.NewSet()}, armedGate(nil))
	if _, err := starlark.Call(thread, attr(t, empty, "new"), nil, nil); err == nil ||
		!strings.Contains(err.Error(), "--secret-key") {
		t.Fatalf("no slots: %v", err)
	}
}

func TestWaitFlow(t *testing.T) {
	h := host("on_ok", "on_exp")
	mod := Binding(h, armedGate(nil))
	thread := &starlark.Thread{Name: "t"}
	waitFn := attr(t, mod, "wait")

	if _, err := starlark.Call(thread, attr(t, mod, "new"), nil, nil); err != nil {
		t.Fatal(err)
	}
	kw := func(k string, v starlark.Value) starlark.Tuple { return starlark.Tuple{starlark.String(k), v} }

	// wait on an unreserved id fails inside the gate.
	if _, err := starlark.Call(thread, waitFn, starlark.Tuple{starlark.String("int_9")}, []starlark.Tuple{
		kw("handler", starlark.String("on_ok")), kw("context", dict(t)),
	}); err == nil {
		t.Fatal("wait on unreserved id accepted")
	}

	// A name that is not a handler function fails, for handler and
	// on_expire alike.
	if _, err := starlark.Call(thread, waitFn, starlark.Tuple{starlark.String("int_1")}, []starlark.Tuple{
		kw("handler", starlark.String("nope")), kw("context", dict(t)),
	}); err == nil {
		t.Fatal("unknown handler accepted")
	}
	if _, err := starlark.Call(thread, waitFn, starlark.Tuple{starlark.String("int_1")}, []starlark.Tuple{
		kw("handler", starlark.String("on_ok")), kw("on_expire", starlark.String("nope")), kw("context", dict(t)),
	}); err == nil {
		t.Fatal("unknown on_expire accepted")
	}
	// Context must be a dict.
	if _, err := starlark.Call(thread, waitFn, starlark.Tuple{starlark.String("int_1")}, []starlark.Tuple{
		kw("handler", starlark.String("on_ok")), kw("context", starlark.String("x")),
	}); err == nil {
		t.Fatal("non-dict context accepted")
	}

	// A valid wait records Waiting and unwinds with ErrWait.
	_, err := starlark.Call(thread, waitFn, starlark.Tuple{starlark.String("int_1")}, []starlark.Tuple{
		kw("handler", starlark.String("on_ok")),
		kw("on_expire", starlark.String("on_exp")),
		kw("context", dict(t, "k", starlark.String("v"))),
	})
	if !errors.Is(err, ErrWait) {
		t.Fatalf("expected ErrWait, got %v", err)
	}
	w := h.Waiting
	if w == nil || w.InterruptID != "int_1" || w.Handler != "on_ok" || w.OnExpire != "on_exp" ||
		string(w.Context) != `{"k":"v"}` {
		t.Fatalf("bad waiting: %+v", w)
	}
}

func TestWaitRejectsSecretContext(t *testing.T) {
	secs := secrets.NewSet()
	secs.Add("TOK", []byte("s3cr3t"), nil)
	h := host("on_ok")
	h.Secrets = secs
	mod := Binding(h, armedGate(nil))
	thread := &starlark.Thread{Name: "t"}
	waitFn := attr(t, mod, "wait")
	if _, err := starlark.Call(thread, attr(t, mod, "new"), nil, nil); err != nil {
		t.Fatal(err)
	}
	kw := func(k string, v starlark.Value) starlark.Tuple { return starlark.Tuple{starlark.String(k), v} }
	_, err := starlark.Call(thread, waitFn, starlark.Tuple{starlark.String("int_1")}, []starlark.Tuple{
		kw("handler", starlark.String("on_ok")), kw("context", dict(t, "k", starlark.String("s3cr3t"))),
	})
	if err == nil || !strings.Contains(err.Error(), "secret") {
		t.Fatalf("secret context: %v", err)
	}
	if h.Waiting != nil {
		t.Fatal("waiting recorded for a rejected context")
	}
}

func TestNoRun(t *testing.T) {
	h := &Host{Secrets: secrets.NewSet(), ToJSON: toJSON}
	mod := Binding(h, armedGate(nil))
	thread := &starlark.Thread{Name: "t"}
	if _, err := callModule(t, mod, thread, "run_id"); err == nil ||
		!strings.Contains(err.Error(), "application runs") {
		t.Fatalf("run_id outside run: %v", err)
	}
}

func TestFactoryRefusesConfig(t *testing.T) {
	f := Factory()
	if _, err := f.Validate(nil, capability.ServerLimits{}); err == nil {
		t.Fatal("interrupt spec config accepted")
	}
	if f.Name() != Name {
		t.Fatal("wrong name")
	}
	if len(f.Ops()) != 3 || len(f.Help(nil)) != 4 {
		t.Fatal("missing op/help docs")
	}
}
