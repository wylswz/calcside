package extensions

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"calcside/cmd/calcside/internal/api/intgen"
	"calcside/internal/runtime"
	"calcside/internal/runtime/remote"
)

// ExtTreeHandler serves local extension source trees to worker nodes,
// routed by the generated api-callback server and authenticated with
// the same shared key as the runtime protocol.
func ExtTreeHandler(localRoots []string, key []byte) http.Handler {
	return CallbackHandler(localRoots, key, nil)
}

// CallbackHandler serves the api-callback surface: local ext trees plus
// the node-control ops when ctl is configured.
func CallbackHandler(localRoots []string, key []byte, ctl runtime.NodeControl) http.Handler {
	r := gin.New()
	r.Use(gin.Recovery(), remote.AuthMiddleware(key))
	intgen.RegisterHandlers(r, intgen.NewStrictHandlerWithOptions(extTrees{roots: localRoots, control: ctl},
		nil,
		intgen.StrictGinServerOptions{
			RequestErrorHandlerFunc: func(ctx *gin.Context, err error) {
				ctx.JSON(http.StatusBadRequest, errorEnvelope("bad_spec", "request body invalid"))
			},
			HandlerErrorFunc: func(ctx *gin.Context, err error) {
				ctx.JSON(http.StatusInternalServerError, errorEnvelope("internal", "control request failed"))
			},
		}))
	return r
}

// extTrees implements the generated strict interface.
type extTrees struct {
	roots   []string
	control runtime.NodeControl
}

var _ intgen.StrictServerInterface = extTrees{}

func (s extTrees) ExtTree(_ context.Context, req intgen.ExtTreeRequestObject) (intgen.ExtTreeResponseObject, error) {
	files, sum, err := ReadLocalTree(s.roots, req.Params.Path)
	if err != nil {
		return intgen.ExtTreedefaultJSONResponse{Body: errEnv(err), StatusCode: http.StatusBadRequest}, nil
	}
	return intgen.ExtTree200JSONResponse{Sum: sum, Files: files}, nil
}

func errEnv(err error) intgen.ErrorEnvelope {
	return errorEnvelope("internal", err.Error())
}

// nodeID returns the authenticated caller identity set by the auth
// middleware and rejects a body claiming a different node.
func nodeID(c context.Context, body string) (context.Context, error) {
	g, ok := c.(*gin.Context)
	if !ok {
		return c, nil
	}
	caller := g.GetString("caller_node")
	if caller == "" || caller != body {
		return nil, runtime.ErrNotOwner
	}
	return g.Request.Context(), nil
}

// ctlErr maps control errors onto the error envelope kinds.
func ctlErr(err error) intgen.ErrorEnvelope {
	switch {
	case errors.Is(err, runtime.ErrStaleEpoch):
		return errorEnvelope("stale_epoch", "stale fence")
	case errors.Is(err, runtime.ErrNotOwner):
		return errorEnvelope("not_owner", "forbidden")
	case errors.Is(err, runtime.ErrNotFound):
		return errorEnvelope("not_found", "not found")
	case errors.Is(err, runtime.ErrBadSpec):
		return errorEnvelope("bad_spec", "invalid request")
	case errors.Is(err, runtime.ErrTooMany):
		return errorEnvelope("too_many", "capacity")
	default:
		return errorEnvelope("internal", "control request failed")
	}
}

func ctlStatus(err error) int {
	switch {
	case errors.Is(err, runtime.ErrStaleEpoch):
		return http.StatusConflict
	case errors.Is(err, runtime.ErrNotOwner):
		return http.StatusForbidden
	case errors.Is(err, runtime.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, runtime.ErrBadSpec):
		return http.StatusBadRequest
	case errors.Is(err, runtime.ErrTooMany):
		return http.StatusTooManyRequests
	default:
		return http.StatusInternalServerError
	}
}

var errUnconfigured = errors.New("unconfigured")

func (s extTrees) ctl() (runtime.NodeControl, error) {
	if s.control == nil {
		return nil, errUnconfigured
	}
	return s.control, nil
}

func (s extTrees) NodeRegister(ctx context.Context, req intgen.NodeRegisterRequestObject) (intgen.NodeRegisterResponseObject, error) {
	ctl, err := s.ctl()
	var rctx context.Context
	if err == nil {
		rctx, err = nodeID(ctx, req.Body.NodeID)
	}
	var resp *runtime.NodeRegisterResponse
	if err == nil {
		resp, err = ctl.RegisterNode(rctx, req.Body)
	}
	if err != nil {
		code := ctlStatus(err)
		if err.Error() == "unconfigured" {
			code = http.StatusServiceUnavailable
		}
		return intgen.NodeRegisterdefaultJSONResponse{Body: ctlErr(err), StatusCode: code}, nil
	}
	return intgen.NodeRegister200JSONResponse(*resp), nil
}

func (s extTrees) NodeSync(ctx context.Context, req intgen.NodeSyncRequestObject) (intgen.NodeSyncResponseObject, error) {
	ctl, err := s.ctl()
	var rctx context.Context
	if err == nil {
		rctx, err = nodeID(ctx, req.Body.Node.NodeID)
	}
	var resp *runtime.NodeSyncResponse
	if err == nil {
		resp, err = ctl.SyncNode(rctx, req.Body)
	}
	if err != nil {
		code := ctlStatus(err)
		if err.Error() == "unconfigured" {
			code = http.StatusServiceUnavailable
		}
		return intgen.NodeSyncdefaultJSONResponse{Body: ctlErr(err), StatusCode: code}, nil
	}
	return intgen.NodeSync200JSONResponse(*resp), nil
}

func (s extTrees) NodePrepare(ctx context.Context, req intgen.NodePrepareRequestObject) (intgen.NodePrepareResponseObject, error) {
	ctl, err := s.ctl()
	var rctx context.Context
	if err == nil {
		rctx, err = nodeID(ctx, req.Body.Node.NodeID)
	}
	var resp *runtime.NodePrepareResponse
	if err == nil {
		resp, err = ctl.PrepareReplica(rctx, req.Body)
	}
	if err != nil {
		code := ctlStatus(err)
		if err.Error() == "unconfigured" {
			code = http.StatusServiceUnavailable
		}
		return intgen.NodePreparedefaultJSONResponse{Body: ctlErr(err), StatusCode: code}, nil
	}
	return intgen.NodePrepare200JSONResponse(*resp), nil
}

func errorEnvelope(kind, message string) intgen.ErrorEnvelope {
	var e intgen.ErrorEnvelope
	e.Error.Kind = intgen.ErrorEnvelopeErrorKind(kind)
	e.Error.Message = message
	return e
}
