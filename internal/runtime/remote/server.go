package remote

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"calcside/internal/runtime"
	"calcside/internal/runtime/remote/gen"
)

// NewHandler serves the runtime contract over authenticated HTTP, with
// routes generated from api/worker.openapi.yaml. It owns no placement
// or persistence knowledge — it authenticates the caller node and
// delegates to the Runtime it wraps (typically *instance.Manager),
// which re-checks owner and epoch itself.
//
// key is the shared API↔worker secret; an empty key rejects every
// request — a worker without it is unreachable, not open.
func NewHandler(rt runtime.Runtime, key []byte) http.Handler {
	r := gin.New()
	r.Use(gin.Recovery(), AuthMiddleware(key))
	gen.RegisterHandlers(r, gen.NewStrictHandlerWithOptions(strict{rt: rt},
		nil,
		gen.StrictGinServerOptions{
			RequestErrorHandlerFunc: func(ctx *gin.Context, err error) {
				writeErrStatus(ctx, http.StatusBadRequest, "bad_spec", err.Error())
			},
			HandlerErrorFunc: func(ctx *gin.Context, err error) {
				writeErr(ctx, err)
			},
		}))
	return r
}

// AuthMiddleware is the gin middleware enforcing the shared-key HMAC
// scheme on every route behind it. The body is read for the signature
// and then restored so binding still sees it. Exported so the API tier
// can protect the endpoints it serves back to workers with the same
// scheme.
func AuthMiddleware(key []byte) gin.HandlerFunc {
	return func(c *gin.Context) {
		if len(key) == 0 {
			writeErrStatus(c, http.StatusForbidden, "internal", "node auth not configured")
			c.Abort()
			return
		}
		body, err := io.ReadAll(io.LimitReader(c.Request.Body, 4<<20))
		if err != nil {
			writeErrStatus(c, http.StatusBadRequest, "internal", "read body")
			c.Abort()
			return
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(body))
		if err := VerifyRequest(key, c.Request, body, time.Now()); err != nil {
			writeErrStatus(c, http.StatusUnauthorized, "internal", err.Error())
			c.Abort()
			return
		}
		c.Set("caller_node", c.Request.Header.Get(headerNode))
		c.Next()
	}
}

// strict adapts runtime.Runtime onto the generated strict interface.
// Generated body types are aliases of the contract types, so adaptation
// is a pass-through plus error-envelope mapping.
type strict struct {
	rt runtime.Runtime
}

var _ gen.StrictServerInterface = strict{}

func envelope(err error) gen.ErrorEnvelope {
	var e gen.ErrorEnvelope
	e.Error.Kind = gen.ErrorEnvelopeErrorKind(kindOf(err))
	e.Error.Message = err.Error()
	return e
}

func (s strict) RuntimeCreate(ctx context.Context, req gen.RuntimeCreateRequestObject) (gen.RuntimeCreateResponseObject, error) {
	resp, err := s.rt.Create(ctx, req.Body)
	if err != nil {
		return gen.RuntimeCreatedefaultJSONResponse{Body: envelope(err), StatusCode: statusOf(err)}, nil
	}
	return gen.RuntimeCreate200JSONResponse(*resp), nil
}

func (s strict) RuntimeExec(ctx context.Context, req gen.RuntimeExecRequestObject) (gen.RuntimeExecResponseObject, error) {
	resp, err := s.rt.Exec(ctx, req.Body)
	if err != nil {
		return gen.RuntimeExecdefaultJSONResponse{Body: envelope(err), StatusCode: statusOf(err)}, nil
	}
	return gen.RuntimeExec200JSONResponse(*resp), nil
}

func (s strict) RuntimeKeepalive(ctx context.Context, req gen.RuntimeKeepaliveRequestObject) (gen.RuntimeKeepaliveResponseObject, error) {
	resp, err := s.rt.Keepalive(ctx, req.Body)
	if err != nil {
		return gen.RuntimeKeepalivedefaultJSONResponse{Body: envelope(err), StatusCode: statusOf(err)}, nil
	}
	return gen.RuntimeKeepalive200JSONResponse(*resp), nil
}

func (s strict) RuntimeDelete(ctx context.Context, req gen.RuntimeDeleteRequestObject) (gen.RuntimeDeleteResponseObject, error) {
	resp, err := s.rt.Delete(ctx, req.Body)
	if err != nil {
		return gen.RuntimeDeletedefaultJSONResponse{Body: envelope(err), StatusCode: statusOf(err)}, nil
	}
	return gen.RuntimeDelete200JSONResponse(*resp), nil
}

func (s strict) RuntimeBrowse(ctx context.Context, req gen.RuntimeBrowseRequestObject) (gen.RuntimeBrowseResponseObject, error) {
	resp, err := s.rt.Browse(ctx, req.Body)
	if err != nil {
		return gen.RuntimeBrowsedefaultJSONResponse{Body: envelope(err), StatusCode: statusOf(err)}, nil
	}
	return gen.RuntimeBrowse200JSONResponse(*resp), nil
}

func (s strict) RuntimePrompt(ctx context.Context, req gen.RuntimePromptRequestObject) (gen.RuntimePromptResponseObject, error) {
	resp, err := s.rt.Prompt(ctx, req.Body)
	if err != nil {
		return gen.RuntimePromptdefaultJSONResponse{Body: envelope(err), StatusCode: statusOf(err)}, nil
	}
	return gen.RuntimePrompt200JSONResponse(*resp), nil
}

func (s strict) RuntimeInspect(ctx context.Context, req gen.RuntimeInspectRequestObject) (gen.RuntimeInspectResponseObject, error) {
	resp, err := s.rt.Inspect(ctx, req.Body)
	if err != nil {
		return gen.RuntimeInspectdefaultJSONResponse{Body: envelope(err), StatusCode: statusOf(err)}, nil
	}
	return gen.RuntimeInspect200JSONResponse(*resp), nil
}

func writeErr(c *gin.Context, err error) {
	writeErrStatus(c, statusOf(err), kindOf(err), err.Error())
}

func writeErrStatus(c *gin.Context, status int, kind, msg string) {
	if status >= 500 {
		slog.Warn("runtime request failed", "err", msg)
	}
	c.JSON(status, envelopeFor(kind, msg))
}

func envelopeFor(kind, msg string) gen.ErrorEnvelope {
	var e gen.ErrorEnvelope
	e.Error.Kind = gen.ErrorEnvelopeErrorKind(kind)
	e.Error.Message = msg
	return e
}
