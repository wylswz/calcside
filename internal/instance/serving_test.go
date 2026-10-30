package instance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"calcside/internal/capability"
	capfs "calcside/internal/capability/fs"
	capio "calcside/internal/capability/io"
	"calcside/internal/runtime"
	"calcside/internal/types"
)

type spyCloser struct {
	mu     sync.Mutex
	closed int
}

func (c *spyCloser) Close() error { c.mu.Lock(); c.closed++; c.mu.Unlock(); return nil }
func (c *spyCloser) count() int   { c.mu.Lock(); defer c.mu.Unlock(); return c.closed }

// gateSpy is a capability that counts builds, records op context, and
// can block inside the gated op until released (obeying the call ctx).
type gateSpy struct {
	mu      sync.Mutex
	builds  int
	ops     []capability.Call
	closers []*spyCloser
	gates   []*capability.Gate
	block   chan struct{} // when non-nil, ops wait on it or ctx
	onNew   func()        // runs inside New after counters
	onOp    func()
}

func (s *gateSpy) Name() types.CapabilityName { return "probe" }
func (s *gateSpy) Ops() []capability.OpInfo {
	return []capability.OpInfo{{Name: "go"}}
}
func (s *gateSpy) ConfigFields() []capability.FieldDoc { return nil }
func (s *gateSpy) Validate(raw json.RawMessage, limits capability.ServerLimits) (any, error) {
	return struct{}{}, nil
}
func (s *gateSpy) Prompt(cfg any) string               { return "" }
func (s *gateSpy) Help(cfg any) []capability.HelpEntry { return nil }
func (s *gateSpy) New(cfg any, env capability.InstanceEnv) (starlark.Value, io.Closer, error) {
	s.mu.Lock()
	s.builds++
	cl := &spyCloser{}
	s.closers = append(s.closers, cl)
	s.gates = append(s.gates, env.Gate)
	onNew := s.onNew
	s.mu.Unlock()
	if onNew != nil {
		onNew()
	}
	b := starlark.NewBuiltin("probe.go", func(t *starlark.Thread, bi *starlark.Builtin, args starlark.Tuple, kw []starlark.Tuple) (starlark.Value, error) {
		return env.Gate.Invoke(capability.ThreadContext(t), "probe", "go", nil,
			func(ctx context.Context, _ map[string]any) (starlark.Value, map[string]any, error) {
				s.mu.Lock()
				s.ops = append(s.ops, capability.Call{})
				hook, block := s.onOp, s.block
				s.mu.Unlock()
				if hook != nil {
					hook()
				}
				if block != nil {
					select {
					case <-block:
					case <-ctx.Done():
						return nil, nil, ctx.Err()
					}
				}
				return starlark.None, nil, nil
			})
	})
	return &starlarkstruct.Module{Name: "probe", Members: starlark.StringDict{"go": b}}, cl, nil
}

func (s *gateSpy) stats() (builds, ops, closed int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.closers {
		closed += c.count()
	}
	return s.builds, len(s.ops), closed
}

// specFor builds a normalized spec JSON with fs+io+probe capabilities.
func specJSON(caps string) string {
	return fmt.Sprintf(`{"ttl_seconds":300,"limits":{"exec_timeout_ms":60000,"max_steps":10000000,"max_output_bytes":1048576},"capabilities":{%s}}`, caps)
}

var idCounter struct {
	sync.Mutex
	n int
}

func nextID() string {
	idCounter.Lock()
	defer idCounter.Unlock()
	idCounter.n++
	return fmt.Sprintf("rep_test%06d", idCounter.n)
}

// prepareBundle is the canonical test bundle.
func prepareBundle(src string, assets map[string][]byte) runtime.ProgramBundle {
	return runtime.ProgramBundle{
		Sources:      map[string][]byte{"main.star": []byte(src)},
		Assets:       assets,
		InputSchema:  json.RawMessage(`{}`),
		OutputSchema: json.RawMessage(`{}`),
	}
}

