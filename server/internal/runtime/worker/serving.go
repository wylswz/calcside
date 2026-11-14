package worker

import (
	"context"

	"calcside/internal/runtime"
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
	node, d, err := c.router.pick(ctx)
	if err != nil {
		return nil, err
	}
	req.Epoch = 1
	out, err := d.Prepare(ctx, req)
	if err != nil {
		return nil, err
	}
	out.NodeID, out.Epoch = node.NodeID, req.Epoch
	if out.Result.Error == nil {
		c.router.bind(req.InstanceID, node.NodeID, node.Addr)
	}
	return out, nil
}

// Invoke forwards one fenced call through the cached binding. The
// request's release/generation/epoch fence travels untouched; a dead
// route is dropped for future calls but never retried within this one.
func (c *Client) Invoke(ctx context.Context, req *runtime.InvokeRequest) (*runtime.InvokeResponse, error) {
	d, err := c.router.routed(ctx, req.InstanceID)
	if err != nil {
		return nil, err
	}
	out, err := d.Invoke(ctx, req)
	if err != nil {
		c.dropIfDead(req.InstanceID, err)
		return nil, err
	}
	return out, nil
}
