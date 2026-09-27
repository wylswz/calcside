package placement

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMemoryBindEpochAndConflict(t *testing.T) {
	ctx := context.Background()
	m := NewMemory()

	e, err := m.Bind(ctx, "ins_1", "node-a")
	if err != nil || e != 1 {
		t.Fatalf("first bind: %v epoch=%d", err, e)
	}
	// Same node re-binds, bumping the epoch.
	e, err = m.Bind(ctx, "ins_1", "node-a")
	if err != nil || e != 2 {
		t.Fatalf("rebind: %v epoch=%d", err, e)
	}
	// Another node cannot steal the binding.
	if _, err := m.Bind(ctx, "ins_1", "node-b"); !errors.Is(err, ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
	// Release requires the exact epoch — a stale holder is refused.
	if err := m.Release(ctx, "ins_1", "node-a", 1); !errors.Is(err, ErrStaleEpoch) {
		t.Fatalf("want ErrStaleEpoch, got %v", err)
	}
	if err := m.Release(ctx, "ins_1", "node-a", 2); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := m.Lookup(ctx, "ins_1"); !errors.Is(err, ErrNotBound) {
		t.Fatalf("want ErrNotBound, got %v", err)
	}
}

func TestMemoryLivenessExpires(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	m := NewMemory()
	m.SetNow(func() time.Time { return now })

	m.Heartbeat(ctx, NodeRef{NodeID: "n1", BootID: "b1", Addr: "10.0.0.1:9"}, time.Minute)
	if nodes, _ := m.Nodes(ctx); len(nodes) != 1 {
		t.Fatalf("expected 1 live node, got %d", len(nodes))
	}
	m.SetNow(func() time.Time { return now.Add(2 * time.Minute) })
	if nodes, _ := m.Nodes(ctx); len(nodes) != 0 {
		t.Fatalf("expected expiry, got %d nodes", len(nodes))
	}
}
