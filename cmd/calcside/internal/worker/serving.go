package worker

import (
	"context"

	"calcside/internal/runtime"
	"calcside/internal/runtime/remote"
)

// serving.go routes the EP-0014 serving ops. Prepare picks one live
// node exactly like Create and caches the route only on a ready
// result; Invoke resolves the cached route once per call. Neither op
// retries and neither dedups invoke IDs.

var _ runtime.ServingRuntime = (*Client)(nil)

// Prepare places an ephemeral replica on one live worker. The route is
// cached only when the prepare succeeded; a known failure result
// returns its audit without creating a binding.
func (c *Client) Prepare(ctx context.Context, req *runtime.PrepareRequest) (*runtime.PrepareResponse, error) {
	nodes, err := c.Nodes(ctx)
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		return nil, runtime.Errf(runtime.ErrTooMany, "no live worker nodes")
	}
	node := nodes[c.rr.Add(1)%uint64(len(nodes))]
	req.Epoch = 1
	resp, err := c.forNode(node.Addr).RuntimePrepareWithResponse(ctx, *req)
	if err != nil {
		return nil, err
	}
	out, err := remote.UnwrapResponse(resp.JSON200, resp.JSONDefault, resp.HTTPResponse)
	if err != nil {
		return nil, err
	}
	out.NodeID, out.Epoch = node.NodeID, req.Epoch
	if out.Result.Error == nil {
		c.mu.Lock()
		c.routes[req.InstanceID] = routeEnt{nodeID: node.NodeID, addr: node.Addr}
		c.mu.Unlock()
	}
	return out, nil
}

// Invoke forwards one fenced call through the cached binding. The
// request's release/generation/epoch fence travels untouched; a dead
// route is dropped for future calls but never retried within this one.
func (c *Client) Invoke(ctx context.Context, req *runtime.InvokeRequest) (*runtime.InvokeResponse, error) {
	rt, err := c.routed(ctx, req.InstanceID)
	if err != nil {
		return nil, err
	}
	resp, err := c.forNode(rt.addr).RuntimeInvokeWithResponse(ctx, *req)
	if err != nil {
		return nil, err
	}
	out, err := remote.UnwrapResponse(resp.JSON200, resp.JSONDefault, resp.HTTPResponse)
	if err != nil {
		c.dropIfDead(req.InstanceID, err)
		return nil, err
	}
	return out, nil
}