func prepReq(n *node, bundle runtime.ProgramBundle) *runtime.PrepareRequest {
	return prepReqCaps(n, bundle, `"fs":{},"io":{}`)
}

func prepReqCaps(n *node, bundle runtime.ProgramBundle, caps string) *runtime.PrepareRequest {
	spec := specJSON(caps)
	return &runtime.PrepareRequest{
		CreateRequest: runtime.CreateRequest{
			InstanceID: nextID(),
			Owner:      n.owner,
			Labels:     map[string]string{"k": "v"},
			Spec:       json.RawMessage(spec),
			ExpiresAt:  n.clock().Add(10 * time.Minute),
		},
		ReleaseID:  "rel-1",
		Generation: 1,
		Program:    bundle,
		PrepareID:  nextID() + "-prep",
	}
}

func invokeReq(r *runtime.PrepareRequest, input string) *runtime.InvokeRequest {
	return &runtime.InvokeRequest{
		InstanceID: r.InstanceID,
		Owner:      r.Owner,
		ReleaseID:  r.ReleaseID,
		Generation: r.Generation,
		Epoch:      r.Epoch,
		InvokeID:   nextID() + "-inv",
		Input:      json.RawMessage(input),
	}
}

func TestReplicaPrepareInvoke(t *testing.T) {
	now := time.Now()
	n := newNode(t, Options{Limits: defaultLimits()}, &now)
	// counter globals and VFS must reset every invoke.
	req := prepReq(n, prepareBundle(`counter = [0]
def main(input):
    counter[0] += 1
    seed = fs.read("seed.txt")
    fs.write("seed.txt", "mutated")
    return {"n": counter[0], "seed": seed}
`, map[string][]byte{"seed.txt": []byte("orig")}))
	resp, err := n.m.Prepare(context.Background(), req)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if resp.Result.Error != nil {
		t.Fatalf("prepare result: %+v", resp.Result.Error)
	}
	if n.m.Count() != 1 {
		t.Fatalf("count %d", n.m.Count())
	}
	for i := 0; i < 3; i++ {
		out, err := n.m.Invoke(context.Background(), invokeReq(req, `{"x":1}`))
		if err != nil {
			t.Fatalf("invoke %d: %v", i, err)
		}
		if out.Result.Error != nil {
			t.Fatalf("invoke %d: %+v", i, out.Result.Error)
		}
		got := string(out.Result.Output)
		if !strings.Contains(got, `"n":1`) || !strings.Contains(got, `"seed":"orig"`) {
			t.Fatalf("call %d saw stale state: %s", i, got)
		}
	}
}

func TestReplicaPrepareDenied(t *testing.T) {
	spy := &gateSpy{}
	reg := capability.NewRegistry()
	reg.Register(capfs.Factory())
	reg.Register(capio.Factory())
	reg.Register(spy)
	n2 := newNode(t, Options{Limits: defaultLimits(), Registry: reg}, nil)
	req := prepReqCaps(n2, prepareBundle(`probe.go()
def main(input):
    return {}
`, nil), `"fs":{},"io":{},"probe":{}`)
	resp, err := n2.m.Prepare(context.Background(), req)
	if err != nil {
		t.Fatalf("prepare should return 200 with error result: %v", err)
	}
	if resp.Result.Error == nil || resp.Result.Error.Code != "invalid_program" {
		t.Fatalf("expected invalid_program, got %+v", resp.Result.Error)
	}
	if len(resp.Audit.Events) == 0 {
		t.Fatal("expected deny audit")
	}
	if n2.m.Count() != 0 {
		t.Fatalf("failed prepare left a replica: %d", n2.m.Count())
	}
	if _, ops, _ := spy.stats(); ops != 0 {
		t.Fatalf("denied op executed %d times", ops)
	}
}

