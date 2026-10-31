package worker

import (
	"context"
	"errors"
	"net/http"

	"calcside/internal/placement"
	"calcside/internal/runtime"
	"calcside/internal/runtime/remote"
)

// Client is a runtime.Runtime that forwards each operation to the node
// owning the instance. The wire is remote.Direct — the same signed
// protocol the supervisor speaks to instance children — so this file
// holds only the contract's per-method semantics: placement decisions
// live in the router, retry rules live here.
//
// Retry semantics follow the contract: only Keepalive, Browse, Prompt,
// Inspect and Export re-resolve and retry once on a stale route;
// Create, Exec, Delete, Prepare and Invoke carry external side effects
// and fail through.
type Client struct {
	// Key is the shared API↔worker HMAC secret. Required.
	Key []byte
	// NodeID identifies this API node in request signatures.
	NodeID string
	// Nodes lists live workers; used to pick a Create target and to map
	// a bound node_id to its address.
	Nodes func(ctx context.Context) ([]placement.NodeRef, error)
	// Resolve returns the authoritative binding for an instance —
	// typically the node_id/lease_epoch columns on its row. Used on
	// route-cache misses and to re-resolve after invalidation.
	Resolve func(ctx context.Context, instanceID string) (*placement.Binding, error)
	HTTP    *http.Client

	router *router
}

var _ runtime.Runtime = (*Client)(nil)

func NewClient(key []byte, nodeID string, nodes func(context.Context) ([]placement.NodeRef, error), resolve func(context.Context, string) (*placement.Binding, error)) *Client {
	c := &Client{
		Key: key, NodeID: nodeID, Nodes: nodes, Resolve: resolve,
		HTTP: &http.Client{},
	}
	c.router = &router{
		nodes:   func(ctx context.Context) ([]placement.NodeRef, error) { return c.Nodes(ctx) },
		resolve: func(ctx context.Context, id string) (*placement.Binding, error) { return c.Resolve(ctx, id) },
		http:    func() *http.Client { return c.HTTP },
		sign:    func() ([]byte, string) { return c.Key, c.NodeID },
		routes:  map[string]routeEnt{},
		directs: map[string]*remote.Direct{},
	}
	return c
}

func (c *Client) Create(ctx context.Context, req *runtime.CreateRequest) (*runtime.CreateResponse, error) {
	node, d, err := c.router.pick(ctx)
	if err != nil {
		return nil, err
	}
	// A fresh binding starts at epoch 1; a rebind would bump it.
	req.Epoch = 1
	out, err := d.Create(ctx, req)
	if err != nil {
		return nil, err
	}
	out.NodeID, out.Epoch = node.NodeID, req.Epoch
	c.router.bind(req.InstanceID, node.NodeID, node.Addr)
	return out, nil
}

func (c *Client) Exec(ctx context.Context, req *runtime.ExecRequest) (*runtime.ExecResponse, error) {
	d, err := c.router.routed(ctx, req.InstanceID)
	if err != nil {
		return nil, err
	}
	out, err := d.Exec(ctx, req)
	if err != nil {
		c.dropIfDead(req.InstanceID, err)
		return nil, err
	}
	return out, nil
}

func (c *Client) Keepalive(ctx context.Context, req *runtime.KeepaliveRequest) (*runtime.KeepaliveResponse, error) {
	return retryOnStaleRoute(c, ctx, req.InstanceID, func() (*runtime.KeepaliveResponse, error) {
		d, err := c.router.routed(ctx, req.InstanceID)
		if err != nil {
			return nil, err
		}
		return d.Keepalive(ctx, req)
	})
}

func (c *Client) Delete(ctx context.Context, req *runtime.DeleteRequest) (*runtime.DeleteResponse, error) {
	d, err := c.router.routed(ctx, req.InstanceID)
	if err != nil {
		return nil, err
	}
	out, err := d.Delete(ctx, req)
	// Owner/epoch rejections leave the instance alive; anything the
	// node accepted (or no longer knows) drops the route.
	if err == nil || errors.Is(err, runtime.ErrNotFound) {
		c.router.drop(req.InstanceID)
	}
	return out, err
}

func (c *Client) Browse(ctx context.Context, req *runtime.BrowseRequest) (*runtime.BrowseResponse, error) {
	return retryOnStaleRoute(c, ctx, req.InstanceID, func() (*runtime.BrowseResponse, error) {
		d, err := c.router.routed(ctx, req.InstanceID)
		if err != nil {
			return nil, err
		}
		return d.Browse(ctx, req)
	})
}

func (c *Client) Prompt(ctx context.Context, req *runtime.PromptRequest) (*runtime.PromptResponse, error) {
	return retryOnStaleRoute(c, ctx, req.InstanceID, func() (*runtime.PromptResponse, error) {
		d, err := c.router.routed(ctx, req.InstanceID)
		if err != nil {
			return nil, err
		}
		return d.Prompt(ctx, req)
	})
}

func (c *Client) Inspect(ctx context.Context, req *runtime.InspectRequest) (*runtime.InspectResponse, error) {
	return retryOnStaleRoute(c, ctx, req.InstanceID, func() (*runtime.InspectResponse, error) {
		d, err := c.router.routed(ctx, req.InstanceID)
		if err != nil {
			return nil, err
		}
		return d.Inspect(ctx, req)
	})
}

func (c *Client) Export(ctx context.Context, req *runtime.ExportRequest) (*runtime.ExportResponse, error) {
	return retryOnStaleRoute(c, ctx, req.InstanceID, func() (*runtime.ExportResponse, error) {
		d, err := c.router.routed(ctx, req.InstanceID)
		if err != nil {
			return nil, err
		}
		return d.Export(ctx, req)
	})
}

// retryOnStaleRoute runs fn once; a stale binding or vanished instance
// invalidates the cached route and earns one fresh resolution. Only
// side-effect-free methods use it.
func retryOnStaleRoute[Resp any](c *Client, ctx context.Context, instanceID string, fn func() (*Resp, error)) (*Resp, error) {
	resp, err := fn()
	if errors.Is(err, runtime.ErrStaleEpoch) || errors.Is(err, runtime.ErrNotFound) {
		c.router.drop(instanceID)
		return fn()
	}
	return resp, err
}

// dropIfDead invalidates the cached route when the binding provably
// died — stale epoch, unknown instance — or the node stopped speaking
// our protocol. Transport timeouts keep the route: the instance is
// probably still alive on the node.
func (c *Client) dropIfDead(instanceID string, err error) {
	if errors.Is(err, runtime.ErrStaleEpoch) || errors.Is(err, runtime.ErrNotFound) {
		c.router.drop(instanceID)
	}
}
