package net

import (
	"calcside/internal/hostmatch"
	"calcside/internal/secrets"
	"calcside/internal/types"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sync"
	"sync/atomic"

	"go.starlark.net/starlark"

	"calcside/internal/capability"
	"calcside/internal/stream"
)

// netFixture builds a client module allowing the given host:port
// (private IPs permitted for loopback test servers).
func netFixture(t *testing.T, hooks []capability.Hook, obs capability.Observer, allowHost string) (*capability.Gate, starlark.Value) {
	t.Helper()
	c := &client{cfg: Config{Methods: []types.HTTPMethod{types.MethodGet, types.MethodPost}, MaxResponseBytes: defaultMaxResponse, TimeoutMs: defaultTimeoutMs}, allowPrivate: true}
	if allowHost != "" {
		rules, err := hostmatch.ParseAll([]string{allowHost})
		if err != nil {
			t.Fatal(err)
		}
		c.rules = rules
	}
	c.hc = c.newHTTPClient()
	gate := capability.NewGate(capability.GateOwner{InstanceID: "ins"}, hooks, obs)
	t.Cleanup(func() { closer{c}.Close() })
	return gate, c.module(gate)
}

func scopeThread(t *testing.T) (*stream.Scope, *starlark.Thread) {
	t.Helper()
	thread := &starlark.Thread{Name: "exec"}
	sc := stream.NewScope(context.Background(), thread, nil)
	sc.Activate()
	thread.SetLocal(stream.ThreadKey, sc)
	thread.SetLocal(capability.ContextKey, sc.Context())
	return sc, thread
}

func callStream(t *testing.T, gate *capability.Gate, mod starlark.Value, sc *stream.Scope, thread *starlark.Thread, url string) (*starlark.Dict, error) {
	t.Helper()
	op, err := mod.(starlark.HasAttrs).Attr("stream")
	if err != nil {
		t.Fatal(err)
	}
	gate.Arm(capability.ExecContext{ExecID: "e"})
	v, err := starlark.Call(thread, op, starlark.Tuple{starlark.String("POST"), starlark.String(url)}, nil)
	if err != nil {
		return nil, err
	}
	return v.(*starlark.Dict), nil
}

func bodyStream(t *testing.T, d *starlark.Dict) *stream.Stream {
	t.Helper()
	v, found, err := d.Get(starlark.String("body"))
	if !found || err != nil {
		t.Fatal("no body")
	}
	return v.(*stream.Stream)
}

func TestNetStreamLaziness(t *testing.T) {
	sent := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		fmt.Fprint(w, "chunk-one ")
		w.(http.Flusher).Flush()
		sent <- struct{}{} // first chunk delivered before completion
		time.Sleep(50 * time.Millisecond)
		fmt.Fprint(w, "chunk-two")
	}))
	defer srv.Close()
	gate, mod := netFixture(t, nil, nil, hostPort2(srv))
	sc, thread := scopeThread(t)
	d, err := callStream(t, gate, mod, sc, thread, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	st, _, _ := d.Get(starlark.String("status"))
	if iv, _ := st.(starlark.Int); iv != starlark.MakeInt(200) {
		t.Fatalf("status %v", st)
	}
	body := bodyStream(t, d)
	v, ok, err := body.Pull(thread)
	if err != nil || !ok {
		t.Fatalf("pull %v %v", ok, err)
	}
	select {
	case <-sent:
	default:
		t.Fatal("server did not emit first chunk yet")
	}
	if got := string(v.(starlark.Bytes)); got != "chunk-one " {
		t.Fatalf("first chunk %q", got)
	}
	// drain + close
	for {
		_, ok, err := body.Pull(thread)
		if err != nil || !ok {
			break
		}
	}
	if err := body.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNetStreamNeverPulledCleanup(t *testing.T) {
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(done)
	}))
	defer srv.Close()
	gate, mod := netFixture(t, nil, nil, hostPort2(srv))
	sc, thread := scopeThread(t)
	_, err := callStream(t, gate, mod, sc, thread, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	// scope teardown closes the never-read body
	if err := sc.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("body never closed; server still blocked")
	}
}

type denyPullBefore struct{}