func TestReplicaInvalidInput(t *testing.T) {
	spy := &gateSpy{}
	reg := capability.NewRegistry()
	reg.Register(capfs.Factory())
	reg.Register(capio.Factory())
	reg.Register(spy)
	n := newNode(t, Options{Limits: defaultLimits(), Registry: reg}, nil)
	req := prepReqCaps(n, prepareBundle(`def main(input):
    return {}
`, nil), `"fs":{},"io":{},"probe":{}`)
	req.Program.InputSchema = json.RawMessage(`{"type":"object","required":["name"],"additionalProperties":false}`)
	if _, err := n.m.Prepare(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	b0, _, _ := spy.stats()
	out, err := n.m.Invoke(context.Background(), invokeReq(req, `{"nope":1}`))
	if err != nil {
		t.Fatalf("invoke should return 200-level result, got %v", err)
	}
	if out.Result.Error == nil || out.Result.Error.Code != "invalid_input" {
		t.Fatalf("expected invalid_input, got %+v", out.Result.Error)
	}
	if out.Result.Output != nil {
		t.Fatal("failure carried output")
	}
	b1, ops, _ := spy.stats()
	if b1 != b0 || ops != 0 {
		t.Fatalf("invalid input built %d sessions / %d ops", b1-b0, ops)
	}
}

func TestReplicaFences(t *testing.T) {
	n := newNode(t, Options{Limits: defaultLimits()}, nil)
	req := prepReq(n, prepareBundle(`def main(input): return {}`, nil))
	if _, err := n.m.Prepare(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	base := invokeReq(req, `{}`)
	for _, tc := range []struct {
		name string
		mut  func(*runtime.InvokeRequest)
		want error
	}{
		{"wrong owner", func(r *runtime.InvokeRequest) { r.Owner.UserID = "other" }, runtime.ErrNotOwner},
		{"empty owner", func(r *runtime.InvokeRequest) { r.Owner.UserID = "" }, runtime.ErrNotOwner},
		{"epoch", func(r *runtime.InvokeRequest) { r.Epoch = 9 }, runtime.ErrStaleEpoch},
		{"release", func(r *runtime.InvokeRequest) { r.ReleaseID = "rel-2" }, runtime.ErrStaleEpoch},
		{"generation", func(r *runtime.InvokeRequest) { r.Generation = 2 }, runtime.ErrStaleEpoch},
	} {
		r := *base
		tc.mut(&r)
		if _, err := n.m.Invoke(context.Background(), &r); !errors.Is(err, tc.want) {
			t.Fatalf("%s: got %v want %v", tc.name, err, tc.want)
		}
	}
	if _, err := n.m.Invoke(context.Background(), &runtime.InvokeRequest{InstanceID: "nope"}); !errors.Is(err, runtime.ErrNotFound) {
		t.Fatalf("missing replica: %v", err)
	}
}

func TestReplicaSerialization(t *testing.T) {
	spy := &gateSpy{block: make(chan struct{})}
	reg := capability.NewRegistry()
	reg.Register(capfs.Factory())
	reg.Register(capio.Factory())
	reg.Register(spy)
	n := newNode(t, Options{Limits: defaultLimits(), Registry: reg}, nil)
	req := prepReqCaps(n, prepareBundle(`def main(input):
    probe.go()
    return {}
`, nil), `"fs":{},"io":{},"probe":{}`)
	if _, err := n.m.Prepare(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	in := make(chan *runtime.InvokeResponse, 1)
	go func() { r, _ := n.m.Invoke(context.Background(), invokeReq(req, `{}`)); in <- r }()
	// Wait until the first call is inside the blocked op.
	for i := 0; i < 200; i++ {
		if _, ops, _ := spy.stats(); ops > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	// Queued call waits.
	second := make(chan *runtime.InvokeResponse, 1)
	go func() { r, _ := n.m.Invoke(context.Background(), invokeReq(req, `{}`)); second <- r }()
	select {
	case <-second:
		t.Fatal("second invoke completed while first blocked")
	case <-time.After(30 * time.Millisecond):
	}
	close(spy.block)
	if r := <-in; r.Result.Error != nil {
		t.Fatalf("first: %+v", r.Result.Error)
	}
	if r := <-second; r.Result.Error != nil {
		t.Fatalf("second: %+v", r.Result.Error)
	}
	if _, ops, _ := spy.stats(); ops != 2 {
		t.Fatalf("ops %d", ops)
	}
}

func TestReplicaDelete(t *testing.T) {
	spy := &gateSpy{block: make(chan struct{})}
	reg := capability.NewRegistry()
	reg.Register(capfs.Factory())
	reg.Register(capio.Factory())
	reg.Register(spy)
	n := newNode(t, Options{Limits: defaultLimits(), Registry: reg}, nil)
	req := prepReqCaps(n, prepareBundle(`def main(input):
    probe.go()
    return {}
`, nil), `"fs":{},"io":{},"probe":{}`)
	if _, err := n.m.Prepare(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	done := make(chan *runtime.InvokeResponse, 1)
	go func() { r, _ := n.m.Invoke(context.Background(), invokeReq(req, `{}`)); done <- r }()
	for i := 0; i < 200; i++ {
		if _, ops, _ := spy.stats(); ops > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := n.m.Delete(context.Background(), &runtime.DeleteRequest{InstanceID: req.InstanceID, Owner: n.owner, Epoch: req.Epoch}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	close(spy.block)
	if r := <-done; r != nil && r.Result.Error == nil {
		t.Fatal("deleted replica invoke reported success")
	}
	if n.m.Count() != 0 {
		t.Fatalf("count %d after delete", n.m.Count())
	}
	if _, _, closed := spy.stats(); closed == 0 {
		t.Fatal("closers not closed")
	}
	if _, err := n.m.Invoke(context.Background(), invokeReq(req, `{}`)); !errors.Is(err, runtime.ErrNotFound) {
		t.Fatalf("invoke after delete: %v", err)
	}
}

func TestReplicaLease(t *testing.T) {
	now := time.Now()
	n := newNode(t, Options{Limits: defaultLimits()}, &now)
	req := prepReq(n, prepareBundle(`def main(input): return {}`, nil))
	req.ExpiresAt = now.Add(time.Minute)
	if _, err := n.m.Prepare(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	// Renewal extends future calls.
	if _, err := n.m.Keepalive(context.Background(), &runtime.KeepaliveRequest{InstanceID: req.InstanceID, Owner: n.owner, Epoch: 0, RenewedExpiresAt: now.Add(5 * time.Minute)}); err != nil {
		t.Fatalf("keepalive: %v", err)
	}
	// Expire it.
	now = now.Add(10 * time.Minute)
	if _, err := n.m.Invoke(context.Background(), invokeReq(req, `{}`)); !errors.Is(err, runtime.ErrNotRunning) {
		t.Fatalf("expired invoke: %v", err)
	}
	// Cannot revive an expired lease.
	if _, err := n.m.Keepalive(context.Background(), &runtime.KeepaliveRequest{InstanceID: req.InstanceID, Owner: n.owner, RenewedExpiresAt: now.Add(time.Hour)}); !errors.Is(err, runtime.ErrNotRunning) {
		t.Fatalf("revival: %v", err)
	}
	// Delete on expired replica works.
	if _, err := n.m.Delete(context.Background(), &runtime.DeleteRequest{InstanceID: req.InstanceID, Owner: n.owner, Epoch: 0}); err != nil {
		t.Fatalf("delete expired: %v", err)
	}
	if n.m.Count() != 0 {
		t.Fatalf("count %d", n.m.Count())
	}
}

func TestReplicaNamespaceQuota(t *testing.T) {
	n := newNode(t, Options{Limits: defaultLimits(), MaxInstances: 2}, nil)
	req := prepReq(n, prepareBundle(`def main(input): return {}`, nil))
	if _, err := n.m.Prepare(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	// Same ID create collides.
	if _, err := n.m.Create(context.Background(), &runtime.CreateRequest{InstanceID: req.InstanceID, Owner: n.owner, Spec: json.RawMessage(`{"ttl_seconds":60}`), ExpiresAt: n.clock().Add(time.Hour)}); err == nil {
		t.Fatal("create on replica ID succeeded")
	}
	// Quota shared across both maps.
	_, err := n.m.Create(context.Background(), &runtime.CreateRequest{InstanceID: nextID(), Owner: n.owner, Spec: json.RawMessage(`{"ttl_seconds":60}`), ExpiresAt: n.clock().Add(time.Hour)})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := n.m.Prepare(context.Background(), prepReq(n, prepareBundle(`def main(input): return {}`, nil))); !errors.Is(err, runtime.ErrTooMany) {
		t.Fatalf("quota: %v", err)
	}
	// A pending/prepare'd replica id also collides on the dev side.
	if _, err := n.m.Create(context.Background(), &runtime.CreateRequest{InstanceID: req.InstanceID, Owner: n.owner, Spec: json.RawMessage(`{"ttl_seconds":60}`), ExpiresAt: n.clock().Add(time.Hour)}); err == nil {
		t.Fatal("create on replica id succeeded")
	}
}

func TestReplicaMutationAfterPrepare(t *testing.T) {
	n := newNode(t, Options{Limits: defaultLimits()}, nil)
	req := prepReq(n, prepareBundle(`def main(input):
    return fs.read("a.txt")
`, map[string][]byte{"a.txt": []byte("orig")}))
	if _, err := n.m.Prepare(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	// Mutate caller-held request fields: the replica is immutable.
	req.Labels["k"] = "changed"
	req.Program.Assets["a.txt"][0] = 'X'
	req.Owner.Email = "changed@x"
	out, err := n.m.Invoke(context.Background(), invokeReq(req, `{}`))
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if out.Result.Error != nil {
		t.Fatalf("invoke result: %+v", out.Result.Error)
	}
	if string(out.Result.Output) != `"orig"` {
		t.Fatalf("mutation leaked: %s", out.Result.Output)
	}
}

func TestReplicaQueueCapacity(t *testing.T) {
	spy := &gateSpy{block: make(chan struct{})}
	reg := capability.NewRegistry()
	reg.Register(capfs.Factory())
	reg.Register(capio.Factory())
	reg.Register(spy)
	n := newNode(t, Options{Limits: defaultLimits(), Registry: reg}, nil)
	req := prepReqCaps(n, prepareBundle(`def main(input):
    probe.go()
    return {}
`, nil), `"fs":{},"io":{},"probe":{}`)
	if _, err := n.m.Prepare(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	// Occupy the single run slot.
	first := make(chan *runtime.InvokeResponse, 1)
	go func() { r, _ := n.m.Invoke(context.Background(), invokeReq(req, `{}`)); first <- r }()
	for i := 0; i < 500; i++ {
		if _, ops, _ := spy.stats(); ops > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	// 32 queued admissions are accepted.
	var wg sync.WaitGroup
	queued := make(chan *runtime.InvokeResponse, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, _ := n.m.Invoke(context.Background(), invokeReq(req, `{}`))
			queued <- r
		}()
	}
	for i := 0; i < 500; i++ {
		n.m.mu.Lock()
		full := len(n.m.replicas[req.InstanceID].slots) == replicaSlots
		n.m.mu.Unlock()
		if full {
			break
		}
		time.Sleep(time.Millisecond)
	}
	// The 34th call is refused, not queued.
	if _, err := n.m.Invoke(context.Background(), invokeReq(req, `{}`)); !errors.Is(err, runtime.ErrTooMany) {
		t.Fatalf("34th call: %v", err)
	}
	close(spy.block)
	wg.Wait()
	if r := <-first; r.Result.Error != nil {
		t.Fatalf("first: %+v", r.Result.Error)
	}
	for i := 0; i < 32; i++ {
		<-queued
	}
}

func TestReplicaQueuedCancel(t *testing.T) {
	spy := &gateSpy{block: make(chan struct{})}
	reg := capability.NewRegistry()
	reg.Register(capfs.Factory())
	reg.Register(capio.Factory())
	reg.Register(spy)
	n := newNode(t, Options{Limits: defaultLimits(), Registry: reg}, nil)
	req := prepReqCaps(n, prepareBundle(`def main(input):
    probe.go()
    return {}
`, nil), `"fs":{},"io":{},"probe":{}`)
	if _, err := n.m.Prepare(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	go func() { n.m.Invoke(context.Background(), invokeReq(req, `{}`)) }()
	for i := 0; i < 500; i++ {
		if _, ops, _ := spy.stats(); ops > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	// Queued call times out on its own deadline before the slot frees.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	out, err := n.m.Invoke(ctx, invokeReq(req, `{}`))
	if err != nil {
		t.Fatalf("queued timeout should return a result, got %v", err)
	}
	if out.Result.Error == nil || out.Result.Error.Code != "timeout" {
		t.Fatalf("expected timeout, got %+v", out.Result.Error)
	}
	close(spy.block)
}

func TestReplicaDeleteQueued(t *testing.T) {
	spy := &gateSpy{block: make(chan struct{})}
	reg := capability.NewRegistry()
	reg.Register(capfs.Factory())
	reg.Register(capio.Factory())
	reg.Register(spy)
	n := newNode(t, Options{Limits: defaultLimits(), Registry: reg}, nil)
	req := prepReqCaps(n, prepareBundle(`def main(input):
    probe.go()
    return {}
`, nil), `"fs":{},"io":{},"probe":{}`)
	if _, err := n.m.Prepare(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	active := make(chan *runtime.InvokeResponse, 1)
	go func() { r, _ := n.m.Invoke(context.Background(), invokeReq(req, `{}`)); active <- r }()
	for i := 0; i < 500; i++ {
		if _, ops, _ := spy.stats(); ops > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	queued := make(chan *runtime.InvokeResponse, 2)
	for i := 0; i < 2; i++ {
		go func() { r, _ := n.m.Invoke(context.Background(), invokeReq(req, `{}`)); queued <- r }()
	}
	for i := 0; i < 500; i++ {
		n.m.mu.Lock()
		full := len(n.m.replicas[req.InstanceID].slots) == 3
		n.m.mu.Unlock()
		if full {
			break
		}
		time.Sleep(time.Millisecond)
	}
	// Delete must cancel the blocked op's ctx and drain every call;
	// spy.block stays closed so nothing can "succeed" first.
	if _, err := n.m.Delete(context.Background(), &runtime.DeleteRequest{InstanceID: req.InstanceID, Owner: n.owner, Epoch: req.Epoch}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	close(spy.block)
	if n.m.Count() != 0 {
		t.Fatalf("count %d", n.m.Count())
	}
	builds, _, closed := spy.stats()
	if builds != closed {
		t.Fatalf("built %d closed %d", builds, closed)
	}
	if r := <-active; r != nil && r.Result.Error == nil {
		t.Fatal("active call succeeded after delete")
	}
	for i := 0; i < 2; i++ {
		r := <-queued
		if r != nil && r.Result.Error == nil {
			t.Fatal("queued call succeeded after delete")
		}
	}
}

func TestReplicaPrepareRace(t *testing.T) {
	// While the factory is blocked inside prepare, invoke/keepalive/
	// delete/reap against the published-but-not-ready record must not
	// panic or race.
	for i := 0; i < 20; i++ {
		spy := &gateSpy{}
		release := make(chan struct{})
		spy.onNew = func() { <-release }
		spy.onOp = nil
		reg := capability.NewRegistry()
		reg.Register(capfs.Factory())
		reg.Register(capio.Factory())
		reg.Register(spy)
		n := newNode(t, Options{Limits: defaultLimits(), Registry: reg}, nil)
		req := prepReqCaps(n, prepareBundle(`def main(input): return {}`, nil), `"fs":{},"io":{},"probe":{}`)
		done := make(chan *runtime.PrepareResponse, 1)
		go func() { r, _ := n.m.Prepare(context.Background(), req); done <- r }()
		// Wait until the record is in the map.
		var ok bool
		for j := 0; j < 500; j++ {
			n.m.mu.Lock()
			_, ok = n.m.replicas[req.InstanceID]
			n.m.mu.Unlock()
			if ok {
				break
			}
			time.Sleep(time.Millisecond)
		}
		if !ok {
			t.Fatal("prepare never published")
		}
		if _, err := n.m.Invoke(context.Background(), invokeReq(req, `{}`)); !errors.Is(err, runtime.ErrNotRunning) {
			t.Fatalf("invoke during prepare: %v", err)
		}
		_, _ = n.m.Keepalive(context.Background(), &runtime.KeepaliveRequest{InstanceID: req.InstanceID, Owner: n.owner, RenewedExpiresAt: n.clock().Add(time.Hour)})
		n.m.Reap(context.Background())
		// The scheduled Delete must bar admission before the blocked
		// prepare can commit — wait for its lifecycle cancel before
		// releasing the factory.
		n.m.mu.Lock()
		r := n.m.replicas[req.InstanceID]
		n.m.mu.Unlock()
		delDone := make(chan error, 1)
		go func() {
			_, err := n.m.Delete(context.Background(), &runtime.DeleteRequest{InstanceID: req.InstanceID, Owner: n.owner})
			delDone <- err
		}()
		select {
		case <-r.lifecycle.Done():
		case <-time.After(5 * time.Second):
			close(release)
			t.Fatal("delete never cancelled the replica lifecycle")
		}
		close(release)
		resp := <-done
		// A prepare committed after Delete must never report a ready
		// success; it resolves as a timed-out failure.
		if resp != nil && resp.Result.Error == nil {
			t.Fatal("deleted replica committed as ready")
		}
		select {
		case <-delDone:
		case <-time.After(5 * time.Second):
			t.Fatal("delete hung on blocked prepare")
		}
	}
}

func TestReplicaTimeoutValidation(t *testing.T) {
	n := newNode(t, Options{Limits: defaultLimits()}, nil)
	req := prepReq(n, prepareBundle(`def main(input): return {}`, nil))
	if _, err := n.m.Prepare(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	for _, ms := range []int64{-1, 1 << 62, 120000} { // 120s > 60s release limit
		r := invokeReq(req, `{}`)
		r.TimeoutMs = ms
		if _, err := n.m.Invoke(context.Background(), r); err == nil {
			t.Fatalf("timeout_ms=%d accepted", ms)
		}
	}
	// Narrower timeout works.
	r := invokeReq(req, `{}`)
	r.TimeoutMs = 5000
	if _, err := n.m.Invoke(context.Background(), r); err != nil {
		t.Fatalf("narrow timeout rejected: %v", err)
	}
}

// A pending Create must hold the shared namespace slot and quota while
// its session factory is still blocked.
func TestReplicaPendingCreateNamespace(t *testing.T) {
	spy := &gateSpy{}
	release := make(chan struct{})
	var entered sync.WaitGroup
	entered.Add(1)
	spy.onNew = func() { entered.Done(); <-release }
	reg := capability.NewRegistry()
	reg.Register(capfs.Factory())
	reg.Register(capio.Factory())
	reg.Register(spy)
	n := newNode(t, Options{Limits: defaultLimits(), Registry: reg, MaxInstances: 1}, nil)

	spec := json.RawMessage(specJSON(`"fs":{},"io":{},"probe":{}`))
	pending := make(chan error, 1)
	go func() {
		_, err := n.m.Create(context.Background(), &runtime.CreateRequest{
			InstanceID: "pend-1", Owner: n.owner, Spec: spec,
			ExpiresAt: n.clock().Add(time.Hour),
		})
		pending <- err
	}()
	entered.Wait() // factory blocked inside the pending create

	// Same id collides while the create is still pending.
	prep := prepReqCaps(n, prepareBundle(`def main(input): return {}`, nil), `"fs":{},"io":{},"probe":{}`)
	prep.InstanceID = "pend-1"
	if _, err := n.m.Prepare(context.Background(), prep); err == nil {
		t.Fatal("prepare claimed a pending-create id")
	}
	// Pending reservation counts against the shared quota.
	prep.InstanceID = nextID()
	if _, err := n.m.Prepare(context.Background(), prep); !errors.Is(err, runtime.ErrTooMany) {
		t.Fatalf("pending create did not count toward quota: %v", err)
	}
	if n.m.Count() != 1 {
		t.Fatalf("pending create missing from count: %d", n.m.Count())
	}
	close(release)
	if err := <-pending; err != nil {
		t.Fatalf("pending create: %v", err)
	}
	if n.m.Count() != 1 {
		t.Fatalf("count %d", n.m.Count())
	}
}

func TestReplicaInterruptWaiting(t *testing.T) {
	now := time.Now()
	n := newNode(t, Options{Limits: defaultLimits()}, &now)
	bundle := prepareBundle(`def main(input):
    req = interrupt.new("approval", title = "go?", expires_in_s = 30)
    interrupt.wait(req["id"], handler = "on_ok", context = {"order_id": "o1"})

def on_ok(ctx):
    return {"status": "ok"}
`, nil)
	bundle.Handlers = map[string]runtime.HandlerSpec{
		"on_ok": {ContextSchema: json.RawMessage(`{"type":"object","required":["order_id"],"properties":{"order_id":{"type":"string"}}}`)},
	}
	req := prepReq(n, bundle)
	resp, err := n.m.Prepare(context.Background(), req)
	if err != nil || resp.Result.Error != nil {
		t.Fatalf("prepare: %v %+v", err, resp.Result.Error)
	}

	inv := invokeReq(req, `{}`)
	inv.RunID = "run_1"
	inv.Interrupts = []runtime.InterruptSlot{{ID: "int_1", ConsoleURL: "https://console/int_1"}}
	out, err := n.m.Invoke(context.Background(), inv)
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if out.Result.Error != nil || out.Result.Output != nil {
		t.Fatalf("expected waiting, got err=%+v out=%s", out.Result.Error, out.Result.Output)
	}
	w := out.Result.Waiting
	if w == nil || w.InterruptID != "int_1" || w.Handler != "on_ok" || w.Kind != "approval" ||
		w.Title != "go?" || w.ExpiresInS != 30 || string(w.Context) != `{"order_id":"o1"}` {
		t.Fatalf("bad waiting: %+v", w)
	}

	// A handler entry receives the full handler context.
	inv2 := invokeReq(req, `{"interrupt_id":"int_1","context":{"order_id":"o9"},"event":{"approved":true}}`)
	inv2.Entry = "on_ok"
	inv2.RunID = "run_1"
	inv2.Step = 1
	out2, err := n.m.Invoke(context.Background(), inv2)
	if err != nil || out2.Result.Error != nil {
		t.Fatalf("handler invoke: %v %+v", err, out2.Result.Error)
	}
	if !strings.Contains(string(out2.Result.Output), `"status":"ok"`) {
		t.Fatalf("handler output: %s", out2.Result.Output)
	}
}
