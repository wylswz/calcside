// Package api implements the REST API on Gin with handlers generated
// from api/openapi.yaml (see internal/api/gen). Route registration is
// contract-driven; the strict server implementation lives in impl.go.
package api

//go:generate go tool oapi-codegen -config ../../api/oapi-server.yaml ../../api/openapi.yaml

import (
	"encoding/json"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"calcside/internal/api/gen"
	"calcside/internal/auth"
	auditsvc "calcside/internal/service/audit"
	"calcside/internal/service/catalog"
	"calcside/internal/service/iam"
	policysvc "calcside/internal/service/policy"
	"calcside/internal/service/sandbox"
	"calcside/internal/service/vault"
	"calcside/internal/store"
	"calcside/internal/types"
)

const maxBodyBytes = 1 << 20

// Deps wires the API.
type Deps struct {
	IAM     *iam.Service
	Vault   *vault.Service
	Policy  *policysvc.Service
	Audit   *auditsvc.Service
	Catalog *catalog.Service
	Sandbox *sandbox.Service

	Auth          *auth.Service
	Web           fs.FS           // static files served at /; nil for now
	GoogleEnabled bool            // reported by /api/v1/auth/config
	Dev           bool            // anonymous dev mode
	Anonymous     *store.User     // dev-mode anonymous principal's user
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code types.APIErrorCode, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": string(code), "message": msg}})
}

// requestLogger is a slog-based access log (replaces gin's default
// stdout logger).
func requestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		slog.Info("http", "method", c.Request.Method, "path", c.Request.URL.Path,
			"status", c.Writer.Status(), "dur_ms", time.Since(start).Milliseconds())
	}
}

// authMiddleware resolves the principal (bearer > cookie > anonymous in
// dev mode) and enforces CSRF on cookie/anonymous mutations. Anonymous
// principals get the dev anonymous user; requests stay unauthenticated
// when there is no credential and no dev mode.
func (d Deps) authMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		p, err := d.Auth.Resolve(c.Request.Context(), c.Request)
		if err != nil {
			// Dev mode: a stale session cookie from an earlier
			// database is not a credential failure — downgrade to
			// anonymous and clear it. A wrong bearer token is an
			// explicit credential and still 401s.
			if d.Dev && !hasBearer(c.Request) {
				d.Auth.ClearSessionCookie(c.Writer)
			} else {
				writeErr(c.Writer, http.StatusUnauthorized, types.ErrCodeUnauthorized, err.Error())
				c.Abort()
				return
			}
		}
		if p == nil && d.Dev && d.Anonymous != nil {
			p = &auth.Principal{User: d.Anonymous, Kind: types.AuthAnonymous}
		}
		if p != nil && auth.NeedsCSRF(p) &&
			c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
			if !auth.CSRFHeaderOK(c.Request) {
				writeErr(c.Writer, http.StatusForbidden, types.ErrCodeCSRF, "missing X-Requested-With header")
				c.Abort()
				return
			}
		}
		if p != nil {
			c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), p))
		}
		c.Next()
	}
}

// bodyLimit caps mutating request bodies like the old handlers did:
// 1MB everywhere, except /exec which allows the 256KB code plus slack.
func bodyLimit() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method == http.MethodGet || c.Request.Method == http.MethodHead {
			c.Next()
			return
		}
		limit := int64(maxBodyBytes)
		if strings.HasSuffix(c.Request.URL.Path, "/exec") {
			limit = sandbox.MaxCodeBytes + 1024
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
		c.Next()
	}
}

