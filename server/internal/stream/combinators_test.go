package stream

import (
	"context"
	"errors"
	"testing"

	"go.starlark.net/starlark"
)

func TestNewListLazyAndOrdered(t *testing.T) {
	sc, thread := testScope(t)
	l := starlark.NewList([]starlark.Value{starlark.MakeInt(1), starlark.MakeInt(2)})
	st, err := NewList(sc, l)
	if err != nil {
		t.Fatal(err)
	}
	if st.Pulls() != 0 {
		t.Fatal("construction consumed the list")
	}
	v, ok, err := st.Pull(thread)
	if err != nil || !ok || v.String() != "1" {
		t.Fatalf("first: %v %v %v", v, ok, err)
	}
	if err := l.Append(starlark.MakeInt(9)); err == nil {
		t.Fatal("list mutated during iteration")
	}
	v, ok, err = st.Pull(thread)
	if err != nil || !ok || v.String() != "2" {
		t.Fatalf("second: %v %v %v", v, ok, err)
	}
	if _, ok, err := st.Pull(thread); ok || err != nil {
		t.Fatalf("EOF: %v %v", ok, err)
	}
	if st.State() != Exhausted {
		t.Fatalf("state %v", st.State())
	}
	if err := l.Append(starlark.MakeInt(3)); err != nil {
		t.Fatalf("append after exhaustion: %v", err)
	}
}

