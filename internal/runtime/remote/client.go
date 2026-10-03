package remote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"calcside/internal/placement"
	"calcside/internal/runtime"
	"calcside/internal/runtime/remote/gen"
)

// Client is a runtime.Runtime that forwards each operation to the node
// owning the instance. Placement decisions happen here: Create picks a
// live node and records the binding; the other methods resolve the
// binding and call that node's address. The wire itself is the client
// generated from api/worker.openapi.yaml.
//
// Retry semantics follow the contract: only Keepalive, Browse, Prompt,
// and Inspect re-resolve and retry once on a stale route; Create, Exec,
// and Delete carry external side effects and fail through.
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

	mu      sync.Mutex
	routes  map[string]routeEnt
	clients map[string]*gen.ClientWithResponses // per node addr
	rr      atomic.Uint64
	now     func() time.Time
}

type routeEnt struct {
	nodeID string
	addr   string
}

var _ runtime.Runtime = (*Client)(nil)

func NewClient(key []byte, nodeID string, nodes func(context.Context) ([]placement.NodeRef, error), resolve func(context.Context, string) (*placement.Binding, error)) *Client {
	return &Client{
		Key: key, NodeID: nodeID, Nodes: nodes, Resolve: resolve,
		HTTP:    &http.Client{},
		routes:  map[string]routeEnt{},
		clients: map[string]*gen.ClientWithResponses{},
		now:     time.Now,
	}
}

func (c *Client) Create(ctx context.Context, req *runtime.CreateRequest) (*runtime.CreateResponse, error) {
	nodes, err := c.Nodes(ctx)
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		return nil, runtime.Errf(runtime.ErrTooMany, "no live worker nodes")
	}
	node := nodes[c.rr.Add(1)%uint64(len(nodes))]
	// A fresh binding starts at epoch 1; a rebind would bump it.
	req.Epoch = 1
	resp, err := c.forNode(node.Addr).RuntimeCreateWithResponse(ctx, *req)
	if err != nil {
		return nil, err
	}
	out, err := respOf(resp.JSON200, resp.JSONDefault, resp.HTTPResponse)
	if err != nil {
		return nil, err
	}
	out.NodeID, out.Epoch = node.NodeID, req.Epoch
	c.mu.Lock()
	c.routes[req.InstanceID] = routeEnt{nodeID: node.NodeID, addr: node.Addr}
	c.mu.Unlock()
	return out, nil
}

// routed resolves the node for an instance, hitting the local route
// cache first and the authoritative binding second. The request itself
// already carries the fencing epoch — the API tier reads it off the
// instance row — so routing only needs the node address.
func (c *Client) routed(ctx context.Context, instanceID string) (routeEnt, error) {
	c.mu.Lock()
	rt, ok := c.routes[instanceID]
	c.mu.Unlock()
	if ok {
		return rt, nil
	}
	b, err := c.Resolve(ctx, instanceID)
	if err != nil {
		return routeEnt{}, err
	}
	addr, err := c.nodeAddr(ctx, b.NodeID)
	if err != nil {
		return routeEnt{}, err
	}
	rt = routeEnt{nodeID: b.NodeID, addr: addr}
	c.mu.Lock()
	c.routes[instanceID] = rt
	c.mu.Unlock()
	return rt, nil
}

