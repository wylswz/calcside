package instance

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"calcside/internal/runtime"
)

// A node reclaims memory only after the grace period: expiry as a
// status is the API tier's call, and the node must not race ahead of
// it and drop an instance the API tier still believes is running.
func TestLocalReclaimWaitsOutGrace(t *testing.T) {
	now := time.Now()
	n := newNode(t, Options{Limits: defaultLimits(), ReapGrace: time.Minute}, &now)
	ctx := context.Background()
	id := n.mustCreate(`{"ttl_seconds":60,"capabilities":{"fs":{}}}`)

	// Past the deadline but inside the grace window: still held.
	now = now.Add(90 * time.Second)
	n.m.Reap(ctx)
	if n.m.Count() != 1 {
		t.Fatalf("reclaimed inside grace window")
	}
	if _, err := n.exec(id, "print(1)"); err != nil {
		t.Fatalf("instance should still run inside grace: %v", err)
	}

	// Past deadline + grace, with no renewal in between.
	now = now.Add(10 * time.Minute)
	n.m.Reap(ctx)
	if n.m.Count() != 0 {
		t.Fatalf("expected reclaim, %d still live", n.m.Count())
	}
	if _, err := n.exec(id, "print(1)"); !errors.Is(err, runtime.ErrNotFound) {
		t.Fatalf("expected ErrNotFound after reclaim, got %v", err)
	}
}

func TestKeepaliveExtends(t *testing.T) {
	now := time.Now()
	n := newNode(t, Options{Limits: defaultLimits(), ReapGrace: time.Minute}, &now)
	id := n.mustCreate(`{"ttl_seconds":60}`)

	now = now.Add(50 * time.Second)
	if err := n.keepalive(id); err != nil {
		t.Fatal(err)
	}
	// 100s since creation, but only 50s since the renewal.
	now = now.Add(50 * time.Second)
	n.m.Reap(context.Background())
	if _, err := n.exec(id, "print('alive')"); err != nil {
		t.Fatalf("instance should still be alive: %v", err)
	}
}

// The per-user quota moved to the API tier, which can see the whole
// cluster. What a node enforces is its own capacity.
func TestNodeInstanceLimit(t *testing.T) {
	now := time.Now()
	n := newNode(t, Options{Limits: defaultLimits(), MaxInstances: 2}, &now)
	for i := 0; i < 2; i++ {
		if _, err := n.create(`{}`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := n.create(`{}`); !errors.Is(err, runtime.ErrTooMany) {
		t.Fatalf("expected ErrTooMany, got %v", err)
	}
}

// A node is reachable by anything on the internal network, so it must
// not take the caller's word for who owns an instance.
func TestOwnershipRecheckedOnNode(t *testing.T) {
	now := time.Now()
	n := newNode(t, Options{Limits: defaultLimits()}, &now)
	id := n.mustCreate(`{"capabilities":{"fs":{}}}`)

	_, err := n.m.Exec(context.Background(), &runtime.ExecRequest{
		InstanceID: id,
		Owner:      runtime.Owner{UserID: "usr_someone_else"},
		ExecID:     "exe_intruder",
		Code:       "print(1)",
	})
	if !errors.Is(err, runtime.ErrNotOwner) {
		t.Fatalf("expected ErrNotOwner, got %v", err)
	}
}

func TestConcurrentExecsSerialized(t *testing.T) {
	now := time.Now()
	n := newNode(t, Options{Limits: defaultLimits()}, &now)
	id := n.mustCreate(`{"capabilities":{"fs":{}}}`)

	// Hammer the same instance from many goroutines; -race catches
	// unsynchronized globals/buffer access.
	n.mustExec(id, "counter = 0")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := n.exec(id, "counter = counter + 1\nfs.write('n.txt', str(counter))")
			if err != nil || res.Error != nil {
				t.Errorf("exec %d: %v %+v", i, err, res)
			}
		}(i)
	}
	wg.Wait()
}

// Every gated call must come back on the response: the node cannot
// write the audit log itself.
func TestAuditRidesTheResponse(t *testing.T) {
	now := time.Now()
	n := newNode(t, Options{Limits: defaultLimits()}, &now)
	id := n.mustCreate(`{"capabilities":{"fs":{}}}`)
	n.mustExec(id, "fs.write('a.txt', 'hello')\nfs.read('a.txt')")

	var ops []string
	for _, ev := range n.audit() {
		if ev.Capability == "fs" {
			ops = append(ops, string(ev.Op))
		}
		if ev.InstanceID != id {
			t.Fatalf("event attributed to %s, want %s", ev.InstanceID, id)
		}
	}
	if len(ops) < 2 {
		t.Fatalf("expected write+read in the audit batch, got %v", ops)
	}
	// Draining is per request: a second exec reports only its own.
	before := len(n.audit())
	n.mustExec(id, "fs.read('a.txt')")
	if got := len(n.audit()) - before; got == 0 || got > 2 {
		t.Fatalf("second exec reported %d events, want 1..2", got)
	}
}

func TestDeleteReleasesInstance(t *testing.T) {
	now := time.Now()
	n := newNode(t, Options{Limits: defaultLimits()}, &now)
	id := n.mustCreate(`{"capabilities":{"fs":{}}}`)
	if err := n.delete(id); err != nil {
		t.Fatal(err)
	}
	if n.m.Count() != 0 {
		t.Fatalf("instance still live after delete")
	}
	if err := n.delete(id); !errors.Is(err, runtime.ErrNotFound) {
		t.Fatalf("second delete: expected ErrNotFound, got %v", err)
	}
}
