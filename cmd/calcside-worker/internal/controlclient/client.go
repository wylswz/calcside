// Package controlclient is the worker's HTTP adapter for the
// api-callback node-control surface. It signs every call with the
// shared node key, bounds response reads before parsing, and maps
// typed error kinds onto runtime sentinels. No retry — the agent's
// outer poll loop owns that policy.
package controlclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"calcside/cmd/calcside-worker/internal/apiclient"
	"calcside/internal/runtime"
	"calcside/internal/runtime/remote"
)

const (
	maxRegisterBody = 1 << 20
	maxSyncBody     = 1 << 20
	maxPrepareBody  = 16 << 20
)

var errTransport = errors.New("controlclient: transport failed")

// Client implements runtime.NodeControl over the generated low-level
// client so the body size is bounded before any JSON parse.
type Client struct {
	api *apiclient.Client
}

var _ runtime.NodeControl = (*Client)(nil)

// New validates the base URL — http(s), a real host, no userinfo or
// query — and wires the HMAC sign editor.
func New(baseURL, nodeID string, key []byte, hc *http.Client) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") ||
		u.Host == "" || u.User != nil || u.RawQuery != "" ||
		u.ForceQuery || u.Fragment != "" {
		return nil, fmt.Errorf("%w: invalid base url", errTransport)
	}
	if nodeID == "" || len(nodeID) > 128 || len(key) == 0 {
		return nil, fmt.Errorf("%w: node id and key required", errTransport)
	}
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	// The remote and apiclient editor types share the same signature.
	editor := apiclient.RequestEditorFn(remote.SignEditor(key, nodeID, time.Now))
	api, err := apiclient.NewClient(baseURL,
		apiclient.WithHTTPClient(hc), apiclient.WithRequestEditorFn(editor))
	if err != nil {
		return nil, errTransport
	}
	return &Client{api: api}, nil
}

// call bounds the body read at max+1, decodes the success type, and
// maps the error envelope kind — never echoing response text.
func call[Req, Resp any](ctx context.Context, do func(context.Context, Req) (*http.Response, error), req Req, max int64) (*Resp, error) {
	hresp, err := do(ctx, req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errTransport
	}
	defer hresp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(hresp.Body, max+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errTransport
	}
	if int64(len(body)) > max {
		return nil, errTransport
	}
	if hresp.StatusCode == http.StatusOK {
		// Success must be a JSON object — a null/scalar/zero struct
		// would look like an authoritative empty payload.
		var out Resp
		trimmed := bytes.TrimSpace(body)
		if len(trimmed) == 0 || trimmed[0] != '{' {
			return nil, errTransport
		}
		if err := json.Unmarshal(body, &out); err != nil {
			return nil, errTransport
		}
		return &out, nil
	}
	var env apiclient.ErrorEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, errTransport
	}
	// Kind must match the status it claims — a 500 wearing a fence
	// kind is a transport failure, not authoritative.
	switch {
	case hresp.StatusCode == http.StatusConflict && env.Error.Kind == apiclient.StaleEpoch:
		return nil, runtime.ErrStaleEpoch
	case hresp.StatusCode == http.StatusForbidden && env.Error.Kind == apiclient.NotOwner:
		return nil, runtime.ErrNotOwner
	case hresp.StatusCode == http.StatusBadRequest && env.Error.Kind == apiclient.BadSpec:
		return nil, runtime.ErrBadSpec
	case hresp.StatusCode == http.StatusNotFound && env.Error.Kind == apiclient.NotFound:
		return nil, runtime.ErrNotFound
	case hresp.StatusCode == http.StatusTooManyRequests && env.Error.Kind == apiclient.TooMany:
		return nil, runtime.ErrTooMany
	default:
		return nil, errTransport
	}
}

// RegisterNode performs one signed registration call.
func (c *Client) RegisterNode(ctx context.Context, req *runtime.NodeRegisterRequest) (*runtime.NodeRegisterResponse, error) {
	return call[*runtime.NodeRegisterRequest, runtime.NodeRegisterResponse](ctx,
		func(ctx context.Context, r *runtime.NodeRegisterRequest) (*http.Response, error) {
			return c.api.NodeRegister(ctx, *r)
		}, req, maxRegisterBody)
}

// SyncNode performs one signed sync call — heartbeat, reports and
// inventory in; authoritative assignments out.
func (c *Client) SyncNode(ctx context.Context, req *runtime.NodeSyncRequest) (*runtime.NodeSyncResponse, error) {
	return call[*runtime.NodeSyncRequest, runtime.NodeSyncResponse](ctx,
		func(ctx context.Context, r *runtime.NodeSyncRequest) (*http.Response, error) {
			return c.api.NodeSync(ctx, *r)
		}, req, maxSyncBody)
}

// PrepareReplica fetches the self-contained prepare payload for one
// fenced replica — bounded at the wire's 16 MiB.
func (c *Client) PrepareReplica(ctx context.Context, req *runtime.NodePrepareRequest) (*runtime.NodePrepareResponse, error) {
	return call[*runtime.NodePrepareRequest, runtime.NodePrepareResponse](ctx,
		func(ctx context.Context, r *runtime.NodePrepareRequest) (*http.Response, error) {
			return c.api.NodePrepare(ctx, *r)
		}, req, maxPrepareBody)
}
