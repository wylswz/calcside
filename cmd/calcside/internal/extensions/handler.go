package extensions

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"calcside/cmd/calcside/internal/api/intgen"
	"calcside/internal/runtime/remote"
)

// ExtTreeHandler serves local extension source trees to worker nodes,
// routed by the generated api-callback server and authenticated with
// the same shared key as the runtime protocol.
func ExtTreeHandler(localRoots []string, key []byte) http.Handler {
	r := gin.New()
	r.Use(gin.Recovery(), remote.AuthMiddleware(key))
	intgen.RegisterHandlers(r, intgen.NewStrictHandlerWithOptions(extTrees{roots: localRoots},
		nil,
		intgen.StrictGinServerOptions{
			RequestErrorHandlerFunc: func(ctx *gin.Context, err error) {
				ctx.JSON(http.StatusBadRequest, errorEnvelope("bad_spec", err.Error()))
			},
			HandlerErrorFunc: func(ctx *gin.Context, err error) {
				slog.Warn("runtime request failed", "err", err.Error())
				ctx.JSON(http.StatusInternalServerError, errEnv(err))
			},
		}))
	return r
}

// extTrees implements the generated strict interface.
type extTrees struct{ roots []string }

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

func errorEnvelope(kind, message string) intgen.ErrorEnvelope {
	var e intgen.ErrorEnvelope
	e.Error.Kind = intgen.ErrorEnvelopeErrorKind(kind)
	e.Error.Message = message
	return e
}