func (c *Client) nodeAddr(ctx context.Context, nodeID string) (string, error) {
	nodes, err := c.Nodes(ctx)
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

func (c *Client) drop(instanceID string) {
	c.mu.Lock()
	delete(c.routes, instanceID)
	c.mu.Unlock()
}

// forNode returns the generated client for a node address, creating it
// on first use. The signing editor runs per request, so a shared client
// stays valid across time and callers.
func (c *Client) forNode(addr string) *gen.ClientWithResponses {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cl, ok := c.clients[addr]; ok {
		return cl
	}
	cl, err := gen.NewClientWithResponses("http://"+addr,
		gen.WithHTTPClient(c.HTTP),
		gen.WithRequestEditorFn(signEditor(c.Key, c.NodeID, c.now)))
	if err != nil {
		// Only reachable on a malformed base URL; fall back to a client
		// that errors at call time rather than panicking here.
		cl, _ = gen.NewClientWithResponses("http://invalid.invalid", gen.WithHTTPClient(c.HTTP))
	}
	c.clients[addr] = cl
	return cl
}

// signEditor signs the serialized request body per worker.openapi.yaml.
func signEditor(key []byte, callerNode string, now func() time.Time) gen.RequestEditorFn {
	return func(ctx context.Context, req *http.Request) error {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return err
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
		SignRequest(key, callerNode, req, body, now())
		return nil
	}
}

func (c *Client) Exec(ctx context.Context, req *runtime.ExecRequest) (*runtime.ExecResponse, error) {
	rt, err := c.routed(ctx, req.InstanceID)
	if err != nil {
		return nil, err
	}
	resp, err := c.forNode(rt.addr).RuntimeExecWithResponse(ctx, *req)
	if err != nil {
		return nil, err
	}
	out, err := respOf(resp.JSON200, resp.JSONDefault, resp.HTTPResponse)
	if err != nil {
		c.dropIfDead(req.InstanceID, err)
		return nil, err
	}
	return out, nil
}

func (c *Client) Keepalive(ctx context.Context, req *runtime.KeepaliveRequest) (*runtime.KeepaliveResponse, error) {
	return retryOnStaleRoute(c, ctx, req.InstanceID, func() (*runtime.KeepaliveResponse, error) {
		rt, err := c.routed(ctx, req.InstanceID)
		if err != nil {
			return nil, err
		}
		resp, err := c.forNode(rt.addr).RuntimeKeepaliveWithResponse(ctx, *req)
		if err != nil {
			return nil, err
		}
		return respOf(resp.JSON200, resp.JSONDefault, resp.HTTPResponse)
	})
}

func (c *Client) Delete(ctx context.Context, req *runtime.DeleteRequest) (*runtime.DeleteResponse, error) {
	rt, err := c.routed(ctx, req.InstanceID)
	if err != nil {
		return nil, err
	}
	resp, err := c.forNode(rt.addr).RuntimeDeleteWithResponse(ctx, *req)
	if err != nil {
		return nil, err
	}
	out, err := respOf(resp.JSON200, resp.JSONDefault, resp.HTTPResponse)
	if err == nil || errors.Is(err, runtime.ErrNotFound) {
		c.drop(req.InstanceID)
	}
	return out, err
}

func (c *Client) Browse(ctx context.Context, req *runtime.BrowseRequest) (*runtime.BrowseResponse, error) {
	return retryOnStaleRoute(c, ctx, req.InstanceID, func() (*runtime.BrowseResponse, error) {
		rt, err := c.routed(ctx, req.InstanceID)
		if err != nil {
			return nil, err
		}
		resp, err := c.forNode(rt.addr).RuntimeBrowseWithResponse(ctx, *req)
		if err != nil {
			return nil, err
		}
		return respOf(resp.JSON200, resp.JSONDefault, resp.HTTPResponse)
	})
}

func (c *Client) Prompt(ctx context.Context, req *runtime.PromptRequest) (*runtime.PromptResponse, error) {
	return retryOnStaleRoute(c, ctx, req.InstanceID, func() (*runtime.PromptResponse, error) {
		rt, err := c.routed(ctx, req.InstanceID)
		if err != nil {
			return nil, err
		}
		resp, err := c.forNode(rt.addr).RuntimePromptWithResponse(ctx, *req)
		if err != nil {
			return nil, err
		}
		return respOf(resp.JSON200, resp.JSONDefault, resp.HTTPResponse)
	})
}

func (c *Client) Inspect(ctx context.Context, req *runtime.InspectRequest) (*runtime.InspectResponse, error) {
	return retryOnStaleRoute(c, ctx, req.InstanceID, func() (*runtime.InspectResponse, error) {
		rt, err := c.routed(ctx, req.InstanceID)
		if err != nil {
			return nil, err
		}
		resp, err := c.forNode(rt.addr).RuntimeInspectWithResponse(ctx, *req)
		if err != nil {
			return nil, err
		}
		return respOf(resp.JSON200, resp.JSONDefault, resp.HTTPResponse)
	})
}

// retryOnStaleRoute runs fn once; a stale binding or vanished instance
// invalidates the cached route and earns one fresh resolution. Only
// side-effect-free methods use it.
func retryOnStaleRoute[Resp any](c *Client, ctx context.Context, instanceID string, fn func() (*Resp, error)) (*Resp, error) {
	resp, err := fn()
	if errors.Is(err, runtime.ErrStaleEpoch) || errors.Is(err, runtime.ErrNotFound) {
		c.drop(instanceID)
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
		c.drop(instanceID)
	}
}

// respOf unwraps a generated response: success payload, error envelope,
// or a transport-level failure.
func respOf[Resp any](ok *Resp, def *gen.Error, hresp *http.Response) (*Resp, error) {
	if ok != nil {
		return ok, nil
	}
	if def != nil {
		return nil, errorOf(string(def.Error.Kind), def.Error.Message)
	}
	return nil, fmt.Errorf("remote: worker returned %s", hresp.Status)
}
