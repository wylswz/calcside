package nodeagent

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The advertised endpoint rides the signed registration verbatim —
// canonicalized at Options time — and never mutates afterwards.
func TestAdvertiseAddrRegistration(t *testing.T) {
	ctl := &fakeCtl{}
	n := realNode(t, "")
	a, err := New(Options{
		Control: ctl, Runtime: n.rt, NodeID: "n1", BootID: "b1",
		Capacity: 4, AdvertiseAddr: "[::ffff:10.1.2.3]:08090",
		PollInterval: 20 * time.Millisecond, CallTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if a.o.AdvertiseAddr != "[::ffff:10.1.2.3]:8090" {
		t.Fatalf("advertise addr not canonicalized: %q", a.o.AdvertiseAddr)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("agent run: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("agent.Run did not return after cancel")
		}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ctl.mu.Lock()
		addr, registered := "", false
		if ctl.lastReg != nil {
			addr, registered = ctl.lastReg.Addr, true
		}
		ctl.mu.Unlock()
		if registered {
			if addr != "[::ffff:10.1.2.3]:8090" {
				t.Fatalf("registered addr %q", addr)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("registration never happened")
}

func TestAdvertiseAddrValidation(t *testing.T) {
	ctl := &fakeCtl{}
	n := realNode(t, "")
	for _, bad := range []string{"http://x:1", "x:0", "", "  ", "x:99999", "0.0.0.0:1"} {
		if bad == "" {
			continue // empty means "no advertisement", always legal
		}
		if _, err := New(Options{
			Control: ctl, Runtime: n.rt, NodeID: "n", BootID: "b",
			Capacity: 1, AdvertiseAddr: bad,
		}); err == nil {
			t.Fatalf("invalid advertise addr %q accepted", bad)
		}
	}
	a, err := New(Options{
		Control: ctl, Runtime: n.rt, NodeID: "n", BootID: "b",
		Capacity: 1, AdvertiseAddr: "Worker-1.SVC:08090"})
	if err != nil {
		t.Fatal(err)
	}
	if a.o.AdvertiseAddr != "worker-1.svc:8090" {
		t.Fatalf("canonical form: %q", a.o.AdvertiseAddr)
	}
	// No advertisement configured: registration sends no addr.
	b, err := New(Options{
		Control: ctl, Runtime: n.rt, NodeID: "n", BootID: "b", Capacity: 1})
	if err != nil {
		t.Fatal(err)
	}
	if b.o.AdvertiseAddr != "" {
		t.Fatalf("unexpected advertise addr %q", b.o.AdvertiseAddr)
	}
}
