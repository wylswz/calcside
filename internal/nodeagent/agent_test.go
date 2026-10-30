package nodeagent

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"calcside/internal/runtime"
)

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

type fakeRT struct {
	runtime.Runtime
	prepares  atomic.Int32
	deletes   atomic.Int32
	keeps     atomic.Int32
	prepErr   error
	prepRes   *runtime.PrepareResponse
	delErr    error
	keepErr   error
	prepBlock chan struct{}
}

func (f *fakeRT) Prepare(ctx context.Context, r *runtime.PrepareRequest) (*runtime.PrepareResponse, error) {
	f.prepares.Add(1)
	if f.prepBlock != nil {
		select {
		case <-f.prepBlock:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.prepErr != nil {
		return nil, f.prepErr
	}
	if f.prepRes != nil {
		return f.prepRes, nil
	}
	return &runtime.PrepareResponse{}, nil
}

func (f *fakeRT) Delete(ctx context.Context, r *runtime.DeleteRequest) (*runtime.DeleteResponse, error) {
	f.deletes.Add(1)
	return &runtime.DeleteResponse{}, f.delErr
}

func (f *fakeRT) Keepalive(ctx context.Context, r *runtime.KeepaliveRequest) (*runtime.KeepaliveResponse, error) {
	f.keeps.Add(1)
	return &runtime.KeepaliveResponse{}, f.keepErr
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
	rt := &fakeRT{}
	exp := time.Now().Add(300 * time.Millisecond)
	as := assign("rep_1", "pin_1", 1, exp)
	ctl.syncResp = &runtime.NodeSyncResponse{
		Assignments: []runtime.ReplicaAssignment{as}, ServerNow: time.Now()}
	ctl.prepResp = &runtime.NodePrepareResponse{
		ServerNow: time.Now(),
		Request: runtime.PrepareRequest{
			CreateRequest: runtime.CreateRequest{
				InstanceID: "rep_1", Owner: runtime.Owner{UserID: "u1"},
				ExpiresAt: exp, Epoch: 1,
			},
			ReleaseID: "rel_1", Generation: 1, PrepareID: "prp_1",
		},
	}
	a := fastAgent(ctl, rt)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	// Wait for prepare then ready.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && rt.prepares.Load() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if rt.prepares.Load() != 1 {
		t.Fatalf("prepares %d", rt.prepares.Load())
	}
	// The API stops assigning it: the snapshot no longer renews the
	// lease -> delete + stopped report.
	ctl.mu.Lock()
	ctl.syncResp = &runtime.NodeSyncResponse{ServerNow: time.Now()}
	ctl.mu.Unlock()
	for time.Now().Before(deadline) && rt.deletes.Load() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if rt.deletes.Load() != 1 {
		t.Fatalf("deletes %d", rt.deletes.Load())
	}
	cancel()
	<-done
}

// A ready report enqueued mid-cycle must sync immediately — not on the
// next PollInterval tick.
func TestReadyReportWakesSync(t *testing.T) {
	ctl := &fakeCtl{}
	rt := &fakeRT{}
	exp := time.Now().Add(30 * time.Second)
	as := assign("rep_1", "pin_1", 1, exp)
	ctl.syncResp = &runtime.NodeSyncResponse{
		Assignments: []runtime.ReplicaAssignment{as}, ServerNow: time.Now()}
	ctl.prepResp = &runtime.NodePrepareResponse{
		ServerNow: time.Now(),
		Request: runtime.PrepareRequest{
			CreateRequest: runtime.CreateRequest{
				InstanceID: "rep_1", Owner: runtime.Owner{UserID: "u1"},
				ExpiresAt: exp, Epoch: 1,
			},
			ReleaseID: "rel_1", Generation: 1, PrepareID: "prp_1",
		},
	}
	a, err := New(Options{
		Control: ctl, Runtime: rt, NodeID: "n1", BootID: "b1",
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
	rt := &fakeRT{}
	a := fastAgent(ctl, rt)
	err := a.Run(context.Background())
	if err == nil || !errors.Is(err, runtime.ErrStaleEpoch) {
		t.Fatalf("run: %v", err)
	}
	if ctl.regs != 1 || rt.prepares.Load() != 0 {
		t.Fatalf("regs=%d prepares=%d", ctl.regs, rt.prepares.Load())
	}
}

func TestTransientSyncNotEmpty(t *testing.T) {
	ctl := &fakeCtl{}
	rt := &fakeRT{}
	// Assignment far-future so it survives transient sync failures.
	exp := time.Now().Add(30 * time.Second)
	as := assign("rep_1", "pin_1", 1, exp)
	ctl.syncResp = &runtime.NodeSyncResponse{
		Assignments: []runtime.ReplicaAssignment{as}, ServerNow: time.Now()}
	ctl.prepResp = &runtime.NodePrepareResponse{
		ServerNow: time.Now(),
		Request: runtime.PrepareRequest{
			CreateRequest: runtime.CreateRequest{
				InstanceID: "rep_1", Owner: runtime.Owner{UserID: "u1"},
				ExpiresAt: exp, Epoch: 1,
			},
			ReleaseID: "rel_1", Generation: 1, PrepareID: "prp_1",
		},
	}
	a := fastAgent(ctl, rt)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && rt.prepares.Load() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	// Flip to transient failure: the managed context must NOT be torn
	// down as if the desired set were empty.
	ctl.mu.Lock()
	ctl.syncErr = errors.New("network down")
	ctl.mu.Unlock()
	time.Sleep(120 * time.Millisecond)
	if rt.deletes.Load() != 0 {
		t.Fatal("transient sync deleted ready context")
	}
	cancel()
	<-done
	if rt.deletes.Load() != 1 {
		t.Fatalf("shutdown deletes %d", rt.deletes.Load())
	}
}

func TestFailedPrepareAudit(t *testing.T) {
	ctl := &fakeCtl{}
	rt := &fakeRT{
		prepRes: &runtime.PrepareResponse{
			Result: runtime.ProgramResult{Error: &runtime.ProgramError{Code: "policy_denied"}},
			Audit:  runtime.AuditBatch{Events: []runtime.AuditEvent{{Ts: time.Now()}}},
		},
	}
	exp := time.Now().Add(30 * time.Second)
	as := assign("rep_1", "pin_1", 1, exp)
	ctl.syncResp = &runtime.NodeSyncResponse{
		Assignments: []runtime.ReplicaAssignment{as}, ServerNow: time.Now()}
	ctl.prepResp = &runtime.NodePrepareResponse{
		ServerNow: time.Now(),
		Request: runtime.PrepareRequest{
			CreateRequest: runtime.CreateRequest{
				InstanceID: "rep_1", Owner: runtime.Owner{UserID: "u1"},
				ExpiresAt: exp, Epoch: 1,
			},
			ReleaseID: "rel_1", Generation: 1, PrepareID: "prp_1",
		},
	}
	a := fastAgent(ctl, rt)
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
	if rep.State != runtime.NodeReplicaFailed || rep.ErrorCode != "policy_denied" ||
		len(rep.Audit.Events) != 1 {
		t.Fatalf("report: %+v", rep)
	}
	cancel()
	<-done
}
