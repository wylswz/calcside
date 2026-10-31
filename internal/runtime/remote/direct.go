package remote

import (
	"bytes"
	"context"
	"fmt"
	"io"
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
	cl   *gen.ClientWithResponses
	hook LifecycleHook
}

// LifecycleHook observes the side-effecting lifecycle calls a Direct
// sends. Each method runs before the request leaves the process, so a
// call that fails in transport is still observed. Hooks must not mutate
// the request and must be safe for concurrent use.
type LifecycleHook interface {
	Create(ctx context.Context, req *runtime.CreateRequest)
	Prepare(ctx context.Context, req *runtime.PrepareRequest)
	Invoke(ctx context.Context, req *runtime.InvokeRequest)
	Delete(ctx context.Context, req *runtime.DeleteRequest)
}

// SetLifecycleHook installs h to observe outbound Create, Prepare,
// Invoke and Delete calls. Set it before the client is shared.
func (d *Direct) SetLifecycleHook(h LifecycleHook) { d.hook = h }

var _ runtime.Runtime = (*Direct)(nil)
var _ runtime.ServingRuntime = (*Direct)(nil)

// NewDirect builds a Direct client for baseURL. hc carries the dialer
// (e.g. a unix-socket transport); key and callerNode sign each request.
func NewDirect(baseURL string, hc *http.Client, key []byte, callerNode string) (*Direct, error) {
	cl, err := gen.NewClientWithResponses(baseURL,
		gen.WithHTTPClient(hc),
		gen.WithRequestEditorFn(SignEditor(key, callerNode, time.Now)))
	if err != nil {
		return nil, err
	}
	return &Direct{cl: cl}, nil
}

func (d *Direct) Create(ctx context.Context, req *runtime.CreateRequest) (*runtime.CreateResponse, error) {
	if d.hook != nil {
		d.hook.Create(ctx, req)
	}
	resp, err := d.cl.RuntimeCreateWithResponse(ctx, *req)
	if err != nil {
		return nil, err
	}
	return UnwrapResponse(resp.JSON200, resp.JSONDefault, resp.HTTPResponse)
}

func (d *Direct) Exec(ctx context.Context, req *runtime.ExecRequest) (*runtime.ExecResponse, error) {
	resp, err := d.cl.RuntimeExecWithResponse(ctx, *req)
	if err != nil {
		return nil, err
	}
	return UnwrapResponse(resp.JSON200, resp.JSONDefault, resp.HTTPResponse)
}

func (d *Direct) Prepare(ctx context.Context, req *runtime.PrepareRequest) (*runtime.PrepareResponse, error) {
	if d.hook != nil {
		d.hook.Prepare(ctx, req)
	}
	resp, err := d.cl.RuntimePrepareWithResponse(ctx, *req)
	if err != nil {
		return nil, err
	}
	return UnwrapResponse(resp.JSON200, resp.JSONDefault, resp.HTTPResponse)
}

func (d *Direct) Invoke(ctx context.Context, req *runtime.InvokeRequest) (*runtime.InvokeResponse, error) {
	if d.hook != nil {
		d.hook.Invoke(ctx, req)
	}
	resp, err := d.cl.RuntimeInvokeWithResponse(ctx, *req)
	if err != nil {
		return nil, err
	}
	return UnwrapResponse(resp.JSON200, resp.JSONDefault, resp.HTTPResponse)
}

func (d *Direct) Keepalive(ctx context.Context, req *runtime.KeepaliveRequest) (*runtime.KeepaliveResponse, error) {
	resp, err := d.cl.RuntimeKeepaliveWithResponse(ctx, *req)
	if err != nil {
		return nil, err
	}
	return UnwrapResponse(resp.JSON200, resp.JSONDefault, resp.HTTPResponse)
}

func (d *Direct) Delete(ctx context.Context, req *runtime.DeleteRequest) (*runtime.DeleteResponse, error) {
	if d.hook != nil {
		d.hook.Delete(ctx, req)
	}
	resp, err := d.cl.RuntimeDeleteWithResponse(ctx, *req)
	if err != nil {
		return nil, err
	}
	return UnwrapResponse(resp.JSON200, resp.JSONDefault, resp.HTTPResponse)
}

func (d *Direct) Browse(ctx context.Context, req *runtime.BrowseRequest) (*runtime.BrowseResponse, error) {
	resp, err := d.cl.RuntimeBrowseWithResponse(ctx, *req)
	if err != nil {
		return nil, err
	}
	return UnwrapResponse(resp.JSON200, resp.JSONDefault, resp.HTTPResponse)
}

func (d *Direct) Prompt(ctx context.Context, req *runtime.PromptRequest) (*runtime.PromptResponse, error) {
	resp, err := d.cl.RuntimePromptWithResponse(ctx, *req)
	if err != nil {
		return nil, err
	}
	return UnwrapResponse(resp.JSON200, resp.JSONDefault, resp.HTTPResponse)
}

func (d *Direct) Inspect(ctx context.Context, req *runtime.InspectRequest) (*runtime.InspectResponse, error) {
	resp, err := d.cl.RuntimeInspectWithResponse(ctx, *req)
	if err != nil {
		return nil, err
	}
	return UnwrapResponse(resp.JSON200, resp.JSONDefault, resp.HTTPResponse)
}

func (d *Direct) Export(ctx context.Context, req *runtime.ExportRequest) (*runtime.ExportResponse, error) {
	resp, err := d.cl.RuntimeExportWithResponse(ctx, *req)
	if err != nil {
		return nil, err
	}
	return UnwrapResponse(resp.JSON200, resp.JSONDefault, resp.HTTPResponse)
}

// SignEditor signs the serialized request body per worker.openapi.yaml.
func SignEditor(key []byte, callerNode string, now func() time.Time) gen.RequestEditorFn {
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

// UnwrapResponse unwraps a generated response: success payload, error envelope,
// or a transport-level failure.
func UnwrapResponse[Resp any](ok *Resp, def *gen.Error, hresp *http.Response) (*Resp, error) {
	if ok != nil {
		return ok, nil
	}
	if def != nil {
		return nil, errorOf(string(def.Error.Kind), def.Error.Message)
	}
	return nil, fmt.Errorf("remote: worker returned %s", hresp.Status)
}