// Handler builds the Gin engine: contract routes via the generated
// strict handler, raw auth routes, static SPA.
func Handler(d Deps) http.Handler {
	// All dependencies are programmer-supplied; a missing one is a
	// wiring bug, so fail fast instead of panicking mid-request.
	for _, dep := range []struct {
		name string
		ok   bool
	}{
		{"IAM", d.IAM != nil},
		{"Vault", d.Vault != nil},
		{"Policy", d.Policy != nil},
		{"Audit", d.Audit != nil},
		{"Catalog", d.Catalog != nil},
		{"Sandbox", d.Sandbox != nil},
		{"Auth", d.Auth != nil},
	} {
		if !dep.ok {
			panic("api: Deps." + dep.name + " is nil")
		}
	}
	if d.Dev {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.New()
	r.HandleMethodNotAllowed = true
	r.Use(gin.Recovery(), requestLogger())

	impl := &strictImpl{d: d}
	strict := gen.NewStrictHandlerWithOptions(impl, nil, gen.StrictGinServerOptions{
		RequestErrorHandlerFunc: func(c *gin.Context, err error) {
			if strings.Contains(err.Error(), "request body too large") {
				writeErr(c.Writer, http.StatusRequestEntityTooLarge, types.ErrCodeTooLarge, "request body too large")
				return
			}
			writeErr(c.Writer, http.StatusBadRequest, types.ErrCodeBadRequest, "invalid JSON body")
		},
		HandlerErrorFunc: func(c *gin.Context, err error) {
			writeErr(c.Writer, http.StatusInternalServerError, types.ErrCodeInternal, err.Error())
		},
		ResponseErrorHandlerFunc: func(c *gin.Context, err error) {
			writeErr(c.Writer, http.StatusInternalServerError, types.ErrCodeInternal, err.Error())
		},
	})

	api := r.Group("/")
	api.Use(d.authMiddleware(), bodyLimit())
	gen.RegisterHandlers(api, strict)

	// auth-raw: session cookie endpoints stay hand-written.
	raw := r.Group("/auth")
	raw.Use(d.resolveOnly())
	raw.POST("/logout", gin.WrapH(d.Auth.LogoutHandler()))

	r.NoRoute(d.noRoute())
	return r
}

// resolveOnly attaches a principal when credentials exist but never
// rejects (used by /auth/logout).
func (d Deps) resolveOnly() gin.HandlerFunc {
	return func(c *gin.Context) {
		p, err := d.Auth.Resolve(c.Request.Context(), c.Request)
		if err == nil && p != nil {
			c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), p))
		}
		c.Next()
	}
}

func hasBearer(r *http.Request) bool {
	return strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ")
}

// noRoute serves JSON errors under /api and the embedded SPA
// everywhere else.
func (d Deps) noRoute() gin.HandlerFunc {
	return func(c *gin.Context) {
		p := c.Request.URL.Path
		if strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/auth/") {
			writeErr(c.Writer, http.StatusNotFound, types.ErrCodeNotFound, "not found")
			return
		}
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
			writeErr(c.Writer, http.StatusMethodNotAllowed, types.ErrCodeMethodNotAllowed, "method not allowed")
			return
		}
		if d.Web == nil {
			writeErr(c.Writer, http.StatusNotFound, types.ErrCodeNotFound, "not found")
			return
		}
		serveSPA(c.Writer, c.Request, d.Web)
	}
}

// serveSPA serves the built web console: real files when present,
// index.html for unknown non-API GET paths (client-side routing), long
// cache for hashed assets, no-cache for index.html.
func serveSPA(w http.ResponseWriter, r *http.Request, web fs.FS) {
	upath := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	serve := func(name string) bool {
		data, err := fs.ReadFile(web, name)
		if err != nil {
			return false
		}
		if name == "index.html" {
			w.Header().Set("Cache-Control", "no-cache")
		} else if strings.HasPrefix(name, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		if ct := mime.TypeByExtension(path.Ext(name)); ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		w.WriteHeader(200)
		_, _ = w.Write(data)
		return true
	}
	if upath != "index.html" {
		if st, err := fs.Stat(web, upath); err == nil && !st.IsDir() {
			if serve(upath) {
				return
			}
		}
	}
	if serve("index.html") {
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(200)
	_, _ = w.Write([]byte("calcside console not built; run `make web`\n"))
}