func (denyPullBefore) Name() string { return "d" }
func (denyPullBefore) Before(_ context.Context, c *capability.Call) error {
	if c.Args[capability.StreamActionArg] == "pull" {
		return fmt.Errorf("pull denied")
	}
	return nil
}
func (denyPullBefore) After(context.Context, *capability.Call, *capability.Result) error { return nil }

func TestNetStreamPullDenyClosesBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "data")
	}))
	defer srv.Close()
	gate, mod := netFixture(t, []capability.Hook{denyPullBefore{}}, nil, hostPort2(srv))
	sc, thread := scopeThread(t)
	d, err := callStream(t, gate, mod, sc, thread, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	body := bodyStream(t, d)
	if _, _, err := body.Pull(thread); err == nil {
		t.Fatal("pull not denied")
	}
	if body.State() != stream.Failed {
		t.Fatalf("state %v", body.State())
	}
}

func TestNetStreamCapAndAudit(t *testing.T) {
	obs := &capObs{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "0123456789")
	}))
	defer srv.Close()
	gate, mod := netFixture(t, nil, obs, hostPort2(srv))
	sc, thread := scopeThread(t)
	d, err := callStream(t, gate, mod, sc, thread, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	body := bodyStream(t, d)
	for {
		_, ok, err := body.Pull(thread)
		if err != nil || !ok {
			if err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	if err := sc.Close(); err != nil {
		t.Fatal(err)
	}
	var open, close *capability.Record
	for i := range obs.recs {
		switch obs.recs[i].Call.Args[capability.StreamActionArg] {
		case "open":
			open = &obs.recs[i]
		case "close":
			close = &obs.recs[i]
		}
	}
	if open == nil || close == nil {
		t.Fatal("missing open/close records")
	}
	if open.Call.Args[capability.StreamIDArg] != close.Call.Args[capability.StreamIDArg] {
		t.Fatal("open/close stream ids differ")
	}
	if close.Call.Args["raw_bytes"] != int64(10) {
		t.Fatalf("raw_bytes %v", close.Call.Args["raw_bytes"])
	}
	if close.Call.Args["stream_status"] != "complete" {
		t.Fatalf("status %v", close.Call.Args["stream_status"])
	}
}

type capObs struct{ recs []capability.Record }

func (o *capObs) Observe(r capability.Record) { o.recs = append(o.recs, r) }

func TestNetStreamResponseCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "0123456789")
	}))
	defer srv.Close()
	c := &client{cfg: Config{Methods: []types.HTTPMethod{types.MethodPost}, MaxResponseBytes: 5, TimeoutMs: defaultTimeoutMs}, allowPrivate: true}
	c.hc = c.newHTTPClient()
	gate := capability.NewGate(capability.GateOwner{}, nil, nil)
	v := c.module(gate)
	sc, thread := scopeThread(t)
	d, err := callStream(t, gate, v, sc, thread, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	body := bodyStream(t, d)
	got := 0
	var perr error
	for {
		v, ok, err := body.Pull(thread)
		if err != nil {
			perr = err
			break
		}
		if !ok {
			break
		}
		got += len(v.(starlark.Bytes))
	}
	if perr == nil {
		t.Fatal("cap not enforced; read", got)
	}
}

func hostPort2(srv *httptest.Server) string {
	u := srv.URL[len("http://"):]
	return u
}

