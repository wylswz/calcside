// Package placement owns the authoritative instance→node mapping used
// to route execution-dependent requests to the worker holding a live
// instance. The interface is deliberately small and free of Redis,
// SQL, or transport concerns: Bind/Lookup/Release track ownership with
// a fencing epoch, while Heartbeat/Nodes track node liveness.
//
// Two identity layers matter: NodeID is stable across restarts (a
// configured or persisted identity), BootID identifies one process
// incarnation. A restart keeps the NodeID — its bindings still point at
// it — but a changed BootID tells the control plane every instance that
// node held is dead.
package placement

import (
	"context"
	"errors"
	"time"
)

// NodeRef is a live execution node as seen by the control plane.
type NodeRef struct {
	NodeID string
	BootID string
	Addr   string
}

// Binding is the routing fact for one instance.
type Binding struct {
	InstanceID string
	NodeID     string
	Epoch      int64
}

var (
	// ErrNotBound means no node owns the instance — it was never
	// placed, or its binding was released.
	ErrNotBound = errors.New("placement: instance not bound")
	// ErrConflict means another node holds the binding already.
	ErrConflict = errors.New("placement: instance bound to another node")
	// ErrStaleEpoch means the caller's epoch no longer matches the
	// binding — a zombie holder trying to act on a newer claim.
	ErrStaleEpoch = errors.New("placement: stale epoch")
)

// Registry is the placement port. The Postgres implementation treats
// the instances row (node_id, lease_epoch) as the source of truth for
// Bind/Lookup/Release; node liveness is a separate concern (Redis TTL
// keys, or in-memory for dev and tests).
type Registry interface {
	// Bind claims an unbound instance for nodeID — or re-affirms the
	// same node's claim — returning the new fencing epoch.
	Bind(ctx context.Context, instanceID, nodeID string) (int64, error)
	// Lookup resolves where an instance lives.
	Lookup(ctx context.Context, instanceID string) (*Binding, error)
	// Release drops a binding, refusing stale node/epoch pairs.
	Release(ctx context.Context, instanceID, nodeID string, epoch int64) error
	// Heartbeat refreshes a node's liveness for ttl.
	Heartbeat(ctx context.Context, node NodeRef, ttl time.Duration) error
	// Nodes lists nodes whose liveness has not expired.
	Nodes(ctx context.Context) ([]NodeRef, error)
}
