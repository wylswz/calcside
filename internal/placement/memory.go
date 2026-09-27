package placement

import (
	"context"
	"sync"
	"time"
)

// Memory is a process-local Registry for dev mode, the single-binary
// topology, and tests. Liveness is real (heartbeats expire) but dies
// with the process — which is correct, because the instances bound here
// die with it too.
type Memory struct {
	mu       sync.Mutex
	bindings map[string]Binding
	live     map[string]liveNode
	now      func() time.Time
}

type liveNode struct {
	ref       NodeRef
	expiresAt time.Time
}

func NewMemory() *Memory {
	return &Memory{bindings: map[string]Binding{}, live: map[string]liveNode{}, now: time.Now}
}

// SetNow overrides the clock; for tests.
func (m *Memory) SetNow(now func() time.Time) { m.now = now }

func (m *Memory) Bind(_ context.Context, instanceID, nodeID string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.bindings[instanceID]
	if ok && b.NodeID != nodeID {
		return 0, ErrConflict
	}
	b = Binding{InstanceID: instanceID, NodeID: nodeID, Epoch: b.Epoch + 1}
	m.bindings[instanceID] = b
	return b.Epoch, nil
}

func (m *Memory) Lookup(_ context.Context, instanceID string) (*Binding, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.bindings[instanceID]
	if !ok {
		return nil, ErrNotBound
	}
	return &b, nil
}

func (m *Memory) Release(_ context.Context, instanceID, nodeID string, epoch int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.bindings[instanceID]
	if !ok {
		return ErrNotBound
	}
	if b.Epoch != epoch {
		return ErrStaleEpoch
	}
	if b.NodeID != nodeID {
		return ErrConflict
	}
	delete(m.bindings, instanceID)
	return nil
}

func (m *Memory) Heartbeat(_ context.Context, node NodeRef, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.live[node.NodeID] = liveNode{ref: node, expiresAt: m.now().Add(ttl)}
	return nil
}

func (m *Memory) Nodes(_ context.Context) ([]NodeRef, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	out := make([]NodeRef, 0, len(m.live))
	for id, n := range m.live {
		if now.Before(n.expiresAt) {
			out = append(out, n.ref)
		} else {
			delete(m.live, id)
		}
	}
	return out, nil
}