// A full live cap rejects the stream before any HTTP request is sent.
func TestNetStreamMaxLiveNoRequest(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		fmt.Fprint(w, "data")
	}))
	defer srv.Close()
	gate, mod := netFixture(t, nil, nil, hostPort2(srv))
	sc, thread := scopeThread(t)
	// fill the live registry with placeholder streams
	for i := 0; i < stream.MaxLive; i++ {
		if _, err := stream.New(sc, func(_ *starlark.Thread) (starlark.Value, bool, error) {
			return starlark.None, false, nil
		}, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := callStream(t, gate, mod, sc, thread, srv.URL); err == nil {
		t.Fatal("stream created over cap")
	}
	if hits.Load() != 0 {
		t.Fatalf("network request sent despite full scope (%d hits)", hits.Load())
	}
}

// --- security regressions (finding 5) ---

func cancelScopeThread(t *testing.T) (*stream.Scope, *starlark.Thread, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	thread := &starlark.Thread{Name: "exec"}
	sc := stream.NewScope(ctx, thread, nil)
	sc.Activate()
	thread.SetLocal(stream.ThreadKey, sc)
	thread.SetLocal(capability.ContextKey, sc.Context())
	return sc, thread, cancel
}

// Cancellation while the body Read is actively blocked must fail the
// pull and close the response.
func TestNetStreamCancelDuringBlockedRead(t *testing.T) {
	readEntered := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	blocking := &blockReadCloser{ctx: ctx.Done(), entered: readEntered}
	secs := secrets.NewSet()
	c := &client{cfg: Config{MaxResponseBytes: defaultMaxResponse}, secrets: secs}
	thread := &starlark.Thread{Name: "exec"}
	sc := stream.NewScope(ctx, thread, nil)
	sc.Activate()
	thread.SetLocal(stream.ThreadKey, sc)
	src, r, err := c.newBodyStream(sc, ctx, func() {})
	if err != nil {
		t.Fatal(err)
	}
	r.SetBody(&http.Response{StatusCode: 200, Body: blocking, Header: http.Header{}})
	pullDone := make(chan error, 1)
	go func() {
		_, _, err := src.Pull(thread)
		pullDone <- err
	}()
	select {
	case <-readEntered: // the pull is blocked inside Body.Read now
	case <-time.After(3 * time.Second):
		t.Fatal("read never entered")
	}
	cancel()
	select {
	case err := <-pullDone:
		if err == nil {
			t.Fatal("blocked read survived cancellation")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("blocked read never unblocked")
	}
	if err := sc.Close(); err != nil {
		t.Fatal(err)
	}
}

// blockReadCloser blocks in Read until ctx is closed.
type blockReadCloser struct {
	ctx     <-chan struct{}
	entered chan struct{}
	once    sync.Once
}

func (b *blockReadCloser) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.entered) })
	<-b.ctx
	return 0, io.EOF
}
func (b *blockReadCloser) Close() error { return nil }

// The whole-request timeout covers a stalled body, not just the open.
func TestNetStreamWholeRequestTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "first")
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer srv.Close()
	c := &client{cfg: Config{Methods: []types.HTTPMethod{types.MethodPost},
		TimeoutMs: 200, MaxResponseBytes: defaultMaxResponse}, allowPrivate: true}
	c.hc = c.newHTTPClient()
	gate := capability.NewGate(capability.GateOwner{}, nil, nil)
	mod := c.module(gate)
	sc, thread := scopeThread(t)
	d, err := callStream(t, gate, mod, sc, thread, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	body := bodyStream(t, d)
	// first chunk may arrive; the stall must hit the request timeout
	var perr error
	for i := 0; i < 5; i++ {
		if _, ok, err := body.Pull(thread); err != nil {
			perr = err
			break
		} else if !ok {
			break
		}
	}
	if perr == nil {
		t.Fatal("stalled body did not time out")
	}
}

// With only one live slot left the placeholder allocates but the
// wrapper fails — no HTTP request is ever sent.
func TestNetStreamOneSlotLeftNoRequest(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		fmt.Fprint(w, "data")
	}))
	defer srv.Close()
	gate, mod := netFixture(t, nil, nil, hostPort2(srv))
	sc, thread := scopeThread(t)
	for i := 0; i < stream.MaxLive-1; i++ {
		if _, err := stream.New(sc, func(_ *starlark.Thread) (starlark.Value, bool, error) {
			return starlark.None, false, nil
		}, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := callStream(t, gate, mod, sc, thread, srv.URL); err == nil {
		t.Fatal("stream created over cap")
	}
	if hits.Load() != 0 {
		t.Fatalf("request sent despite full scope (%d)", hits.Load())
	}
	if sc.Live() != stream.MaxLive-1 {
		t.Fatalf("live %d", sc.Live())
	}
}

// Open-phase After denial: response must be released, nothing yielded.
type denyAfter struct{ on string }

func (d denyAfter) Name() string                                   { return "d" }
func (d denyAfter) Before(context.Context, *capability.Call) error { return nil }
func (d denyAfter) After(_ context.Context, c *capability.Call, r *capability.Result) error {
	if c.Args[capability.StreamActionArg] == d.on {
		return fmt.Errorf("denied")
	}
	return nil
}

func TestNetStreamOpenAfterDenyClosesBody(t *testing.T) {
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(done)
	}))
	defer srv.Close()
	gate, mod := netFixture(t, []capability.Hook{denyAfter{on: "open"}}, nil, hostPort2(srv))
	sc, thread := scopeThread(t)
	if _, err := callStream(t, gate, mod, sc, thread, srv.URL); err == nil {
		t.Fatal("open after-deny accepted")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("response not released after open denial")
	}
}

