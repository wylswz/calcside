package stream

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"go.starlark.net/starlark"
)

// testScope returns an activated standalone scope (default resolver:
// self-while-active) bound to a fresh thread.
func testScope(t *testing.T) (*Scope, *starlark.Thread) {
	t.Helper()
	thread := &starlark.Thread{Name: "test"}
	sc := NewScope(context.Background(), thread, nil)
	sc.Activate()
	thread.SetLocal(ThreadKey, sc)
	return sc, thread
}

func listStream(t *testing.T, sc *Scope, items ...starlark.Value) *Stream {
	t.Helper()
	i := 0
	st, err := New(sc, func(_ *starlark.Thread) (starlark.Value, bool, error) {
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

func drain(t *testing.T, it starlark.Iterator) []starlark.Value {
	t.Helper()
	defer it.Done()
	var out []starlark.Value
	var v starlark.Value
	for it.Next(&v) {
		out = append(out, v)
	}
	return out
}

func TestLazyConstruction(t *testing.T) {
	sc, thread := testScope(t)
	pulled := false
	st, err := New(sc, func(_ *starlark.Thread) (starlark.Value, bool, error) {
		pulled = true
		return starlark.None, false, nil
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if pulled {
		t.Fatal("pull ran at construction")
	}
	if _, _, err := st.Pull(thread); err != nil {
		t.Fatal(err)
	}
	if !pulled {
		t.Fatal("pull did not run on demand")
	}
}

func TestNonePayload(t *testing.T) {
	sc, thread := testScope(t)
	st := listStream(t, sc, starlark.None, starlark.MakeInt(1))
	v, ok, err := st.Pull(thread)
	if err != nil || !ok || v != starlark.None {
		t.Fatalf("None payload: v=%v ok=%v err=%v", v, ok, err)
	}
	v, ok, err = st.Pull(thread)
	if err != nil || !ok || v.String() != "1" {
		t.Fatalf("second: %v %v %v", v, ok, err)
	}
	if _, ok, _ := st.Pull(thread); ok {
		t.Fatal("expected EOF")
	}
	if st.State() != Exhausted {
		t.Fatalf("state %v", st.State())
	}
}

func TestMalformedCallback(t *testing.T) {
	sc, thread := testScope(t)
	for name, fn := range map[string]starlark.Value{
		"noncall": starlark.MakeInt(1),
		"nontuple": starlark.NewBuiltin("f", func(t *starlark.Thread, b *starlark.Builtin, a starlark.Tuple, k []starlark.Tuple) (starlark.Value, error) {
			return starlark.None, nil
		}),
		"badbool": starlark.NewBuiltin("f", func(t *starlark.Thread, b *starlark.Builtin, a starlark.Tuple, k []starlark.Tuple) (starlark.Value, error) {
			return starlark.Tuple{starlark.MakeInt(1), starlark.None}, nil
		}),
		"shortuple": starlark.NewBuiltin("f", func(t *starlark.Thread, b *starlark.Builtin, a starlark.Tuple, k []starlark.Tuple) (starlark.Value, error) {
			return starlark.Tuple{starlark.True}, nil
		}),
	} {
		st, err := NewCallback(sc, fn)
		if name == "noncall" {
			if err == nil {
				t.Fatalf("%s: expected error", name)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, _, err := st.Pull(thread); err == nil {
			t.Fatalf("%s: expected malformed-return error", name)
		}
		if st.State() != Failed {
			t.Fatalf("%s: state %v", name, st.State())
		}
		if sc.Err() == nil {
			t.Fatalf("%s: sticky not latched", name)
		}
		// fresh scope per case would be cleaner; reset sticky is not
		// supported, so stop after first sticky case is fine — but each
		// case should still run. Create a new scope instead:
		sc, thread = testScope(t)
	}
}

func TestCallbackRunsOnOwnerThread(t *testing.T) {
	sc, thread := testScope(t)
	var got *starlark.Thread
	fn := starlark.NewBuiltin("f", func(t *starlark.Thread, b *starlark.Builtin, a starlark.Tuple, k []starlark.Tuple) (starlark.Value, error) {
		got = t
		return starlark.Tuple{starlark.False, starlark.None}, nil
	})
	st, err := NewCallback(sc, fn)
	if err != nil {
		t.Fatal(err)
	}
	it := st.Iterate()
	var v starlark.Value
	for it.Next(&v) {
	}
	it.Done()
	if got != thread {
		t.Fatalf("callback ran on %p, want owner %p", got, thread)
	}
}

func TestCloseIdempotentCascade(t *testing.T) {
	sc, _ := testScope(t)
	closed := 0
	src, err := New(sc, func(_ *starlark.Thread) (starlark.Value, bool, error) {
		return starlark.None, false, nil
	}, func() error { closed++; return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	w, err := New(sc, func(t *starlark.Thread) (starlark.Value, bool, error) {
		return src.Pull(t)
	}, nil, src)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if closed != 1 {
		t.Fatalf("closeFn ran %d times", closed)
	}
	if src.State() != Closed || w.State() != Closed {
		t.Fatalf("states %v %v", src.State(), w.State())
	}
	if sc.Live() != 0 {
		t.Fatalf("live=%d", sc.Live())
	}
}

func TestScopeTeardownNeverConsumed(t *testing.T) {
	sc, _ := testScope(t)
	closed := 0
	var order []int64
	mk := func() *Stream {
		id := sc.Reserve()
		st, err := NewWithID(sc, id, func(_ *starlark.Thread) (starlark.Value, bool, error) {
			return starlark.None, false, nil
		}, func() error {
			closed++
			order = append(order, id)
			return nil
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	a, b, c := mk(), mk(), mk()
	if sc.Live() != 3 {
		t.Fatalf("live=%d", sc.Live())
	}
	if err := sc.Close(); err != nil {
		t.Fatal(err)
	}
	if closed != 3 {
		t.Fatalf("closed=%d", closed)
	}
	// reverse registration order
	want := []int64{c.ID(), b.ID(), a.ID()}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("teardown order %v, want %v", order, want)
		}
	}
	if sc.Live() != 0 || sc.Active() {
		t.Fatalf("scope state live=%d active=%v", sc.Live(), sc.Active())
	}
}

func TestBreakReleasesCursorOnly(t *testing.T) {
	sc, thread := testScope(t)
	closed := false
	st, err := New(sc, func(_ *starlark.Thread) (starlark.Value, bool, error) {
		return starlark.MakeInt(1), true, nil // never-ending
	}, func() error { closed = true; return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	it := st.Iterate()
	var v starlark.Value
	if !it.Next(&v) {
		t.Fatal("first item missing")
	}
	it.Done() // break releases the cursor; the stream stays open
	if closed || st.State() != Open {
		t.Fatalf("break closed the stream: closed=%v state=%v", closed, st.State())
	}
	// the same cursor can resume via .next (no replay, no concurrent iter)
	if _, ok, err := st.Pull(thread); err != nil || !ok {
		t.Fatalf("resume after break: %v %v", ok, err)
	}
	// explicit close is the user's early-stop control
	if err := st.Close(); err != nil || !closed {
		t.Fatalf("explicit close: %v closed=%v", err, closed)
	}
}

func TestNestedIteratorRejected(t *testing.T) {
	sc, _ := testScope(t)
	st := listStream(t, sc, starlark.MakeInt(1), starlark.MakeInt(2))
	it1 := st.Iterate()
	var v starlark.Value
	if !it1.Next(&v) {
		t.Fatal("first pull failed")
	}
	it2 := st.Iterate()
	var w starlark.Value
	if it2.Next(&w) {
		t.Fatal("nested iterator produced an item")
	}
	it2.Done()
	it1.Done()
	if sc.Err() == nil {
		t.Fatal("nested iteration did not fault the scope")
	}
}

func TestDirectNextStealingRejected(t *testing.T) {
	sc, thread := testScope(t)
	st := listStream(t, sc, starlark.MakeInt(1))
	it := st.Iterate()
	if _, _, err := st.Pull(thread); err == nil {
		t.Fatal("direct .next() during active iterator succeeded")
	}
	it.Done()
	if sc.Err() == nil {
		t.Fatal("stealing attempt not latched")
	}
}

func TestRecursivePullRejected(t *testing.T) {
	sc, thread := testScope(t)
	var st *Stream
	fn := starlark.NewBuiltin("f", func(t *starlark.Thread, b *starlark.Builtin, a starlark.Tuple, k []starlark.Tuple) (starlark.Value, error) {
		_, _, err := st.Pull(t)
		if err == nil {
			return nil, errors.New("recursive pull allowed")
		}
		return starlark.Tuple{starlark.False, starlark.None}, nil
	})
	var err error
	st, err = NewCallback(sc, fn)
	if err != nil {
		t.Fatal(err)
	}
	// The recursive pull poisons the stream: the outer pull sees the
	// terminal failure even though the callback swallowed its own error.
	if _, ok, err := st.Pull(thread); err == nil || ok {
		t.Fatalf("pull: ok=%v err=%v", ok, err)
	}
	if sc.Err() == nil {
		t.Fatal("recursive pull not latched")
	}
}

func TestWrongThreadRejected(t *testing.T) {
	sc, _ := testScope(t)
	st := listStream(t, sc, starlark.MakeInt(1))
	other := &starlark.Thread{Name: "other"}
	if _, _, err := st.Pull(other); !errors.Is(err, ErrOutOfScope) {
		t.Fatalf("wrong thread: %v", err)
	}
}

func TestStaleScopeFaultsCurrent(t *testing.T) {
	sc1, _ := testScope(t)
	st := listStream(t, sc1, starlark.MakeInt(1))
	_ = sc1.Close() // end of exec 1

	// exec 2: new scope; the stale handle must fault the CURRENT scope.
	sc2, t2 := testScope(t)
	sc1.resolve = func() *Scope { return sc2 }
	it := st.Iterate()
	var v starlark.Value
	if it.Next(&v) {
		t.Fatal("stale stream produced an item")
	}
	it.Done()
	if sc2.Err() == nil || !errors.Is(sc2.Err(), ErrOutOfScope) {
		t.Fatalf("current scope not faulted: %v", sc2.Err())
	}
	_ = t2
	// direct .next() on stale stream faults via the calling thread's scope
	sc3, t3 := testScope(t)
	sc1.resolve = func() *Scope { return sc3 }
	if _, _, err := st.Pull(t3); !errors.Is(err, ErrOutOfScope) {
		t.Fatalf("stale .next: %v", err)
	}
	if sc3.Err() == nil {
		t.Fatal("stale .next did not fault current scope")
	}
}

func TestStickyErrorAndCancel(t *testing.T) {
	sc, thread := testScope(t)
	boom := errors.New("read exploded")
	st, err := New(sc, func(_ *starlark.Thread) (starlark.Value, bool, error) {
		return nil, false, boom
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	it := st.Iterate()
	var v starlark.Value
	if it.Next(&v) {
		t.Fatal("failed stream yielded")
	}
	it.Done()
	if !errors.Is(sc.Err(), boom) {
		t.Fatalf("sticky %v", sc.Err())
	}
	// same-scope re-read returns the original error, not exhausted
	if _, _, err := st.Pull(thread); !errors.Is(err, boom) {
		t.Fatalf("failed re-read: %v", err)
	}
}

func TestLiveCap(t *testing.T) {
	sc, _ := testScope(t)
	streams := make([]*Stream, 0, MaxLive)
	pull := func(_ *starlark.Thread) (starlark.Value, bool, error) { return starlark.None, false, nil }
	for i := 0; i < MaxLive; i++ {
		st, err := New(sc, pull, nil, nil)
		if err != nil {
			t.Fatalf("stream %d: %v", i, err)
		}
		streams = append(streams, st)
	}
	if _, err := New(sc, pull, nil, nil); err == nil {
		t.Fatal("cap not enforced")
	}
	// closing one frees a registry slot
	if err := streams[0].Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := New(sc, pull, nil, nil); err != nil {
		t.Fatalf("slot not released: %v", err)
	}
}

func TestOwnedSourceIterationRejected(t *testing.T) {
	sc, _ := testScope(t)
	src := listStream(t, sc, starlark.MakeInt(1))
	_, err := New(sc, func(t *starlark.Thread) (starlark.Value, bool, error) {
		return src.Pull(t)
	}, nil, src)
	if err != nil {
		t.Fatal(err)
	}
	it := src.Iterate()
	var v starlark.Value
	if it.Next(&v) {
		t.Fatal("owned source iterated")
	}
	it.Done()
	if sc.Err() == nil {
		t.Fatal("owned-source iteration not latched")
	}
}

func TestSourcePullFromCallbackAllowed(t *testing.T) {
	sc, thread := testScope(t)
	src := listStream(t, sc, starlark.MakeInt(7), starlark.MakeInt(8))
	fn := starlark.NewBuiltin("f", func(t *starlark.Thread, b *starlark.Builtin, a starlark.Tuple, k []starlark.Tuple) (starlark.Value, error) {
		v, ok, err := src.Pull(t)
		if err != nil {
			return nil, err
		}
		return starlark.Tuple{starlark.Bool(ok), v}, nil
	})
	w, err := NewCallback(sc, fn)
	if err != nil {
		t.Fatal(err)
	}
	v, ok, err := w.Pull(thread)
	if err != nil || !ok || v.String() != "7" {
		t.Fatalf("derived pull: %v %v %v", v, ok, err)
	}
	w.Close()
	// callback captures do NOT transfer lifetime: the user-controlled
	// upstream stays open until the caller closes it explicitly.
	if src.State() == Closed {
		t.Fatal("upstream closed by wrapper close")
	}
	if err := src.Close(); err != nil {
		t.Fatal(err)
	}
	if src.State() != Closed {
		t.Fatalf("source state %v", src.State())
	}
}

func TestAttrs(t *testing.T) {
	sc, thread := testScope(t)
	st := listStream(t, sc, starlark.MakeInt(3))
	next, err := st.Attr("next")
	if err != nil || next == nil {
		t.Fatal("next attr missing")
	}
	v, err := starlark.Call(thread, next, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	tup := v.(starlark.Tuple)
	if tup[0] != starlark.True || tup[1].String() != "3" {
		t.Fatalf("next tuple %v", tup)
	}
	cl, _ := st.Attr("close")
	if _, err := starlark.Call(thread, cl, nil, nil); err != nil {
		t.Fatal(err)
	}
	if st.State() != Closed {
		t.Fatalf("state %v", st.State())
	}
	if st.String() != fmt.Sprintf("<stream #%d>", st.ID()) {
		t.Fatalf("repr %s", st.String())
	}
	if st.Truth() != starlark.True {
		t.Fatal("truth")
	}
	if _, err := st.Hash(); err == nil {
		t.Fatal("hash succeeded")
	}
	st.Freeze() // no-op
	if a, _ := st.Attr("bogus"); a != nil {
		t.Fatal("unexpected attr")
	}
}

func TestCreateInactiveScope(t *testing.T) {
	thread := &starlark.Thread{Name: "test"}
	sc := NewScope(context.Background(), thread, nil) // never Activate
	_, err := New(sc, func(_ *starlark.Thread) (starlark.Value, bool, error) {
		return starlark.None, false, nil
	}, nil, nil)
	if !errors.Is(err, ErrOutOfScope) {
		t.Fatalf("inactive scope create: %v", err)
	}
}

func TestSelfCloseCallback(t *testing.T) {
	sc, thread := testScope(t)
	var st *Stream
	var err error
	st, err = New(sc, func(_ *starlark.Thread) (starlark.Value, bool, error) {
		// closing self mid-pull must not panic or yield the value
		_ = st.Close()
		return starlark.MakeInt(9), true, nil
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	v, ok, err := st.Pull(thread)
	if err != nil || ok || v != starlark.None {
		t.Fatalf("self-close pull: %v %v %v", v, ok, err)
	}
	if st.State() != Closed {
		t.Fatalf("state %v", st.State())
	}
}

func TestEOFCloseErrorLatched(t *testing.T) {
	sc, thread := testScope(t)
	boom := errors.New("close exploded")
	st, err := New(sc, func(_ *starlark.Thread) (starlark.Value, bool, error) {
		return starlark.None, false, nil
	}, func() error { return boom }, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = st.Pull(thread)
	if !errors.Is(err, boom) {
		t.Fatalf("EOF close error: %v", err)
	}
	if !errors.Is(sc.Err(), boom) {
		t.Fatalf("sticky %v", sc.Err())
	}
}

func TestFailPrecedenceReadVsClose(t *testing.T) {
	sc, thread := testScope(t)
	readErr := errors.New("read exploded")
	st, err := New(sc, func(_ *starlark.Thread) (starlark.Value, bool, error) {
		return nil, false, readErr
	}, func() error { return errors.New("close exploded") }, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = st.Pull(thread)
	if !errors.Is(err, readErr) || !errors.Is(sc.Err(), readErr) {
		t.Fatalf("primary error lost: %v / %v", err, sc.Err())
	}
}

func TestScopeCloseErrorLatched(t *testing.T) {
	sc, _ := testScope(t)
	boom := errors.New("teardown exploded")
	if _, err := New(sc, func(_ *starlark.Thread) (starlark.Value, bool, error) {
		return starlark.None, false, nil
	}, func() error { return boom }, nil); err != nil {
		t.Fatal(err)
	}
	if err := sc.Close(); !errors.Is(err, boom) {
		t.Fatalf("scope close: %v", err)
	}
	if !errors.Is(sc.Err(), boom) {
		t.Fatalf("sticky %v", sc.Err())
	}
}

func TestTermMetaSnapshotAfterClose(t *testing.T) {
	sc, _ := testScope(t)
	called := 0
	st, err := New(sc, func(_ *starlark.Thread) (starlark.Value, bool, error) {
		return starlark.None, false, nil
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	st.SetTerminalMeta(func() map[string]any {
		called++
		return map[string]any{"raw_bytes": int64(5)}
	})
	// live counters while open
	if m := st.TerminalMeta(); m == nil || m["raw_bytes"] != int64(5) {
		t.Fatalf("live meta %v", m)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	// after close the getter is released; the snapshot stays readable
	if m := st.TerminalMeta(); m == nil || m["raw_bytes"] != int64(5) {
		t.Fatalf("snapshot %v", m)
	}
	if called != 2 { // one live call + one snapshot at cleanup; never again
		t.Fatalf("getter called %d times", called)
	}
}

// A reserved correlation id (ext open) is lower than the source's
// registration id, yet the wrapper registers LAST — teardown must
// close the wrapper before the source, not rely on id order.
func TestReservedIDTeardownOrder(t *testing.T) {
	sc, _ := testScope(t)
	reserved := sc.Reserve() // id 1, unregistered
	var order []string
	src, err := New(sc, func(_ *starlark.Thread) (starlark.Value, bool, error) {
		return starlark.None, false, nil
	}, func() error { order = append(order, "source"); return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if src.ID() == reserved {
		t.Fatal("source got reserved id")
	}
	if _, err := NewWithID(sc, reserved, func(t *starlark.Thread) (starlark.Value, bool, error) {
		return src.Pull(t)
	}, func() error { order = append(order, "wrapper"); return nil }, src); err != nil {
		t.Fatal(err)
	}
	if err := sc.Close(); err != nil {
		t.Fatal(err)
	}
	if len(order) != 2 || order[0] != "wrapper" || order[1] != "source" {
		t.Fatalf("teardown order %v", order)
	}
}

// A failing native close on explicit .close must latch the scope error.
func TestExplicitCloseErrorLatched(t *testing.T) {
	sc, _ := testScope(t)
	boom := errors.New("close exploded")
	st, err := New(sc, func(_ *starlark.Thread) (starlark.Value, bool, error) {
		return starlark.MakeInt(1), true, nil
	}, func() error { return boom }, nil)
	if err != nil {
		t.Fatal(err)
	}
	it := st.Iterate()
	var v starlark.Value
	it.Next(&v)
	it.Done() // break: releases cursor only, no close
	if err := st.Close(); !errors.Is(err, boom) {
		t.Fatalf("explicit close: %v", err)
	}
	if !errors.Is(sc.Err(), boom) {
		t.Fatalf("close error not latched: %v", sc.Err())
	}
}

// pullOnce checks owner ctx before the callback; a queued item is
// never delivered after cancellation.
func TestPullChecksContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	thread := &starlark.Thread{Name: "x"}
	sc := NewScope(ctx, thread, nil)
	sc.Activate()
	thread.SetLocal(ThreadKey, sc)
	calls := 0
	st, err := New(sc, func(_ *starlark.Thread) (starlark.Value, bool, error) {
		calls++
		return starlark.MakeInt(1), true, nil
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, _, err := st.Pull(thread); err == nil {
		t.Fatal("pull succeeded after cancellation")
	}
	if calls != 0 {
		t.Fatal("callback ran after cancellation")
	}
}
