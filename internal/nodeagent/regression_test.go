package nodeagent

import (
	"context"
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
	return prepFor(as, okProgram())
}

// Shutdown's final flush must send pending reports only — a response
// carrying new assignments must not spawn prepare work after Run.
func TestShutdownFlushNoReconcile(t *testing.T) {
	ctl := &fakeCtl{}
	n := realNode(t, "")
	exp := time.Now().Add(30 * time.Second)
	as := assign("rep_1", "pin_1", 1, exp)
	ctl.syncResp = regSync(as)
	ctl.prepResp = regPrep(as)
	a := fastAgent(ctl, n.rt)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	waitCount(t, n.mgr, 1)
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
	// rep_2 must never bootstrap: no report ever carries it and the
	// manager holds nothing.
	a.mu.Lock()
	var rep2 int
	for _, r := range a.reports {
		if r.Assignment.ReplicaID == "rep_2" {
			rep2++
		}
	}
	left := len(a.managed)
	a.mu.Unlock()
	if rep2 != 0 {
		t.Fatal("post-shutdown assignment was bootstrapped")
	}
	if n.mgr.Count() != 0 {
		t.Fatalf("replica leaked: %d", n.mgr.Count())
	}
	ctl.mu.Lock()
	last := ctl.lastReq
	ctl.mu.Unlock()
	if last == nil || len(last.Inventory) != 0 {
		t.Fatalf("flush inventory: %+v", last)
	}
	if left != 0 {
		t.Fatalf("managed leak: %d", left)
	}
}

// Failed prepare with an unverifiable delete emits audit_only (audit
// preserved, no terminal ack, no lease cut) and the tombstone blocks
// re-bootstrapping the same id on later polls.
func TestFailedPrepareAuditOnlyTombstone(t *testing.T) {
	ctl := &fakeCtl{}
	n := realNode(t, "/delete")
	n.gate.fail.Store(true) // delete never verifies: unverifiable cleanup
	exp := time.Now().Add(30 * time.Second)
	as := assign("rep_1", "pin_1", 1, exp)
	ctl.syncResp = regSync(as)
	ctl.prepResp = prepFor(as, badProgram())
	a := fastAgent(ctl, n.rt)
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
	repReports := 0
	if ctl.lastReq != nil {
		for _, r := range ctl.lastReq.Reports {
			if r.Assignment.ReplicaID != "rep_1" {
				continue
			}
			repReports++
			if r.State == runtime.NodeReplicaAuditOnly && len(r.Audit.Events) > 0 {
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
	// The tombstone blocks re-bootstrap: rep_1 reports exactly once —
	// a re-prepare would have queued another audit_only by now.
	if repReports != 1 {
		t.Fatalf("tombstone re-prepared: %d reports", repReports)
	}
	if n.mgr.Count() != 0 {
		t.Fatalf("failed prepare left %d instances", n.mgr.Count())
	}
	cancel()
	<-done
}

// Keepalive errors never extend the confirmed lease: a fresh
// ServerNow/extending desired set cannot revive the context; the pure
// local timer deletes it.
func TestKeepaliveFailurePureExpiry(t *testing.T) {
	ctl := &fakeCtl{}
	n := realNode(t, "/keepalive")
	n.gate.fail.Store(true) // transient transport error on every renewal
	exp := time.Now().Add(500 * time.Millisecond)
	as := assign("rep_1", "pin_1", 1, exp)
	ctl.prepResp = regPrep(as) // the 500ms fetch lease, not the later 1s
	started := time.Now()
	// Desired keeps claiming a longer lease on each poll.
	ctl.onSync = func() {
		as.ExpiresAt = time.Now().Add(time.Second)
		ctl.syncResp = regSync(as)
	}
	ctl.onSync()
	a := fastAgent(ctl, n.rt)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	waitCount(t, n.mgr, 1)
	// The confirmed lease is ~500ms — the timer must delete even
	// though the API keeps "extending" the desired deadline.
	waitCount(t, n.mgr, 0)
	if time.Since(started) > time.Second {
		t.Fatalf("delete late: %v", time.Since(started))
	}
	cancel()
	<-done
	if n.mgr.Count() != 0 {
		t.Fatalf("re-prepared: %d instances", n.mgr.Count())
	}
}

// A slow prepare must not block heartbeat polls; inventory stays
// empty until the prepare lands.
func TestSlowPrepareUnblocksHeartbeats(t *testing.T) {
	ctl := &fakeCtl{}
	n := realNode(t, "/prepare")
	n.gate.hold = make(chan struct{}) // the worker's prepare stalls
	exp := time.Now().Add(30 * time.Second)
	as := assign("rep_1", "pin_1", 1, exp)
	ctl.syncResp = regSync(as)
	ctl.prepResp = regPrep(as)
	a := fastAgent(ctl, n.rt)
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
	close(n.gate.hold)
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
	waitCount(t, n.mgr, 0)
}