func TestNetStreamPullAfterDenyNoChunks(t *testing.T) {
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "data")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(done)
	}))
	defer srv.Close()
	gate, mod := netFixture(t, []capability.Hook{denyAfter{on: "pull"}}, nil, hostPort2(srv))
	sc, thread := scopeThread(t)
	d, err := callStream(t, gate, mod, sc, thread, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	body := bodyStream(t, d)
	yielded := 0
	for i := 0; i < 3; i++ {
		if _, ok, err := body.Pull(thread); err != nil {
			break
		} else if ok {
			yielded++
		} else {
			break
		}
	}
	if yielded != 0 {
		t.Fatalf("chunks exposed under after-deny: %d", yielded)
	}
	_ = sc.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("response not released after pull denial")
	}
}

// The Range header stays forbidden on the stream path.
func TestNetStreamRangeHeaderRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "data")
	}))
	defer srv.Close()
	gate, mod := netFixture(t, nil, nil, hostPort2(srv))
	sc, thread := scopeThread(t)
	_ = sc
	op, _ := mod.(starlark.HasAttrs).Attr("stream")
	hd := starlark.NewDict(1)
	_ = hd.SetKey(starlark.String("Range"), starlark.String("bytes=0-1"))
	gate.Arm(capability.ExecContext{ExecID: "e"})
	defer gate.Disarm()
	_, err := starlark.Call(thread, op, starlark.Tuple{starlark.String("POST"), starlark.String(srv.URL)},
		[]starlark.Tuple{{starlark.String("headers"), hd}})
	if err == nil {
		t.Fatal("Range header accepted")
	}
}

// Redirect to a host outside allow_hosts is refused (same rules as
// buffered request).
func TestNetStreamRedirectHostDenied(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "data")
	}))
	defer target.Close()
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/data", 302)
	}))
	defer redir.Close()
	gate, mod := netFixture(t, nil, nil, hostPort2(redir)) // only the redirect host allowed
	sc, thread := scopeThread(t)
	if _, err := callStream(t, gate, mod, sc, thread, redir.URL+"/go"); err == nil {
		t.Fatal("redirect to non-allowlisted host accepted")
	}
}

// Private-network policy still applies to the stream dial path.
func TestNetStreamPrivateNetworkDenied(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "data")
	}))
	defer srv.Close()
	c := &client{cfg: Config{Methods: []types.HTTPMethod{types.MethodPost},
		MaxResponseBytes: defaultMaxResponse, TimeoutMs: defaultTimeoutMs},
		allowPrivate: false}
	rules, _ := hostmatch.ParseAll([]string{hostPort2(srv)})
	c.rules = rules
	c.hc = c.newHTTPClient()
	gate := capability.NewGate(capability.GateOwner{}, nil, nil)
	mod := c.module(gate)
	sc, thread := scopeThread(t)
	if _, err := callStream(t, gate, mod, sc, thread, srv.URL); err == nil {
		t.Fatal("private-network dial accepted")
	}
}

// End-to-end redaction through bodyReader at every split boundary: the
// concatenated delivered output never contains the raw token.
func TestNetStreamRedactionEverySplit(t *testing.T) {
	secs := secrets.NewSet()
	secs.Add("K", []byte("tok-FAKE-9"), nil)
	payload := "pre tok-FAKE-9 post café "
	for split := 0; split <= len(payload); split++ {
		c := &client{cfg: Config{MaxResponseBytes: defaultMaxResponse}, secrets: secs}
		gate := capability.NewGate(capability.GateOwner{}, nil, nil)
		sc, thread := scopeThread(t)
		// chunker ReadCloser yields the payload in two pieces at `split`
		rc := &splitReader{parts: [][]byte{[]byte(payload[:split]), []byte(payload[split:])}}
		src, r, err := c.newBodyStream(sc, context.Background(), func() {})
		if err != nil {
			t.Fatal(err)
		}
		_ = gate
		r.SetBody(&http.Response{StatusCode: 200, Body: rc, Header: http.Header{}})
		st := src
		var out strings.Builder
		for {
			v, ok, err := st.Pull(thread)
			if err != nil {
				t.Fatalf("split %d: %v", split, err)
			}
			if !ok {
				break
			}
			out.WriteString(string(v.(starlark.Bytes)))
		}
		got := out.String()
		if strings.Contains(got, "tok-FAKE-9") {
			t.Fatalf("split %d leaked token: %q", split, got)
		}
		if !strings.Contains(got, "[REDACTED:K]") || !strings.Contains(got, "café") {
			t.Fatalf("split %d output %q", split, got)
		}
	}
}

