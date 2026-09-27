package remote

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"calcside/internal/api/intgen"
	capext "calcside/internal/capability/ext"
	"calcside/internal/runtime/remote/apiclient"
)

// ExtTreePath is the API-side endpoint a worker resolves local ext
// sources through — an api-callback operation in worker.openapi.yaml,
// served by the API tier because workers carry no filesystem roots.
const ExtTreePath = "/internal/v1/ext/tree"

// ExtTreeHandler serves local extension source trees to worker nodes,
// routed by the generated api-callback server and authenticated with
// the same shared key as the runtime protocol.
func ExtTreeHandler(localRoots []string, key []byte) http.Handler {
	r := gin.New()
	r.Use(gin.Recovery(), AuthMiddleware(key))
	intgen.RegisterHandlers(r, intgen.NewStrictHandlerWithOptions(extTrees{roots: localRoots},
		nil,
		intgen.StrictGinServerOptions{
			RequestErrorHandlerFunc: func(ctx *gin.Context, err error) {
				writeErrStatus(ctx, http.StatusBadRequest, "bad_spec", err.Error())
			},
			HandlerErrorFunc: func(ctx *gin.Context, err error) {
				writeErr(ctx, err)
			},
		}))
	return r
}

// extTrees implements the generated strict interface.
type extTrees struct{ roots []string }

var _ intgen.StrictServerInterface = extTrees{}

func (s extTrees) ExtTree(_ context.Context, req intgen.ExtTreeRequestObject) (intgen.ExtTreeResponseObject, error) {
	files, sum, err := capext.ReadLocalTree(s.roots, req.Params.Path)
	if err != nil {
		return intgen.ExtTreedefaultJSONResponse{Body: errEnv(err), StatusCode: http.StatusBadRequest}, nil
	}
	return intgen.ExtTree200JSONResponse{Sum: sum, Files: files}, nil
}

func errEnv(err error) intgen.ErrorEnvelope {
	var e intgen.ErrorEnvelope
	e.Error.Kind = intgen.ErrorEnvelopeErrorKind(kindOf(err))
	e.Error.Message = err.Error()
	return e
}

// LocalExtResolver returns a resolver for capext.Options.LocalResolver:
// the worker asks the API tier for the tree and materializes it into
// cacheDir/local/<sha256(path)>/, refetching only when the sum moved.
func LocalExtResolver(apiAddr, nodeID string, key []byte, cacheDir string) func(context.Context, capext.ParsedIdentifier) (string, error) {
	client, err := apiclient.NewClientWithResponses(strings.TrimSuffix(apiAddr, "/"),
		apiclient.WithRequestEditorFn(func(ctx context.Context, req *http.Request) error {
			SignRequest(key, nodeID, req, nil, time.Now())
			return nil
		}))
	if err != nil {
		// A malformed --api-addr makes every resolve fail with a clear
		// error rather than panicking here.
		return func(context.Context, capext.ParsedIdentifier) (string, error) {
			return "", fmt.Errorf("ext: bad api addr %q: %w", apiAddr, err)
		}
	}
	return func(ctx context.Context, p capext.ParsedIdentifier) (string, error) {
		key2 := sha256.Sum256([]byte(p.Local))
		dir := filepath.Join(cacheDir, "local", hex.EncodeToString(key2[:8]))

		resp, err := client.ExtTreeWithResponse(ctx, &apiclient.ExtTreeParams{Path: p.Local})
		if err != nil {
			return "", fmt.Errorf("ext: resolve %q via api: %w", p.Local, err)
		}
		if resp.JSON200 == nil {
			if resp.JSONDefault != nil {
				return "", fmt.Errorf("ext: resolve %q via api: %s", p.Local, resp.JSONDefault.Error.Message)
			}
			return "", fmt.Errorf("ext: resolve %q via api: %s", p.Local, resp.Status())
		}
		if cachedSum(dir) == resp.JSON200.Sum {
			return dir, nil
		}
		if err := writeTree(dir, resp.JSON200.Sum, resp.JSON200.Files); err != nil {
			return "", err
		}
		return dir, nil
	}
}

// cachedSum reads the marker kept next to (never inside) the tree dir,
// so it cannot leak into the extension's own file set.
func cachedSum(dir string) string {
	b, err := os.ReadFile(dir + ".sum")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func writeTree(dir, sum string, files map[string][]byte) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for rel, content := range files {
		clean := filepath.Clean(filepath.FromSlash(rel))
		if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			return fmt.Errorf("ext: tree file %q escapes cache dir", rel)
		}
		dst := filepath.Join(dir, clean)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, content, 0o644); err != nil {
			return err
		}
	}
	return os.WriteFile(dir+".sum", []byte(sum+"\n"), 0o644)
}
