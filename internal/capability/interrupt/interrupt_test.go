package interrupt

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"go.starlark.net/starlark"

	"calcside/internal/capability"
	"calcside/internal/secrets"
	"calcside/internal/serving"
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

func compile(t *testing.T, raw string) *serving.Schema {
	t.Helper()
	s, err := serving.CompileSchema([]byte(raw), serving.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func armedGate(obs capability.Observer) *capability.Gate {
	g := capability.NewGate(capability.GateOwner{}, nil, obs)
	g.Arm(capability.ExecContext{ExecID: "e1"})
	return g
}

func host(handlers map[string]*serving.Schema) *Host {
	return &Host{
		RunID:    "run_1",
		Step:     0,
		Handlers: handlers,
		Secrets:  secrets.NewSet(),
		ToJSON:   toJSON,
		Slots: []Slot{
			{ID: "int_1", ConsoleURL: "https://c/int_1"},
			{ID: "int_2", ConsoleURL: "https://c/int_2", CallbackURL: "https://cb/int_2", Token: "cb_secret_tok"},
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

func TestRunIDAndNewApproval(t *testing.T) {
	h := host(nil)
	obs := &recObserver{}
	mod := Binding(h, armedGate(obs))
	thread := &starlark.Thread{Name: "t"}
	thread.SetLocal(capability.ContextKey, context.Background())

	rid, err := callModule(t, mod, thread, "run_id")
	if err != nil || rid.(starlark.String) != "run_1" {
		t.Fatalf("run_id: %v %v", rid, err)
	}

	kwargs := []starlark.Tuple{
		{starlark.String("title"), starlark.String("Approve it")},
		{starlark.String("assignees"), starlark.NewList([]starlark.Value{starlark.String("usr_a")})},
		{starlark.String("expires_in_s"), starlark.MakeInt(60)},
	}
	newFn := attr(t, mod, "new")
	v, err := starlark.Call(thread, newFn, starlark.Tuple{starlark.String("approval")}, kwargs)
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
	if string(get("id").(starlark.String)) != "int_1" || string(get("kind").(starlark.String)) != "approval" ||
		string(get("console_url").(starlark.String)) != "https://c/int_1" {
		t.Fatalf("bad new result")
	}
	if get("callback_url") != nil || get("callback_token") != nil {
		t.Fatal("approval leaked callback fields")
	}
	if e, ok := get("expires_in_s").(starlark.Int); !ok {
		t.Fatalf("expires_in_s not echoed: %v", get("expires_in_s"))
	} else if i, _ := e.Int64(); i != 60 {
		t.Fatalf("expires_in_s=%d", i)
	}
	if _, ok := h.reservations["int_1"]; !ok {
		t.Fatal("reservation not recorded")
	}

	// The second slot is a callback and carries url+token.
	v, err = starlark.Call(thread, newFn, starlark.Tuple{starlark.String("callback")}, nil)
	if err != nil {
		t.Fatalf("new callback: %v", err)
	}
	d = v.(*starlark.Dict)
	get = func(k string) starlark.Value {
		v, ok, _ := d.Get(starlark.String(k))
		if !ok {
			return nil
		}
		return v
	}
	if string(get("callback_url").(starlark.String)) != "https://cb/int_2" ||
		string(get("callback_token").(starlark.String)) != "cb_secret_tok" {
		t.Fatalf("callback fields missing")
	}

	// No slots left.
	if _, err := starlark.Call(thread, newFn, starlark.Tuple{starlark.String("approval")}, nil); err == nil ||
		!strings.Contains(err.Error(), "interrupt limit for this step reached") {
		t.Fatalf("expected slot limit error, got %v", err)
	}

	// Audit recorded allowed ops with JSON-able metadata only.
	var kinds []string
	for _, r := range obs.recs {
		if r.Call.Capability != Name || r.Decision != types.DecisionAllow {
			t.Fatalf("bad record: %+v", r)
		}
		kinds = append(kinds, string(r.Call.Op))
	}
	if strings.Join(kinds, ",") != "run_id,new,new,new" {
		t.Fatalf("ops: %v", kinds)
	}
}

func TestNewValidation(t *testing.T) {
	h := host(nil)
	mod := Binding(h, armedGate(nil))
	thread := &starlark.Thread{Name: "t"}
	newFn := attr(t, mod, "new")
	call := func(kind string, kwargs ...starlark.Tuple) error {
		_, err := starlark.Call(thread, newFn, starlark.Tuple{starlark.String(kind)}, kwargs)
		return err
	}
	kw := func(k string, v starlark.Value) starlark.Tuple { return starlark.Tuple{starlark.String(k), v} }

	if err := call("bogus"); err == nil {
		t.Fatal("bad kind accepted")
	}
	if err := call("timer"); err == nil || !strings.Contains(err.Error(), "fire_in_s") {
		t.Fatalf("timer without fire_in_s: %v", err)
	}
	if err := call("approval", kw("fire_in_s", starlark.MakeInt(5))); err == nil {
		t.Fatal("approval accepted fire_in_s")
	}
	if err := call("timer", kw("fire_in_s", starlark.MakeInt(5)), kw("expires_in_s", starlark.MakeInt(5))); err == nil {
		t.Fatal("timer accepted expires_in_s")
	}
	if err := call("timer", kw("fire_in_s", starlark.MakeInt(5)), kw("event_schema", dict(t))); err == nil {
		t.Fatal("timer accepted event_schema")
	}
	if err := call("approval", kw("expires_in_s", starlark.MakeInt(0))); err == nil {
		t.Fatal("zero expires_in_s accepted")
	}
	if err := call("approval", kw("expires_in_s", starlark.MakeInt(2592001))); err == nil {
		t.Fatal("oversized expires_in_s accepted")
	}
	if err := call("approval", kw("title", starlark.String(strings.Repeat("x", 201)))); err == nil {
		t.Fatal("long title accepted")
	}
	if err := call("approval", kw("assignees", starlark.NewList([]starlark.Value{starlark.String("bob")}))); err == nil {
		t.Fatal("non-usr assignee accepted")
	}
	if err := call("approval", kw("event_schema", dict(t, "type", starlark.String("string")))); err == nil {
		t.Fatal("non-object event_schema accepted")
	}
	if err := call("timer", kw("fire_in_s", starlark.MakeInt(60))); err != nil {
		t.Fatalf("valid timer rejected: %v", err)
	}
}

func TestWaitFlow(t *testing.T) {
	handlers := map[string]*serving.Schema{
		"on_ok":  compile(t, `{"type":"object","required":["k"],"properties":{"k":{"type":"string"}}}`),
		"on_exp": compile(t, `{"type":"object","required":["k"],"properties":{"k":{"type":"string"}}}`),
	}
	h := host(handlers)
	mod := Binding(h, armedGate(nil))
	thread := &starlark.Thread{Name: "t"}
	newFn := attr(t, mod, "new")
	waitFn := attr(t, mod, "wait")

	if _, err := starlark.Call(thread, newFn, starlark.Tuple{starlark.String("approval")}, nil); err != nil {
		t.Fatal(err)
	}
	kw := func(k string, v starlark.Value) starlark.Tuple { return starlark.Tuple{starlark.String(k), v} }

	// wait on an unreserved id fails inside the gate.
	if _, err := starlark.Call(thread, waitFn, starlark.Tuple{starlark.String("int_9")}, []starlark.Tuple{
		kw("handler", starlark.String("on_ok")), kw("context", dict(t)),
	}); err == nil {
		t.Fatal("wait on unreserved id accepted")
	}

	// Undeclared handler fails.
	if _, err := starlark.Call(thread, waitFn, starlark.Tuple{starlark.String("int_1")}, []starlark.Tuple{
		kw("handler", starlark.String("nope")), kw("context", dict(t)),
	}); err == nil {
		t.Fatal("undeclared handler accepted")
	}

	// Context missing a required field fails.
	if _, err := starlark.Call(thread, waitFn, starlark.Tuple{starlark.String("int_1")}, []starlark.Tuple{
		kw("handler", starlark.String("on_ok")), kw("context", dict(t)),
	}); err == nil {
		t.Fatal("invalid context accepted")
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
		w.Kind != "approval" || string(w.Context) != `{"k":"v"}` {
		t.Fatalf("bad waiting: %+v", w)
	}
}

func TestWaitRejectsSecretContext(t *testing.T) {
	secs := secrets.NewSet()
	secs.Add("TOK", []byte("s3cr3t"), nil)
	h := host(map[string]*serving.Schema{
		"on_ok": compile(t, `{"type":"object"}`),
	})
	h.Secrets = secs
	mod := Binding(h, armedGate(nil))
	thread := &starlark.Thread{Name: "t"}
	newFn := attr(t, mod, "new")
	waitFn := attr(t, mod, "wait")
	if _, err := starlark.Call(thread, newFn, starlark.Tuple{starlark.String("approval")}, nil); err != nil {
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
