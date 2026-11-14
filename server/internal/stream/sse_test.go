package stream

import (
	"errors"
	"strings"
	"testing"

	"go.starlark.net/starlark"
)

// byteSource streams each chunk lazily.
func byteSource(t *testing.T, sc *Scope, chunks ...[]byte) *Stream {
	t.Helper()
	i := 0
	st, err := New(sc, func(_ *starlark.Thread) (starlark.Value, bool, error) {
		if i >= len(chunks) {
			return starlark.None, false, nil
		}
		c := chunks[i]
		i++
		return starlark.Bytes(c), true, nil
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// splitEvery re-chunks data at every byte boundary and asserts the
// parsed events are identical, catching fragmentation bugs.
func splitEvery(t *testing.T, payload []byte) [][]starlark.Value {
	t.Helper()
	var runs [][]starlark.Value
	for split := 1; split < len(payload); split++ {
		sc, _ := testScope(t)
		src := byteSource(t, sc, payload[:split], payload[split:])
		sse, err := NewSSE(sc, src)
		if err != nil {
			t.Fatal(err)
		}
		evs := collectEvents(t, sse)
		runs = append(runs, evs)
	}
	return runs
}

func collectEvents(t *testing.T, st *Stream) []starlark.Value {
	t.Helper()
	var out []starlark.Value
	it := st.Iterate()
	defer it.Done()
	var v starlark.Value
	for it.Next(&v) {
		out = append(out, v)
	}
	return out
}

func eventText(t *testing.T, v starlark.Value) (string, string, string) {
	t.Helper()
	d, ok := v.(*starlark.Dict)
	if !ok {
		t.Fatalf("event is %T not dict", v)
	}
	get := func(k string) string {
		v, found, err := d.Get(starlark.String(k))
		if err != nil || !found {
			t.Fatalf("missing key %s", k)
		}
		s, _ := v.(starlark.String)
		return string(s)
	}
	return get("event"), get("data"), get("id")
}

func TestSSEBasic(t *testing.T) {
	sc, _ := testScope(t)
	src := byteSource(t, sc, []byte("event: hi\ndata: one\ndata: two\nid: 9\n\ndata: x\n\n"))
	sse, err := NewSSE(sc, src)
	if err != nil {
		t.Fatal(err)
	}
	evs := collectEvents(t, sse)
	if len(evs) != 2 {
		t.Fatalf("events %d", len(evs))
	}
	ev, data, id := eventText(t, evs[0])
	if ev != "hi" || data != "one\ntwo" || id != "9" {
		t.Fatalf("event1 %q %q %q", ev, data, id)
	}
	ev, data, id = eventText(t, evs[1])
	if ev != "message" || data != "x" || id != "9" { // id persists
		t.Fatalf("event2 %q %q %q", ev, data, id)
	}
}

func TestSSEFragmentationEveryByte(t *testing.T) {
	payload := []byte("event: e\ndata: héllo wörld\r\ndata: x\r\n\r\ndata: y\r\r\ndata:z\n\n")
	base := splitEvery(t, payload)
	if len(base) == 0 {
		t.Fatal("no runs")
	}
	for i, run := range base {
		if len(run) != 3 {
			t.Fatalf("split %d: %d events", i, len(run))
		}
		_, d0, _ := eventText(t, run[0])
		if d0 != "héllo wörld\nx" {
			t.Fatalf("split %d data %q", i, d0)
		}
		_, d1, _ := eventText(t, run[1])
		if d1 != "y" {
			t.Fatalf("split %d data %q", i, d1)
		}
	}
}

func TestSSECommentsRetryUnknownIgnored(t *testing.T) {
	sc, _ := testScope(t)
	src := byteSource(t, sc, []byte(": comment\nretry: 100\nbogus: z\ndata: a\n\n"))
	sse, err := NewSSE(sc, src)
	if err != nil {
		t.Fatal(err)
	}
	evs := collectEvents(t, sse)
	if len(evs) != 1 {
		t.Fatalf("events %d", len(evs))
	}
	_, d, _ := eventText(t, evs[0])
	if d != "a" {
		t.Fatalf("data %q", d)
	}
}

func TestSSEBOM(t *testing.T) {
	sc, _ := testScope(t)
	src := byteSource(t, sc, []byte{0xEF, 0xBB, 0xBF, 'd'}, []byte("ata: a\n\n"))
	sse, err := NewSSE(sc, src)
	if err != nil {
		t.Fatal(err)
	}
	evs := collectEvents(t, sse)
	if len(evs) != 1 {
		t.Fatalf("events %d", len(evs))
	}
	_, d, _ := eventText(t, evs[0])
	if d != "a" {
		t.Fatalf("data %q", d)
	}
}

func TestSSEIdNULIgnored(t *testing.T) {
	sc, _ := testScope(t)
	src := byteSource(t, sc, []byte("id: 1\ndata: a\n\nid: b\x00ad\ndata: c\n\n"))
	sse, err := NewSSE(sc, src)
	if err != nil {
		t.Fatal(err)
	}
	evs := collectEvents(t, sse)
	if len(evs) != 2 {
		t.Fatalf("events %d", len(evs))
	}
	_, _, id := eventText(t, evs[1])
	if id != "1" {
		t.Fatalf("id %q", id)
	}
}

func TestSSEIncompleteDiscardedAtEOF(t *testing.T) {
	sc, _ := testScope(t)
	src := byteSource(t, sc, []byte("data: complete\n\ndata: partial")) // no trailing blank line
	sse, err := NewSSE(sc, src)
	if err != nil {
		t.Fatal(err)
	}
	evs := collectEvents(t, sse)
	if len(evs) != 1 {
		t.Fatalf("events %d", len(evs))
	}
	_, d, _ := eventText(t, evs[0])
	if d != "complete" {
		t.Fatalf("data %q", d)
	}
}

func TestSSEInvalidUTF8(t *testing.T) {
	sc, _ := testScope(t)
	src := byteSource(t, sc, []byte{'d', 'a', 't', 'a', ':', ' ', 0xFF, 0xFE, '\n', '\n'})
	sse, err := NewSSE(sc, src)
	if err != nil {
		t.Fatal(err)
	}
	it := sse.Iterate()
	var v starlark.Value
	if it.Next(&v) {
		t.Fatal("invalid UTF-8 yielded an event")
	}
	it.Done()
	if sc.Err() == nil || !strings.Contains(sc.Err().Error(), "UTF-8") {
		t.Fatalf("expected UTF-8 error, got %v", sc.Err())
	}
}

func TestSSEChunkCap(t *testing.T) {
	sc, _ := testScope(t)
	src := byteSource(t, sc, make([]byte, maxSSEChunk+1))
	sse, err := NewSSE(sc, src)
	if err != nil {
		t.Fatal(err)
	}
	it := sse.Iterate()
	var v starlark.Value
	it.Next(&v)
	it.Done()
	if sc.Err() == nil || !strings.Contains(sc.Err().Error(), "chunk") {
		t.Fatalf("expected chunk error, got %v", sc.Err())
	}
}

func TestSSEEventCap(t *testing.T) {
	sc, _ := testScope(t)
	// one giant line exceeding maxSSEEvent, delivered in bounded chunks
	line := strings.Repeat("a", maxSSEEvent+1)
	src := byteSource(t, sc, []byte("data: "+line))
	sse, err := NewSSE(sc, src)
	if err != nil {
		t.Fatal(err)
	}
	it := sse.Iterate()
	var v starlark.Value
	it.Next(&v)
	it.Done()
	if sc.Err() == nil || !strings.Contains(sc.Err().Error(), "byte limit") {
		t.Fatalf("expected event cap error, got %v", sc.Err())
	}
}

func TestSSEEventOnlyNoDataNotDispatched(t *testing.T) {
	sc, _ := testScope(t)
	src := byteSource(t, sc, []byte("event: e\n\ndata: x\n\n"))
	sse, err := NewSSE(sc, src)
	if err != nil {
		t.Fatal(err)
	}
	evs := collectEvents(t, sse)
	if len(evs) != 1 {
		t.Fatalf("events %d", len(evs))
	}
	_, d, _ := eventText(t, evs[0])
	if d != "x" {
		t.Fatalf("data %q", d)
	}
}

func TestSSELazyPull(t *testing.T) {
	sc, _ := testScope(t)
	pulls := 0
	src, err := New(sc, func(_ *starlark.Thread) (starlark.Value, bool, error) {
		pulls++
		return starlark.Bytes("data: x\n\ndata: y\n\n"), true, nil
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	sse, err := NewSSE(sc, src)
	if err != nil {
		t.Fatal(err)
	}
	it := sse.Iterate()
	var v starlark.Value
	if !it.Next(&v) {
		t.Fatal("no first event")
	}
	if pulls != 1 {
		t.Fatalf("pulls %d", pulls)
	}
	it.Done()
}

func TestContextResolverDefault(t *testing.T) {
	sc, _ := testScope(t)
	if sc.Current() != sc {
		t.Fatal("default resolver should return self while active")
	}
	_ = sc.Close()
	if sc.Current() != nil {
		t.Fatal("closed scope should resolve nil")
	}
}

func TestSSEEventResetOnBlank(t *testing.T) {
	sc, _ := testScope(t)
	src := byteSource(t, sc, []byte("event: e\n\ndata: x\n\n"))
	sse, err := NewSSE(sc, src)
	if err != nil {
		t.Fatal(err)
	}
	evs := collectEvents(t, sse)
	if len(evs) != 1 {
		t.Fatalf("events %d", len(evs))
	}
	ev, _, _ := eventText(t, evs[0])
	if ev != "message" {
		t.Fatalf("event type leaked: %q", ev)
	}
}

func TestSSETrailingCRDispatches(t *testing.T) {
	sc, _ := testScope(t)
	src := byteSource(t, sc, []byte("data: x\r\r"))
	sse, err := NewSSE(sc, src)
	if err != nil {
		t.Fatal(err)
	}
	evs := collectEvents(t, sse)
	if len(evs) != 1 {
		t.Fatalf("events %d", len(evs))
	}
	_, d, _ := eventText(t, evs[0])
	if d != "x" {
		t.Fatalf("data %q", d)
	}
}

func TestSSEEventCapReal(t *testing.T) {
	sc, _ := testScope(t)
	// oversized line delivered through bounded chunks: exercises the
	// event cap, not the chunk cap
	var chunks [][]byte
	line := []byte("data: " + strings.Repeat("a", maxSSEEvent))
	for len(line) > 0 {
		n := min(len(line), maxSSEChunk)
		chunks = append(chunks, line[:n])
		line = line[n:]
	}
	src := byteSource(t, sc, chunks...)
	sse, err := NewSSE(sc, src)
	if err != nil {
		t.Fatal(err)
	}
	it := sse.Iterate()
	var v starlark.Value
	it.Next(&v)
	it.Done()
	if sc.Err() == nil || !strings.Contains(sc.Err().Error(), "byte limit") {
		t.Fatalf("expected cap error, got %v", sc.Err())
	}
}

// A CR-terminated frame dispatches immediately — no waiting for the
// next byte: the source's second pull deliberately fails and the
// first SSE pull must still return the event.
func TestSSECRImmediateNoSecondRead(t *testing.T) {
	sc, thread := testScope(t)
	pulls := 0
	src, err := New(sc, func(_ *starlark.Thread) (starlark.Value, bool, error) {
		pulls++
		if pulls > 1 {
			return nil, false, errors.New("second read must not happen")
		}
		return starlark.Bytes("data: x\r\r"), true, nil
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	sse, err := NewSSE(sc, src)
	if err != nil {
		t.Fatal(err)
	}
	v, ok, err := sse.Pull(thread)
	if err != nil || !ok {
		t.Fatalf("pull: %v %v", ok, err)
	}
	if pulls != 1 {
		t.Fatalf("source read %d times", pulls)
	}
	_, d, _ := eventText(t, v)
	if d != "x" {
		t.Fatalf("data %q", d)
	}
}