func TestNewListTupleAndEmpty(t *testing.T) {
	sc, thread := testScope(t)
	st, err := NewList(sc, starlark.Tuple{starlark.String("a")})
	if err != nil {
		t.Fatal(err)
	}
	v, ok, _ := st.Pull(thread)
	if !ok || v.String() != `"a"` {
		t.Fatalf("tuple item: %v %v", v, ok)
	}
	empty, err := NewList(sc, starlark.NewList(nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := empty.Pull(thread); ok || err != nil {
		t.Fatalf("empty list: %v %v", ok, err)
	}
}

func TestNewListRejectsOtherValues(t *testing.T) {
	sc, _ := testScope(t)
	for _, v := range []starlark.Value{
		starlark.String("ab"),
		starlark.MakeInt(1),
		starlark.NewDict(0),
		starlark.NewSet(1),
	} {
		if _, err := NewList(sc, v); err == nil {
			t.Fatalf("NewList(%v) succeeded", v)
		}
	}
	if sc.Live() != 0 {
		t.Fatalf("rejected construction left %d live streams", sc.Live())
	}
}

func TestMapDefersCallbacks(t *testing.T) {
	sc, thread := testScope(t)
	pulls, calls := 0, 0
	src, err := New(sc, func(_ *starlark.Thread) (starlark.Value, bool, error) {
		pulls++
		if pulls > 2 {
			return starlark.None, false, nil
		}
		return starlark.MakeInt(pulls), true, nil
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	fn := starlark.NewBuiltin("double", func(_ *starlark.Thread, _ *starlark.Builtin, a starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
		calls++
		i, _ := a[0].(starlark.Int).Int64()
		return starlark.MakeInt64(i * 2), nil
	})
	m, err := src.Map(fn)
	if err != nil {
		t.Fatal(err)
	}
	if pulls != 0 || calls != 0 {
		t.Fatalf("construction ran pulls=%d calls=%d", pulls, calls)
	}
	v, ok, err := m.Pull(thread)
	if err != nil || !ok || v.String() != "2" {
		t.Fatalf("first: %v %v %v", v, ok, err)
	}
	if pulls != 1 || calls != 1 {
		t.Fatalf("pulls=%d calls=%d", pulls, calls)
	}
	if got := drain(t, m.Iterate()); len(got) != 1 || got[0].String() != "4" {
		t.Fatalf("rest %v", got)
	}
	if pulls != 3 || calls != 2 {
		t.Fatalf("pulls=%d calls=%d", pulls, calls)
	}
	if src.State() != Exhausted || m.State() != Exhausted {
		t.Fatalf("states %v %v", src.State(), m.State())
	}
}

func TestMapClaimsSource(t *testing.T) {
	sc, _ := testScope(t)
	src := listStream(t, sc, starlark.MakeInt(1))
	fn := starlark.NewBuiltin("id", func(_ *starlark.Thread, _ *starlark.Builtin, a starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
		return a[0], nil
	})
	if _, err := src.Map(fn); err != nil {
		t.Fatal(err)
	}
	if _, err := src.Map(fn); err == nil {
		t.Fatal("second claim of the same source succeeded")
	}
	it := src.Iterate()
	var v starlark.Value
	if it.Next(&v) {
		t.Fatal("owned source iterated")
	}
	it.Done()
}

func TestMapCallbackErrorSticky(t *testing.T) {
	sc, thread := testScope(t)
	src := listStream(t, sc, starlark.MakeInt(1))
	boom := errors.New("map exploded")
	fn := starlark.NewBuiltin("bad", func(_ *starlark.Thread, _ *starlark.Builtin, _ starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
		return nil, boom
	})
	m, err := src.Map(fn)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Pull(thread); !errors.Is(err, boom) {
		t.Fatalf("pull: %v", err)
	}
	if !errors.Is(sc.Err(), boom) || m.State() != Failed {
		t.Fatalf("sticky %v state %v", sc.Err(), m.State())
	}
}

func TestReduceSingleResult(t *testing.T) {
	sc, thread := testScope(t)
	src := listStream(t, sc, starlark.MakeInt(1), starlark.MakeInt(2), starlark.MakeInt(3))
	calls := 0
	add := starlark.NewBuiltin("add", func(_ *starlark.Thread, _ *starlark.Builtin, a starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
		calls++
		x, _ := a[0].(starlark.Int).Int64()
		y, _ := a[1].(starlark.Int).Int64()
		return starlark.MakeInt64(x + y), nil
	})
	r, err := src.Reduce(add, starlark.MakeInt(10))
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("reduce folded at construction")
	}
	v, ok, err := r.Pull(thread)
	if err != nil || !ok || v.String() != "16" {
		t.Fatalf("result: %v %v %v", v, ok, err)
	}
	if calls != 3 {
		t.Fatalf("calls=%d", calls)
	}
	if _, ok, err := r.Pull(thread); ok || err != nil {
		t.Fatalf("second pull: %v %v", ok, err)
	}
	if r.State() != Exhausted || src.State() != Exhausted {
		t.Fatalf("states %v %v", r.State(), src.State())
	}
}

func TestReduceEmptyYieldsInitial(t *testing.T) {
	sc, thread := testScope(t)
	src := listStream(t, sc)
	r, err := src.Reduce(starlark.NewBuiltin("never", func(_ *starlark.Thread, _ *starlark.Builtin, _ starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
		return nil, errors.New("called on empty")
	}), starlark.String("seed"))
	if err != nil {
		t.Fatal(err)
	}
	v, ok, err := r.Pull(thread)
	if err != nil || !ok || v.String() != `"seed"` {
		t.Fatalf("initial: %v %v %v", v, ok, err)
	}
}

func TestFlatMapMixedInners(t *testing.T) {
	sc, thread := testScope(t)
	src := listStream(t, sc, starlark.MakeInt(1), starlark.MakeInt(2), starlark.MakeInt(3))
	fn := starlark.NewBuiltin("expand", func(_ *starlark.Thread, _ *starlark.Builtin, a starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
		i, _ := a[0].(starlark.Int).Int64()
		switch i {
		case 1:
			return starlark.NewList([]starlark.Value{starlark.String("a"), starlark.String("b")}), nil
		case 2:
			return starlark.Tuple{starlark.String("c")}, nil
		}
		return NewList(sc, starlark.Tuple{starlark.String("d"), starlark.String("e")})
	})
	f, err := src.FlatMap(fn)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for {
		v, ok, err := f.Pull(thread)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		got = append(got, v.String())
	}
	want := []string{`"a"`, `"b"`, `"c"`, `"d"`, `"e"`}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
	if sc.Live() != 0 {
		t.Fatalf("live=%d after full drain", sc.Live())
	}
}

func TestFlatMapEmptyInners(t *testing.T) {
	sc, thread := testScope(t)
	src := listStream(t, sc, starlark.MakeInt(1), starlark.MakeInt(2), starlark.MakeInt(3))
	fn := starlark.NewBuiltin("sparse", func(_ *starlark.Thread, _ *starlark.Builtin, a starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
		i, _ := a[0].(starlark.Int).Int64()
		if i == 2 {
			return starlark.NewList([]starlark.Value{starlark.MakeInt(99)}), nil
		}
		return starlark.NewList(nil), nil
	})
	f, err := src.FlatMap(fn)
	if err != nil {
		t.Fatal(err)
	}
	v, ok, err := f.Pull(thread)
	if err != nil || !ok || v.String() != "99" {
		t.Fatalf("only item: %v %v %v", v, ok, err)
	}
	if _, ok, _ := f.Pull(thread); ok {
		t.Fatal("extra item")
	}
}

func TestFlatMapEarlyCloseCleansInnerAndUpstream(t *testing.T) {
	sc, thread := testScope(t)
	var srcClosed, innerClosed int
	src, err := New(sc, func(_ *starlark.Thread) (starlark.Value, bool, error) {
		return starlark.MakeInt(1), true, nil
	}, func() error { srcClosed++; return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	inner, err := New(sc, func(_ *starlark.Thread) (starlark.Value, bool, error) {
		return starlark.MakeInt(7), true, nil
	}, func() error { innerClosed++; return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	fn := starlark.NewBuiltin("f", func(_ *starlark.Thread, _ *starlark.Builtin, _ starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
		return inner, nil
	})
	f, err := src.FlatMap(fn)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := f.Pull(thread); err != nil || !ok {
		t.Fatalf("first inner item: %v %v", ok, err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if innerClosed != 1 || srcClosed != 1 {
		t.Fatalf("innerClosed=%d srcClosed=%d", innerClosed, srcClosed)
	}
	if inner.State() != Closed || src.State() != Closed {
		t.Fatalf("states %v %v", inner.State(), src.State())
	}
	if sc.Live() != 0 {
		t.Fatalf("live=%d", sc.Live())
	}
}

func TestFlatMapRejectsBadInners(t *testing.T) {
	for _, tc := range []struct {
		name  string
		inner func(t *testing.T, sc *Scope, self, src *Stream) starlark.Value
	}{
		{"nonstream", func(_ *testing.T, _ *Scope, _, _ *Stream) starlark.Value { return starlark.MakeInt(1) }},
		{"self", func(_ *testing.T, _ *Scope, self, _ *Stream) starlark.Value { return self }},
		{"upstream", func(_ *testing.T, _ *Scope, _, src *Stream) starlark.Value { return src }},
		{"foreign", func(t *testing.T, _ *Scope, _, _ *Stream) starlark.Value {
			sc2, _ := testScope(t)
			t.Cleanup(func() { sc2.Close() })
			return listStream(t, sc2, starlark.MakeInt(1))
		}},
		{"owned", func(t *testing.T, sc *Scope, _, _ *Stream) starlark.Value {
			in := listStream(t, sc, starlark.MakeInt(1))
			if _, err := in.Map(starlark.NewBuiltin("id", func(_ *starlark.Thread, _ *starlark.Builtin, a starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
				return a[0], nil
			})); err != nil {
				t.Fatal(err)
			}
			return in
		}},
		{"iterating", func(t *testing.T, sc *Scope, _, _ *Stream) starlark.Value {
			in := listStream(t, sc, starlark.MakeInt(1))
			it := in.Iterate()
			t.Cleanup(it.Done)
			return in
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sc, thread := testScope(t)
			src := listStream(t, sc, starlark.MakeInt(1))
			var flat *Stream
			var fn starlark.Callable = starlark.NewBuiltin("f", func(_ *starlark.Thread, _ *starlark.Builtin, _ starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
				return tc.inner(t, sc, flat, src), nil
			})
			var err error
			flat, err = src.FlatMap(fn)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := flat.Pull(thread); err == nil {
				t.Fatal("bad inner accepted")
			}
			if sc.Err() == nil {
				t.Fatal("failure not latched")
			}
			if flat.State() != Failed {
				t.Fatalf("state %v", flat.State())
			}
		})
	}
}

func TestFlatMapCallbackErrorSticky(t *testing.T) {
	sc, thread := testScope(t)
	src := listStream(t, sc, starlark.MakeInt(1))
	boom := errors.New("flatmap exploded")
	f, err := src.FlatMap(starlark.NewBuiltin("bad", func(_ *starlark.Thread, _ *starlark.Builtin, _ starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
		return nil, boom
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.Pull(thread); !errors.Is(err, boom) {
		t.Fatalf("pull: %v", err)
	}
	if !errors.Is(sc.Err(), boom) {
		t.Fatalf("sticky %v", sc.Err())
	}
}

func TestMethodArgValidation(t *testing.T) {
	sc, thread := testScope(t)
	st := listStream(t, sc, starlark.MakeInt(1))
	call := func(attr string, args ...starlark.Value) error {
		m, err := st.Attr(attr)
		if err != nil || m == nil {
			t.Fatalf("attr %s missing", attr)
		}
		_, err = starlark.Call(thread, m, starlark.Tuple(args), nil)
		return err
	}
	for _, attr := range []string{"map", "flatmap"} {
		if err := call(attr); err == nil {
			t.Fatalf("%s() with no args succeeded", attr)
		}
		if err := call(attr, starlark.MakeInt(1)); err == nil {
			t.Fatalf("%s(1) succeeded", attr)
		}
	}
	if err := call("reduce", starlark.MakeInt(1)); err == nil {
		t.Fatal("reduce(1) succeeded")
	}
	if err := call("map", starlark.Universe["len"]); err != nil {
		t.Fatalf("map(len): %v", err)
	}
	if sc.Live() != 2 {
		t.Fatalf("live=%d", sc.Live())
	}
	for _, attr := range []string{"map", "reduce", "flatmap", "next", "close"} {
		found := false
		for _, n := range st.AttrNames() {
			if n == attr {
				found = true
			}
		}
		if !found {
			t.Fatalf("AttrNames missing %s", attr)
		}
	}
	m, _ := st.Attr("map")
	if _, err := starlark.Call(thread, m, nil, []starlark.Tuple{{starlark.String("fn"), starlark.MakeInt(1)}}); err == nil {
		t.Fatal("map(fn=1) succeeded")
	}
}

func TestCombinatorStaleStreamRejected(t *testing.T) {
	sc, thread := testScope(t)
	st := listStream(t, sc, starlark.MakeInt(1))
	_ = sc.Close()
	for _, attr := range []string{"map", "reduce", "flatmap"} {
		m, _ := st.Attr(attr)
		args := starlark.Tuple{starlark.NewBuiltin("id", func(_ *starlark.Thread, _ *starlark.Builtin, a starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
			return a[0], nil
		})}
		if attr == "reduce" {
			args = append(args, starlark.None)
		}
		if _, err := starlark.Call(thread, m, args, nil); !errors.Is(err, ErrOutOfScope) {
			t.Fatalf("stale %s: %v", attr, err)
		}
	}
}

func TestCombinatorCancelSkipsCallbacks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	thread := &starlark.Thread{Name: "x"}
	sc := NewScope(ctx, thread, nil)
	sc.Activate()
	thread.SetLocal(ThreadKey, sc)
	pulls, calls := 0, 0
	src, err := New(sc, func(_ *starlark.Thread) (starlark.Value, bool, error) {
		pulls++
		return starlark.MakeInt(1), true, nil
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	m, err := src.Map(starlark.NewBuiltin("count", func(_ *starlark.Thread, _ *starlark.Builtin, a starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
		calls++
		return a[0], nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, _, err := m.Pull(thread); err == nil {
		t.Fatal("pull succeeded after cancellation")
	}
	if pulls != 0 || calls != 0 {
		t.Fatalf("pulls=%d calls=%d after cancel", pulls, calls)
	}
}

func TestNewListFailedRegistrationReleasesIterator(t *testing.T) {
	l := starlark.NewList([]starlark.Value{starlark.MakeInt(1)})
	if _, err := NewList(nil, l); err == nil {
		t.Fatal("nil scope accepted")
	}
	if err := l.Append(starlark.MakeInt(2)); err != nil {
		t.Fatalf("nil-scope registration leaked the iterator: %v", err)
	}
	thread := &starlark.Thread{Name: "t"}
	inactive := NewScope(context.Background(), thread, nil)
	if _, err := NewList(inactive, l); !errors.Is(err, ErrOutOfScope) {
		t.Fatalf("inactive scope: %v", err)
	}
	if err := l.Append(starlark.MakeInt(3)); err != nil {
		t.Fatalf("inactive-scope registration leaked the iterator: %v", err)
	}
	sc, _ := testScope(t)
	pull := func(_ *starlark.Thread) (starlark.Value, bool, error) { return starlark.None, false, nil }
	for i := 0; i < MaxLive; i++ {
		if _, err := New(sc, pull, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := NewList(sc, l); err == nil {
		t.Fatal("cap exceeded")
	}
	if err := l.Append(starlark.MakeInt(4)); err != nil {
		t.Fatalf("cap-failed registration leaked the iterator: %v", err)
	}
}
