package capability

import (
	"context"
	"errors"
	"testing"

	"go.starlark.net/starlark"

	"calcside/internal/stream"
	"calcside/internal/types"
)

// scopedThread returns an armed gate, an activated stream scope and the
// thread that carries it.
func scopedThread(t *testing.T, hooks []Hook, obs Observer) (*Gate, *stream.Scope, *starlark.Thread) {
	t.Helper()
	g := NewGate(GateOwner{InstanceID: "ins_1", UserID: "u"}, hooks, obs)
	g.Arm(ExecContext{ExecID: "e1", InstanceID: "ins_1", UserID: "u"})
	thread := &starlark.Thread{Name: "exec e1"}
	ctx := context.Background()
	sc := stream.NewScope(ctx, thread, nil)
	sc.Activate()
	thread.SetLocal(stream.ThreadKey, sc)
	thread.SetLocal(ContextKey, sc.Context())
	return g, sc, thread
}

func pullSource(t *testing.T, sc *stream.Scope, items ...starlark.Value) (*stream.Stream, *int) {
	t.Helper()
	i := 0
	closed := 0
	st, err := stream.New(sc, func(_ *starlark.Thread) (starlark.Value, bool, error) {
		if i >= len(items) {
			return starlark.None, false, nil
		}
		v := items[i]
		i++
		return v, true, nil
	}, func() error { closed++; return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	return st, &closed
}

type actionFilter struct {
	deny   map[string]bool
	after  bool
	reason string
	seen   []string
}

func (h *actionFilter) Name() string { return "filter" }
func (h *actionFilter) Before(_ context.Context, c *Call) error {
	a, _ := c.Args[StreamActionArg].(string)
	h.seen = append(h.seen, "before:"+a)
	if h.deny[a] {
		return errors.New(h.reason)
	}
	return nil
}
func (h *actionFilter) After(_ context.Context, c *Call, r *Result) error {
	a, _ := c.Args[StreamActionArg].(string)
	h.seen = append(h.seen, "after:"+a)
	if h.after && h.deny[a] {
		return errors.New(h.reason)
	}
	return nil
}

func TestWrapStreamPullThroughGate(t *testing.T) {
	g, sc, thread := scopedThread(t, nil, nil)
	src, closed := pullSource(t, sc, starlark.MakeInt(1), starlark.MakeInt(2))
	w, err := g.WrapStream(thread, types.CapExt, "ext.t.stream", map[string]any{"arg": 1}, map[string]any{"source": "x"}, src)
	if err != nil {
		t.Fatal(err)
	}
	v, ok, err := w.Pull(thread)
	if err != nil || !ok || v.String() != "1" {
		t.Fatalf("pull: %v %v %v", v, ok, err)
	}
	// source cursor advanced through the wrapper
	if w.Pulls() != 1 {
		t.Fatalf("pulls %d", w.Pulls())
	}
	// source is claimed: independent iteration rejected
	it := src.Iterate()
	var x starlark.Value
	if it.Next(&x) {
		t.Fatal("owned source iterated independently")
	}
	it.Done()
	_ = closed
}

func TestWrapStreamPullDeny(t *testing.T) {
	hook := &actionFilter{deny: map[string]bool{"pull": true}, reason: "no pulls"}
	g, sc, thread := scopedThread(t, []Hook{hook}, nil)
	src, closed := pullSource(t, sc, starlark.MakeInt(1))
	w, err := g.WrapStream(thread, types.CapExt, "ext.t.stream", nil, nil, src)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = w.Pull(thread)
	var denied *DeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("pull not denied: %v", err)
	}
	if !errors.Is(sc.Err(), err) && sc.Err() == nil {
		t.Fatal("denial not latched")
	}
	// denial closed the stream; the source released natively
	if w.State() != stream.Failed {
		t.Fatalf("state %v", w.State())
	}
	if *closed != 1 {
		t.Fatalf("source closed %d times", *closed)
	}
}

func TestWrapStreamCloseDenyStillCloses(t *testing.T) {
	hook := &actionFilter{deny: map[string]bool{"close": true}, reason: "no close record"}
	g, sc, thread := scopedThread(t, []Hook{hook}, nil)
	src, closed := pullSource(t, sc)
	w, err := g.WrapStream(thread, types.CapExt, "ext.t.stream", nil, nil, src)
	if err != nil {
		t.Fatal(err)
	}
	err = w.Close()
	if err == nil {
		t.Fatal("expected close denial to surface")
	}
	if *closed != 1 {
		t.Fatalf("native close ran %d times", *closed)
	}
}

func TestWrapStreamAuditPhases(t *testing.T) {
	obs := &recordingObs{}
	g, sc, thread := scopedThread(t, nil, obs)
	src, _ := pullSource(t, sc, starlark.MakeInt(1))
	w, err := g.WrapStream(thread, types.CapExt, "ext.t.stream", map[string]any{"arg": 1}, nil, src)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.Pull(thread); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.Pull(thread); err != nil { // EOF
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	var pulls, closes int
	var closeRec *Record
	for i := range obs.recs {
		r := &obs.recs[i]
		a, _ := r.Call.Args[StreamActionArg].(string)
		switch a {
		case "pull":
			pulls++
			if _, ok := r.Call.Args[StreamIDArg]; !ok {
				t.Fatal("pull record missing stream_id")
			}
		case "close":
			closes++
			closeRec = r
		}
	}
	if pulls != 2 || closes != 1 {
		t.Fatalf("records pulls=%d closes=%d", pulls, closes)
	}
	if closeRec == nil || closeRec.Decision != types.DecisionAllow {
		t.Fatal("missing terminal close record")
	}
}

func TestWrapStreamTeardown(t *testing.T) {
	obs := &recordingObs{}
	g, sc, thread := scopedThread(t, nil, obs)
	src, closed := pullSource(t, sc)
	if _, err := g.WrapStream(thread, types.CapExt, "ext.t.stream", nil, nil, src); err != nil {
		t.Fatal(err)
	}
	// never consumed: scope teardown must emit the terminal record and
	// close natively while the gate is still armed
	if err := sc.Close(); err != nil {
		t.Fatal(err)
	}
	if *closed != 1 {
		t.Fatalf("native close %d", *closed)
	}
	var hasClose bool
	for _, r := range obs.recs {
		if a, _ := r.Call.Args[StreamActionArg].(string); a == "close" {
			hasClose = true
		}
	}
	if !hasClose {
		t.Fatal("no terminal close record at teardown")
	}
}

func TestWrapStreamNoScope(t *testing.T) {
	g := NewGate(GateOwner{}, nil, nil)
	g.Arm(ExecContext{ExecID: "e"})
	thread := &starlark.Thread{Name: "no scope"} // no stream scope local
	sc := stream.NewScope(context.Background(), thread, nil)
	sc.Activate()
	src, _ := pullSource(t, sc, starlark.MakeInt(1))
	w, err := g.WrapStream(thread, types.CapExt, "op", nil, nil, src)
	if w != nil || err == nil {
		t.Fatal("wrap without scope succeeded")
	}
	if src.State() != stream.Closed {
		t.Fatalf("source not closed: %v", src.State())
	}
}

// afterOnly denies After for the given actions only; Before always
// allows so callbacks DO run but the chunk is never exposed.
type afterOnly struct{ deny map[string]bool }

func (h afterOnly) Name() string                        { return "afteronly" }
func (h afterOnly) Before(context.Context, *Call) error { return nil }
func (h afterOnly) After(_ context.Context, c *Call, r *Result) error {
	a, _ := c.Args[StreamActionArg].(string)
	if h.deny[a] {
		return errors.New("after deny " + a)
	}
	return nil
}

func TestWrapStreamAfterOnlyDeny(t *testing.T) {
	g, sc, thread := scopedThread(t, []Hook{afterOnly{deny: map[string]bool{"pull": true}}}, nil)
	pulled := false
	src, err := stream.New(sc, func(_ *starlark.Thread) (starlark.Value, bool, error) {
		pulled = true
		return starlark.MakeInt(1), true, nil
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	w, err := g.WrapStream(thread, types.CapExt, "ext.t.stream", nil, nil, src)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.Pull(thread); err == nil {
		t.Fatal("after-deny not surfaced")
	}
	if !pulled {
		t.Fatal("callback did not run under After denial")
	}
}

func TestWrapStreamSameIDAcrossPhases(t *testing.T) {
	obs := &recordingObs{}
	g, sc, thread := scopedThread(t, nil, obs)
	src, _ := pullSource(t, sc, starlark.MakeInt(1))
	args := map[string]any{StreamIDArg: sc.Reserve()}
	w, err := g.WrapStream(thread, types.CapExt, "ext.t.stream", args, nil, src)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _ = w.Pull(thread)
	_ = w.Close()
	want := w.ID()
	var got []any
	for _, r := range obs.recs {
		got = append(got, r.Call.Args[StreamIDArg])
	}
	for _, id := range got {
		if id != want {
			t.Fatalf("phase ids %v want %v", got, want)
		}
	}
}

func TestWrapStreamFailedTerminalErr(t *testing.T) {
	obs := &recordingObs{}
	g, sc, thread := scopedThread(t, nil, obs)
	boom := errors.New("read exploded")
	src, err := stream.New(sc, func(_ *starlark.Thread) (starlark.Value, bool, error) {
		return nil, false, boom
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	w, err := g.WrapStream(thread, types.CapExt, "ext.t.stream", nil, nil, src)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.Pull(thread); !errors.Is(err, boom) {
		t.Fatalf("pull: %v", err)
	}
	_ = w.Close()
	var close *Record
	for i := range obs.recs {
		if obs.recs[i].Call.Args[StreamActionArg] == "close" {
			close = &obs.recs[i]
		}
	}
	if close == nil {
		t.Fatal("no close record")
	}
	if !errors.Is(close.Err, boom) {
		t.Fatalf("terminal err %v", close.Err)
	}
	if close.Call.Args["stream_status"] != "error" {
		t.Fatalf("status %v", close.Call.Args["stream_status"])
	}
}
