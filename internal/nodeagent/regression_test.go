package nodeagent

import (
	"context"
	"errors"
	"testing"
	"time"

	"calcside/internal/runtime"
)

// regSync builds a fresh-now sync response carrying one assignment.
func regSync(as runtime.ReplicaAssignment) *runtime.NodeSyncResponse {
	return &runtime.NodeSyncResponse{
		Assignments: []runtime.ReplicaAssignment{as}, ServerNow: time.Now()}
}

func regPrep(as runtime.ReplicaAssignment) *runtime.NodePrepareResponse {
	return &runtime.NodePrepareResponse{
		ServerNow: time.Now(),
		Request: runtime.PrepareRequest{
			CreateRequest: runtime.CreateRequest{
				InstanceID: as.ReplicaID, Owner: as.Owner,
				ExpiresAt: as.ExpiresAt, Epoch: as.Epoch,
			},
			ReleaseID: as.ReleaseID, Generation: as.Generation, PrepareID: "prp_1",
		},
	}
}

// Shutdown's final flush must send pending reports only — a response
// carrying new assignments must not spawn prepare work after Run.
func TestShutdownFlushNoReconcile(t *testing.T) {
	ctl := &fakeCtl{}
	rt := &fakeRT{}
	exp := time.Now().Add(30 * time.Second)
	as := assign("rep_1", "pin_1", 1, exp)
	ctl.syncResp = regSync(as)
	ctl.prepResp = regPrep(as)
	a := fastAgent(ctl, rt)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	wait := time.Now().Add(2 * time.Second)
	for time.Now().Before(wait) && rt.prepares.Load() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if rt.prepares.Load() != 1 {
		t.Fatalf("prepares %d", rt.prepares.Load())
	}
	// Wait until the replica is reported ready in inventory, then
	// cancel. The final flush (reports-only, empty inventory) gets a
	// response carrying a NEW assignment — it must never be applied.
	waitReady := time.Now().Add(2 * time.Second)
	for time.Now().Before(waitReady) {
		ctl.mu.Lock()
		n := len(ctl.lastInv)
		ctl.mu.Unlock()
		if n == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	ctl.mu.Lock()
	ctl.onSync = func() {
		if len(ctl.lastReq.Inventory) == 0 && len(ctl.lastReq.Reports) > 0 {
			ctl.syncResp = regSync(assign("rep_2", "pin_2", 1, exp))
		}
	}
	ctl.mu.Unlock()
	cancel()
	<-done
	if rt.prepares.Load() != 1 {
		t.Fatalf("post-shutdown prepare: %d", rt.prepares.Load())
	}
	ctl.mu.Lock()
	last := ctl.lastReq
	ctl.mu.Unlock()
	if last == nil || len(last.Inventory) != 0 {
		t.Fatalf("flush inventory: %+v", last)
	}
	a.mu.Lock()
	left := len(a.managed)
	a.mu.Unlock()
	if left != 0 {
		t.Fatalf("managed leak: %d", left)
	}
}

// Failed prepare with an unverifiable delete emits audit_only (audit
// preserved, no terminal ack, no lease cut) and the tombstone blocks
// re-bootstrapping the same id on later polls.
func TestFailedPrepareAuditOnlyTombstone(t *testing.T) {
	ctl := &fakeCtl{}
	rt := &fakeRT{
		prepRes: &runtime.PrepareResponse{
			Result: runtime.ProgramResult{Error: &runtime.ProgramError{Code: "policy_denied"}},
			Audit:  runtime.AuditBatch{Events: []runtime.AuditEvent{{Ts: time.Now()}}},
		},
		delErr: errors.New("transport lost"),
	}
	exp := time.Now().Add(30 * time.Second)
	as := assign("rep_1", "pin_1", 1, exp)
	ctl.syncResp = regSync(as)
	ctl.prepResp = regPrep(as)
	a := fastAgent(ctl, rt)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	// Let several polls pass — the same id keeps coming back.
	wait := time.Now().Add(2 * time.Second)
	var sawAuditOnly bool
	for time.Now().Before(wait) {
		ctl.mu.Lock()
		n := ctl.syncs
		ctl.mu.Unlock()
		if n >= 4 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	ctl.mu.Lock()
	if ctl.lastReq != nil {
		for _, r := range ctl.lastReq.Reports {
			if r.State == runtime.NodeReplicaAuditOnly && len(r.Audit.Events) == 1 {
				sawAuditOnly = true
			}
			if r.State == runtime.NodeReplicaFailed || r.State == runtime.NodeReplicaStopped {
				t.Fatalf("terminal report on unverified cleanup: %s", r.State)
			}
		}
	}
	ctl.mu.Unlock()
	if !sawAuditOnly {
		t.Fatal("audit_only report missing")
	}
	if rt.prepares.Load() != 1 {
		t.Fatalf("tombstone re-prepared: %d", rt.prepares.Load())
	}
	cancel()
	<-done
}

// Keepalive errors never extend the confirmed lease: a fresh
// ServerNow/extending desired set cannot revive the context; the pure
// local timer deletes it.
func TestKeepaliveFailurePureExpiry(t *testing.T) {
	ctl := &fakeCtl{}
	rt := &fakeRT{keepErr: errors.New("transient")}
	exp := time.Now().Add(150 * time.Millisecond)
	as := assign("rep_1", "pin_1", 1, exp)
	ctl.prepResp = regPrep(as) // the 150ms fetch lease, not the later 1s
	started := time.Now()
	// Desired keeps claiming a longer lease on each poll.
	ctl.onSync = func() {
		as.ExpiresAt = time.Now().Add(time.Second)
		ctl.syncResp = regSync(as)
	}
	ctl.onSync()
	a := fastAgent(ctl, rt)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	// The confirmed lease is ~150ms — the timer must delete even
	// though the API keeps "extending" the desired deadline.
	wait := time.Now().Add(2 * time.Second)
	for time.Now().Before(wait) && rt.deletes.Load() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if rt.deletes.Load() != 1 {
		t.Fatalf("no timer delete: %d", rt.deletes.Load())
	}
	if time.Since(started) > time.Second {
		t.Fatalf("delete late: %v", time.Since(started))
	}
	if rt.prepares.Load() != 1 {
		t.Fatalf("re-prepared: %d", rt.prepares.Load())
	}
	cancel()
	<-done
}

// A slow prepare must not block heartbeat polls; inventory stays
// empty until the prepare lands.
func TestSlowPrepareUnblocksHeartbeats(t *testing.T) {
	ctl := &fakeCtl{}
	rt := &fakeRT{prepBlock: make(chan struct{})}
	exp := time.Now().Add(30 * time.Second)
	as := assign("rep_1", "pin_1", 1, exp)
	ctl.syncResp = regSync(as)
	ctl.prepResp = regPrep(as)
	a := fastAgent(ctl, rt)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	wait := time.Now().Add(2 * time.Second)
	for time.Now().Before(wait) {
		ctl.mu.Lock()
		n := ctl.syncs
		ctl.mu.Unlock()
		if n >= 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	ctl.mu.Lock()
	if ctl.syncs < 3 {
		t.Fatalf("heartbeat starved: %d", ctl.syncs)
	}
	ctl.mu.Unlock()
	ctl.mu.Lock()
	inv := ctl.lastInv
	ctl.mu.Unlock()
	if len(inv) != 0 {
		t.Fatalf("inventory before ready: %+v", inv)
	}
	close(rt.prepBlock)
	for time.Now().Before(wait) {
		ctl.mu.Lock()
		inv = ctl.lastInv
		ctl.mu.Unlock()
		if len(inv) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(inv) != 1 {
		t.Fatal("ready never inventoried")
	}
	cancel()
	<-done
	if rt.deletes.Load() != 1 {
		t.Fatalf("shutdown deletes %d", rt.deletes.Load())
	}
}
