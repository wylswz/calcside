package worker

import (
	"context"
	"sync/atomic"

	"calcside/internal/placement"
	"calcside/internal/runtime"
)

// testPlacement is the scheduler fixture for transport tests: it always
// picks the first configured node and resolves through the supplied
// binding map.
type testPlacement struct {
	nodes      []placement.NodeRef
	nodesCalls *atomic.Int64
	pickErr    error
	resolve    func(context.Context, string) (*placement.Binding, error)
}

func (p *testPlacement) Pick(context.Context) (placement.NodeRef, error) {
	if p.nodesCalls != nil {
		p.nodesCalls.Add(1)
	}
	if p.pickErr != nil {
		return placement.NodeRef{}, p.pickErr
	}
	if len(p.nodes) == 0 {
		return placement.NodeRef{}, runtime.Errf(runtime.ErrTooMany, "no live worker nodes")
	}
	return p.nodes[0], nil
}

func (p *testPlacement) Nodes(context.Context) ([]placement.NodeRef, error) {
	if p.nodesCalls != nil {
		p.nodesCalls.Add(1)
	}
	return p.nodes, nil
}

func (p *testPlacement) Resolve(ctx context.Context, instanceID string) (*placement.Binding, error) {
	if p.resolve != nil {
		return p.resolve(ctx, instanceID)
	}
	return nil, placement.ErrNotBound
}
