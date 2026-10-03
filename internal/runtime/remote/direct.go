package remote

import (
	"context"
	"net/http"
	"time"

	"calcside/internal/runtime"
	"calcside/internal/runtime/remote/gen"
)

// Direct is a runtime.Runtime bound to a single endpoint: no placement,
// no route cache, every call goes to the same node. The worker's
// process supervisor uses it to reach a per-instance child over a unix
// socket, speaking the same signed protocol the API tier speaks to it.
type Direct struct {
	cl *gen.ClientWithResponses
}

var _ runtime.Runtime = (*Direct)(nil)

// NewDirect builds a Direct client for baseURL. hc carries the dialer
// (e.g. a unix-socket transport); key and callerNode sign each request.
func NewDirect(baseURL string, hc *http.Client, key []byte, callerNode string) (*Direct, error) {
	cl, err := gen.NewClientWithResponses(baseURL,
		gen.WithHTTPClient(hc),
		gen.WithRequestEditorFn(signEditor(key, callerNode, time.Now)))
	if err != nil {
		return nil, err
	}
	return &Direct{cl: cl}, nil
}

func (d *Direct) Create(ctx context.Context, req *runtime.CreateRequest) (*runtime.CreateResponse, error) {
	resp, err := d.cl.RuntimeCreateWithResponse(ctx, *req)
	if err != nil {
		return nil, err
	}
	return respOf(resp.JSON200, resp.JSONDefault, resp.HTTPResponse)
}

func (d *Direct) Exec(ctx context.Context, req *runtime.ExecRequest) (*runtime.ExecResponse, error) {
	resp, err := d.cl.RuntimeExecWithResponse(ctx, *req)
	if err != nil {
		return nil, err
	}
	return respOf(resp.JSON200, resp.JSONDefault, resp.HTTPResponse)
}

func (d *Direct) Keepalive(ctx context.Context, req *runtime.KeepaliveRequest) (*runtime.KeepaliveResponse, error) {
	resp, err := d.cl.RuntimeKeepaliveWithResponse(ctx, *req)
	if err != nil {
		return nil, err
	}
	return respOf(resp.JSON200, resp.JSONDefault, resp.HTTPResponse)
}

func (d *Direct) Delete(ctx context.Context, req *runtime.DeleteRequest) (*runtime.DeleteResponse, error) {
	resp, err := d.cl.RuntimeDeleteWithResponse(ctx, *req)
	if err != nil {
		return nil, err
	}
	return respOf(resp.JSON200, resp.JSONDefault, resp.HTTPResponse)
}

func (d *Direct) Browse(ctx context.Context, req *runtime.BrowseRequest) (*runtime.BrowseResponse, error) {
	resp, err := d.cl.RuntimeBrowseWithResponse(ctx, *req)
	if err != nil {
		return nil, err
	}
	return respOf(resp.JSON200, resp.JSONDefault, resp.HTTPResponse)
}

func (d *Direct) Prompt(ctx context.Context, req *runtime.PromptRequest) (*runtime.PromptResponse, error) {
	resp, err := d.cl.RuntimePromptWithResponse(ctx, *req)
	if err != nil {
		return nil, err
	}
	return respOf(resp.JSON200, resp.JSONDefault, resp.HTTPResponse)
}

func (d *Direct) Inspect(ctx context.Context, req *runtime.InspectRequest) (*runtime.InspectResponse, error) {
	resp, err := d.cl.RuntimeInspectWithResponse(ctx, *req)
	if err != nil {
		return nil, err
	}
	return respOf(resp.JSON200, resp.JSONDefault, resp.HTTPResponse)
}