type splitReader struct {
	parts [][]byte
	i     int
	pos   int
}

func (r *splitReader) Read(p []byte) (int, error) {
	if r.i >= len(r.parts) {
		return 0, io.EOF
	}
	rest := r.parts[r.i][r.pos:]
	n := copy(p, rest)
	r.pos += n
	if r.pos >= len(r.parts[r.i]) {
		r.i++
		r.pos = 0
	}
	return n, nil
}
func (r *splitReader) Close() error { return nil }

// Open body failure latched as primary: a close-hook denial afterwards
// cannot replace it (Stream.Fail runs before native cleanup).
func TestNetStreamOpenFailPrimaryOverCloseDeny(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "data")
	}))
	defer srv.Close()
	// allow_hosts only permits 127.0.0.1:1 — the request fails inside
	// open; the close hook denying afterwards must not mask it.
	gate, mod := netFixture(t, []capability.Hook{denyAfter{on: "close"}}, nil, "127.0.0.1:1")
	sc, thread := scopeThread(t)
	_, err := callStream(t, gate, mod, sc, thread, srv.URL)
	if err == nil {
		t.Fatal("disallowed host accepted")
	}
	if !strings.Contains(err.Error(), "allow_hosts") {
		t.Fatalf("primary open error lost: %v", err)
	}
	if sc.Err() == nil || !strings.Contains(sc.Err().Error(), "allow_hosts") {
		t.Fatalf("scope sticky is not the open error: %v", sc.Err())
	}
}

// Opening After denial: latched so a later close denial cannot win.
func TestNetStreamOpenDenyPrimaryOverCloseDeny(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "data")
	}))
	defer srv.Close()
	gate, mod := netFixture(t, []capability.Hook{denyAfterAll{opens: true, closes: true}}, nil, hostPort2(srv))
	sc, thread := scopeThread(t)
	_, err := callStream(t, gate, mod, sc, thread, srv.URL)
	if err == nil {
		t.Fatal("open after-deny accepted")
	}
	var denied *capability.DeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("expected open DeniedError, got %v", err)
	}
	if sc.Err() == nil || sc.Err() != err && !strings.Contains(sc.Err().Error(), "denied") {
		t.Fatalf("sticky %v", sc.Err())
	}
}

// denyAfterAll denies After on open and/or close phases.
type denyAfterAll struct{ opens, closes bool }

func (d denyAfterAll) Name() string                                   { return "deny" }
func (d denyAfterAll) Before(context.Context, *capability.Call) error { return nil }
func (d denyAfterAll) After(_ context.Context, c *capability.Call, r *capability.Result) error {
	a, _ := c.Args[capability.StreamActionArg].(string)
	if (a == "open" && d.opens) || (a == "close" && d.closes) {
		return fmt.Errorf("denied %s", a)
	}
	return nil
}

// Terminal-meta getter that calls back into stream accessors must not
// deadlock cleanup; the snapshot is still captured.
func TestTermMetaGetterCallsState(t *testing.T) {
	secs := secrets.NewSet()
	c := &client{cfg: Config{MaxResponseBytes: defaultMaxResponse}, secrets: secs}
	sc, _ := scopeThread(t)
	src, r, err := c.newBodyStream(sc, context.Background(), func() {})
	if err != nil {
		t.Fatal(err)
	}
	r.SetBody(&http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("x")), Header: http.Header{}})
	src.SetTerminalMeta(func() map[string]any {
		return map[string]any{"state": src.State().String()}
	})
	done := make(chan error, 1)
	go func() { done <- src.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cleanup deadlocked on getter calling State")
	}
	if m := src.TerminalMeta(); m == nil || m["state"] == nil {
		t.Fatalf("snapshot %v", m)
	}
}
