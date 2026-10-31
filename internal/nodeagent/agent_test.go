package nodeagent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"calcside/internal/capability"
	"calcside/internal/engine"
	"calcside/internal/runtime"
	"calcside/internal/runtime/instance"
	"calcside/internal/runtime/remote"
)

var agentKey = []byte("agent-test-shared-key-0123456789a")

// gateRT injects transport faults on a path suffix — a real network
// failure or stall, the only runtime errors a test cannot distinguish
// from a dead node.
type gateRT struct {
	base   http.RoundTripper
	suffix string
	fail   atomic.Bool
	hold   chan struct{}
}

func (g *gateRT) RoundTrip(r *http.Request) (*http.Response, error) {
	if strings.HasSuffix(r.URL.Path, g.suffix) {
		if g.hold != nil {
			select {
			case <-g.hold:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		}
		if g.fail.Load() {
			return nil, errors.New("gate: transport failure")
		}
	}
	return g.base.RoundTrip(r)
}

// nodeRT is a Manager behind the signed worker protocol: the agent's
// runtime calls take the production path, gate injects faults on the
// named path suffix ("/prepare", "/keepalive", "/delete").
type nodeRT struct {
	mgr  *instance.Manager
	rt   *remote.Direct
	gate *gateRT
}

func realNode(t *testing.T, suffix string) *nodeRT {
	t.Helper()
	reg := capability.NewRegistry()
	mgr := instance.New(instance.Options{
		Engine: engine.New(8), Registry: reg,
		Limits: capability.ServerLimits{MaxExecTimeout: time.Minute},
	})
	t.Cleanup(mgr.StopReaper)
	srv := httptest.NewServer(remote.NewHandler(mgr, agentKey))
	t.Cleanup(srv.Close)
	gate := &gateRT{base: http.DefaultTransport, suffix: suffix}
	rt, err := remote.NewDirect(srv.URL, &http.Client{Transport: gate}, agentKey, "agent-test")
	if err != nil {
		t.Fatal(err)
	}
	return &nodeRT{mgr: mgr, rt: rt, gate: gate}
}

// waitCount polls until the manager holds n live instances.
func waitCount(t *testing.T, m *instance.Manager, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if m.Count() == n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("instance count %d, want %d", m.Count(), n)
}

// okProgram is the smallest valid replica program.
func okProgram() runtime.ProgramBundle {
	return runtime.ProgramBundle{
		Sources:      map[string][]byte{"main.star": []byte("def main(i):\n    return i\n")},
		InputSchema:  json.RawMessage(`{"type":"object","additionalProperties":true}`),
		OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":true}`),
	}
}

// badProgram fails prepare with a real invalid_program result and the
// engine's audit.
func badProgram() runtime.ProgramBundle {
	return runtime.ProgramBundle{
		Sources:      map[string][]byte{"main.star": []byte("print(\"top\")\ndef main(i):\n    return {}\n")},
		InputSchema:  json.RawMessage(`{}`),
		OutputSchema: json.RawMessage(`{}`),
	}
}

// prepFor builds the control-plane prepare payload for an assignment —
// a complete request the real Manager accepts.
func prepFor(as runtime.ReplicaAssignment, prog runtime.ProgramBundle) *runtime.NodePrepareResponse {
	return &runtime.NodePrepareResponse{
		ServerNow: time.Now(),
		Request: runtime.PrepareRequest{
			CreateRequest: runtime.CreateRequest{
				InstanceID: as.ReplicaID, Owner: as.Owner,
				ExpiresAt: as.ExpiresAt, Epoch: as.Epoch,
				Spec: json.RawMessage(`{"capabilities":{}}`),
			},
			ReleaseID: as.ReleaseID, Generation: as.Generation, PrepareID: "prp_1",
			Program: prog,
		},
	}
}

type fakeCtl struct {
	mu       sync.Mutex
	regs     int
	syncs    int
	preps    int
	regErr   error
	syncErr  error
	prepErr  error
	syncResp *runtime.NodeSyncResponse
	prepResp *runtime.NodePrepareResponse
	onSync   func()
	lastInv  []runtime.ReplicaAssignment
	lastReq  *runtime.NodeSyncRequest
}

func (f *fakeCtl) RegisterNode(ctx context.Context, r *runtime.NodeRegisterRequest) (*runtime.NodeRegisterResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.regs++
	if f.regErr != nil {
		return nil, f.regErr
	}
	return &runtime.NodeRegisterResponse{
		Node:      runtime.NodeIdentity{NodeID: r.NodeID, BootID: r.BootID, Incarnation: 1},
		ServerNow: time.Now(),
	}, nil
}

func (f *fakeCtl) SyncNode(ctx context.Context, r *runtime.NodeSyncRequest) (*runtime.NodeSyncResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.syncs++
	f.lastInv = r.Inventory
	cp := *r
	f.lastReq = &cp
	if f.onSync != nil {
		f.onSync()
	}
	if f.syncErr != nil {
		return nil, f.syncErr
	}
	return f.syncResp, nil
}

func (f *fakeCtl) PrepareReplica(ctx context.Context, r *runtime.NodePrepareRequest) (*runtime.NodePrepareResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.preps++
	if f.prepErr != nil {
		return nil, f.prepErr
	}
	return f.prepResp, nil
}

func assign(id, inst string, epoch int64, exp time.Time) runtime.ReplicaAssignment {
	return runtime.ReplicaAssignment{
		ReplicaID: id, InstanceID: inst, ReleaseID: "rel_1",
		Generation: 1, Epoch: epoch, ExpiresAt: exp,
		Owner: runtime.Owner{UserID: "u1"},
	}
}

func fastAgent(ctl runtime.NodeControl, rt runtime.Runtime) *Agent {
	a, err := New(Options{
		Control: ctl, Runtime: rt, NodeID: "n1", BootID: "b1",
		Capacity: 4, PollInterval: 20 * time.Millisecond, CallTimeout: time.Second,
	})
	if err != nil {
		panic(err)
	}
	return a
}

func TestLeaseDeadline(t *testing.T) {
	base := time.Now()
	// API clock 2h ahead/behind: result stays ~TTL, local-clock based.
	for _, skew := range []time.Duration{-2 * time.Hour, 2 * time.Hour} {
		srvNow := base.Add(skew)
		apiExp := srvNow.Add(30 * time.Second)
		d, ok := LeaseDeadline(base, 10*time.Millisecond, apiExp, srvNow)
		if !ok {
			t.Fatalf("skew %v rejected", skew)
		}
		got := time.Until(d)
		if got < 25*time.Second || got > 31*time.Second {
			t.Fatalf("skew %v deadline %v", skew, got)
		}
	}
	// Expired lease rejected.
	if _, ok := LeaseDeadline(base, 0, base.Add(-time.Second), base); ok {
		t.Fatal("expired accepted")
	}
	// RTT longer than TTL rejected.
	if _, ok := LeaseDeadline(base, time.Minute, base.Add(30*time.Second), base); ok {
		t.Fatal("rtt>ttl accepted")
	}
}

func TestStartupPrepareAndExpiry(t *testing.T) {
	ctl := &fakeCtl{}
	n := realNode(t, "")
	exp := time.Now().Add(300 * time.Millisecond)
	as := assign("rep_1", "pin_1", 1, exp)
	ctl.syncResp = &runtime.NodeSyncResponse{
		Assignments: []runtime.ReplicaAssignment{as}, ServerNow: time.Now()}
	ctl.prepResp = prepFor(as, okProgram())
	a := fastAgent(ctl, n.rt)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	waitCount(t, n.mgr, 1)
	// The API stops assigning it: the snapshot no longer renews the
	// lease -> delete + stopped report.
	ctl.mu.Lock()
	ctl.syncResp = &runtime.NodeSyncResponse{ServerNow: time.Now()}
	ctl.mu.Unlock()
	waitCount(t, n.mgr, 0)
	cancel()
	<-done
}

// A ready report enqueued mid-cycle must sync immediately — not on the
// next PollInterval tick.
func TestReadyReportWakesSync(t *testing.T) {
	ctl := &fakeCtl{}
	n := realNode(t, "")
	exp := time.Now().Add(30 * time.Second)
	as := assign("rep_1", "pin_1", 1, exp)
	ctl.syncResp = &runtime.NodeSyncResponse{
		Assignments: []runtime.ReplicaAssignment{as}, ServerNow: time.Now()}
	ctl.prepResp = prepFor(as, okProgram())
	a, err := New(Options{
		Control: ctl, Runtime: n.rt, NodeID: "n1", BootID: "b1",
		Capacity: 4, PollInterval: time.Hour, CallTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ctl.mu.Lock()
		var found bool
		if ctl.lastReq != nil {
			for _, r := range ctl.lastReq.Reports {
				if r.State == runtime.NodeReplicaReady {
					found = true
				}
			}
		}
		ctl.mu.Unlock()
		if found {
			cancel()
			<-done
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("ready report waited for the poll tick")
}

func TestStaleBootTerminal(t *testing.T) {
	ctl := &fakeCtl{regErr: runtime.ErrStaleEpoch}
	n := realNode(t, "")
	a := fastAgent(ctl, n.rt)
	err := a.Run(context.Background())
	if err == nil || !errors.Is(err, runtime.ErrStaleEpoch) {
		t.Fatalf("run: %v", err)
	}
	if ctl.regs != 1 || n.mgr.Count() != 0 {
		t.Fatalf("regs=%d instances=%d", ctl.regs, n.mgr.Count())
	}
}

func TestTransientSyncNotEmpty(t *testing.T) {
	ctl := &fakeCtl{}
	n := realNode(t, "")
	// Assignment far-future so it survives transient sync failures.
	exp := time.Now().Add(30 * time.Second)
	as := assign("rep_1", "pin_1", 1, exp)
	ctl.syncResp = &runtime.NodeSyncResponse{
		Assignments: []runtime.ReplicaAssignment{as}, ServerNow: time.Now()}
	ctl.prepResp = prepFor(as, okProgram())
	a := fastAgent(ctl, n.rt)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	waitCount(t, n.mgr, 1)
	// Flip to transient failure: the managed context must NOT be torn
	// down as if the desired set were empty.
	ctl.mu.Lock()
	ctl.syncErr = errors.New("network down")
	ctl.mu.Unlock()
	time.Sleep(120 * time.Millisecond)
	if n.mgr.Count() != 1 {
		t.Fatal("transient sync deleted ready context")
	}
	cancel()
	<-done
	waitCount(t, n.mgr, 0)
}

func TestFailedPrepareAudit(t *testing.T) {
	ctl := &fakeCtl{}
	n := realNode(t, "")
	exp := time.Now().Add(30 * time.Second)
	as := assign("rep_1", "pin_1", 1, exp)
	ctl.syncResp = &runtime.NodeSyncResponse{
		Assignments: []runtime.ReplicaAssignment{as}, ServerNow: time.Now()}
	ctl.prepResp = prepFor(as, badProgram())
	a := fastAgent(ctl, n.rt)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		a.mu.Lock()
		n := len(a.reports)
		a.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	a.mu.Lock()
	rep := a.reports[0]
	a.mu.Unlock()
	if rep.State != runtime.NodeReplicaFailed || rep.ErrorCode != "invalid_program" ||
		len(rep.Audit.Events) == 0 {
		t.Fatalf("report: %+v", rep)
	}
	cancel()
	<-done
}
