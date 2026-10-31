package worker

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"calcside/internal/placement"
	"calcside/internal/runtime/remote"
)

// router owns the runtime half of Client: which node owns an instance,
// and the remote.Direct bound to each node address. Fresh placement is
// delegated to Picker; this type only caches and forwards.
type router struct {
	picker  Picker
	nodes   func(ctx context.Context) ([]placement.NodeRef, error)
	resolve func(ctx context.Context, instanceID string) (*placement.Binding, error)
	http    func() *http.Client
	sign    func() (key []byte, callerNode string)

	mu      sync.Mutex
	routes  map[string]routeEnt
	directs map[string]*remote.Direct // per node addr
}

type routeEnt struct {
	nodeID string
	addr   string
}

// pick selects a live node for a fresh Create/Prepare placement.
func (r *router) pick(ctx context.Context) (placement.NodeRef, *remote.Direct, error) {
	node, err := r.picker.Pick(ctx)
	if err != nil {
		return placement.NodeRef{}, nil, err
	}
	return node, r.forNode(node.Addr), nil
}

// routed resolves the Direct for an instance, hitting the local route
// cache first and the authoritative binding second. The request itself
// already carries the fencing epoch — the API tier reads it off the
// instance row — so routing only needs the node address.
func (r *router) routed(ctx context.Context, instanceID string) (*remote.Direct, error) {
	r.mu.Lock()
	rt, ok := r.routes[instanceID]
	r.mu.Unlock()
	if ok {
		return r.forNode(rt.addr), nil
	}
	b, err := r.resolve(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	addr, err := r.nodeAddr(ctx, b.NodeID)
	if err != nil {
		return nil, err
	}
	r.bind(instanceID, b.NodeID, addr)
	return r.forNode(addr), nil
}

// bind records the node a successful Create/Prepare placed the
// instance on.
func (r *router) bind(instanceID, nodeID, addr string) {
	r.mu.Lock()
	r.routes[instanceID] = routeEnt{nodeID: nodeID, addr: addr}
	r.mu.Unlock()
}

func (r *router) drop(instanceID string) {
	r.mu.Lock()
	delete(r.routes, instanceID)
	r.mu.Unlock()
}

func (r *router) nodeAddr(ctx context.Context, nodeID string) (string, error) {
	nodes, err := r.nodes(ctx)
	if err != nil {
		return "", err
	}
	for _, n := range nodes {
		if n.NodeID == nodeID {
			return n.Addr, nil
		}
	}
	return "", fmt.Errorf("remote: node %s not live", nodeID)
}

// forNode returns the Direct for a node address, creating it on first
// use. Signing reads key/caller lazily so the pool stays valid if the
// Client's fields are swapped after construction.
func (r *router) forNode(addr string) *remote.Direct {
	r.mu.Lock()
	defer r.mu.Unlock()
	if d, ok := r.directs[addr]; ok {
		return d
	}
	key, node := r.sign()
	d, err := remote.NewDirect("http://"+addr, r.http(), key, node)
	if err != nil {
		// Only reachable on a malformed base URL; fall back to a client
		// that errors at call time rather than panicking here.
		d, _ = remote.NewDirect("http://invalid.invalid", r.http(), key, node)
	}
	r.directs[addr] = d
	return d
}
