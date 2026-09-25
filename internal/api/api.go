// Package api implements the REST API on the stdlib ServeMux.
package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"go.starlark.net/starlark"

	"calcside/internal/auth"
	"calcside/internal/capability"
	"calcside/internal/engine"
	"calcside/internal/instance"
	"calcside/internal/policy"
	"calcside/internal/secrets"
	"calcside/internal/store"
	"calcside/internal/types"
)

const (
	maxBodyBytes = 1 << 20
	maxCodeBytes = 256 << 10
	snippetBytes = 2 << 10
)

// Deps wires the API.
type Deps struct {
	Store         store.Store
	Manager       *instance.Manager
	Registry      *capability.Registry
	Auth          *auth.Service
	Web           fs.FS           // static files served at /; nil for now
	GoogleEnabled bool            // reported by /api/v1/auth/config
	Cipher        *secrets.Cipher // nil = vault secrets disabled
	Now           func() time.Time
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code types.APIErrorCode, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": string(code), "message": msg}})
}

func decode(w http.ResponseWriter, r *http.Request, v any, limit int64) error {
	return json.NewDecoder(http.MaxBytesReader(w, r.Body, limit)).Decode(v)
}

func principal(r *http.Request) *auth.Principal { return auth.FromContext(r.Context()) }

// Handler builds the full mux: public auth routes, protected API, static.
func Handler(d Deps) http.Handler {
	mux := http.NewServeMux()
	a := &api{d: d}

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true})
	})
	mux.HandleFunc("GET /api/v1/auth/config", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{
			"google":    d.GoogleEnabled,
			"dev_login": d.Auth.DevLoginEnabled(),
			"secrets":   d.Cipher != nil,
		})
	})

	// auth routes
	mux.Handle("POST /auth/dev/login", d.Auth.DevLoginHandler())
	mux.Handle("POST /auth/logout", d.Auth.LogoutHandler())

	// protected API
	api := http.NewServeMux()
	api.HandleFunc("GET /api/v1/me", a.me)
	api.HandleFunc("GET /api/v1/capabilities", a.capabilities)

	api.HandleFunc("POST /api/v1/keys", a.createKey)
	api.HandleFunc("GET /api/v1/keys", a.listKeys)
	api.HandleFunc("DELETE /api/v1/keys/{id}", a.deleteKey)

	api.HandleFunc("POST /api/v1/instances", a.createInstance)
	api.HandleFunc("GET /api/v1/instances", a.listInstances)
	api.HandleFunc("GET /api/v1/instances/{id}", a.getInstance)
	api.HandleFunc("DELETE /api/v1/instances/{id}", a.deleteInstance)
	api.HandleFunc("POST /api/v1/instances/{id}/keepalive", a.keepalive)
	api.HandleFunc("POST /api/v1/instances/{id}/exec", a.exec)
	api.HandleFunc("GET /api/v1/instances/{id}/files", a.files)
	api.HandleFunc("GET /api/v1/instances/{id}/executions", a.executions)

	api.HandleFunc("GET /api/v1/secrets", a.listSecrets)
	api.HandleFunc("POST /api/v1/secrets", a.createSecret)
	api.HandleFunc("PUT /api/v1/secrets/{id}", a.updateSecret)
	api.HandleFunc("DELETE /api/v1/secrets/{id}", a.deleteSecret)

	api.HandleFunc("GET /api/v1/audit", a.auditEvents)

	api.HandleFunc("GET /api/v1/policies", a.listPolicies)
	api.HandleFunc("POST /api/v1/policies", a.createPolicy)
	api.HandleFunc("POST /api/v1/policies/validate", a.validatePolicy)
	api.HandleFunc("GET /api/v1/policies/{id}", a.getPolicy)
	api.HandleFunc("PUT /api/v1/policies/{id}", a.updatePolicy)
	api.HandleFunc("DELETE /api/v1/policies/{id}", a.deletePolicy)

	// /api/v1/keys requires session auth; everything else just needs a
	// principal.
	mux.Handle("/api/", d.Auth.Middleware(auth.RequireAuth(sessionGate(api))))

	if d.Web != nil {
		mux.Handle("/", spaHandler(d.Web))
	}
	return mux
}

// spaHandler serves the built web console: real files when present,
// index.html for unknown non-API GET paths (client-side routing), long
// cache for hashed assets, no-cache for index.html.
func spaHandler(web fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeErr(w, 405, types.ErrCodeMethodNotAllowed, "method not allowed")
			return
		}
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
	})
}

// sessionGate enforces RequireSession for /api/v1/keys* and
// /api/v1/secrets* (API-key auth must not manage keys or widen secrets).
func sessionGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, prefix := range []string{"/api/v1/keys", "/api/v1/secrets"} {
			if r.URL.Path == prefix || strings.HasPrefix(r.URL.Path, prefix+"/") {
				auth.RequireSession(next).ServeHTTP(w, r)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

type api struct{ d Deps }

func (a *api) now() time.Time {
	if a.d.Now != nil {
		return a.d.Now()
	}
	return time.Now()
}

func (a *api) me(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	writeJSON(w, 200, map[string]any{"user": p.User, "via_key": p.ViaKey()})
}

func (a *api) capabilities(w http.ResponseWriter, r *http.Request) {
	var out []map[string]any
	for _, name := range a.d.Registry.Names() {
		f, _ := a.d.Registry.Get(name)
		out = append(out, map[string]any{
			"name":          name,
			"ops":           f.Ops(),
			"config_fields": f.ConfigFields(),
		})
	}
	writeJSON(w, 200, map[string]any{"capabilities": out})
}

// --- keys ---

func (a *api) createKey(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	var body struct {
		Name      string `json:"name"`
		ExpiresIn int64  `json:"expires_in_seconds"`
	}
	if err := decode(w, r, &body, maxBodyBytes); err != nil {
		writeErr(w, 400, types.ErrCodeBadRequest, "invalid JSON body")
		return
	}
	var exp *time.Time
	if body.ExpiresIn > 0 {
		t := a.now().Add(time.Duration(body.ExpiresIn) * time.Second)
		exp = &t
	}
	secret, key := auth.NewAPIKey(p.User.ID, body.Name, exp)
	if err := a.d.Store.CreateAPIKey(r.Context(), key); err != nil {
		writeErr(w, 500, types.ErrCodeInternal, err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"key": key, "secret": secret})
}

func (a *api) listKeys(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	keys, err := a.d.Store.ListAPIKeys(r.Context(), p.User.ID)
	if err != nil {
		writeErr(w, 500, types.ErrCodeInternal, err.Error())
		return
	}
	if keys == nil {
		keys = []*store.APIKey{}
	}
	writeJSON(w, 200, map[string]any{"keys": keys})
}

func (a *api) deleteKey(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	id := r.PathValue("id")
	if err := a.d.Store.RevokeAPIKey(r.Context(), p.User.ID, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, 404, types.ErrCodeNotFound, "key not found")
			return
		}
		writeErr(w, 500, types.ErrCodeInternal, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// --- instances ---

func (a *api) createInstance(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		writeErr(w, 400, types.ErrCodeBadRequest, "body too large")
		return
	}
	if len(raw) == 0 {
		raw = []byte(`{}`)
	}
	meta, err := a.d.Manager.Create(r.Context(), p.User, raw)
	if err != nil {
		switch {
		case errors.Is(err, instance.ErrTooMany):
			writeErr(w, 429, types.ErrCodeTooMany, err.Error())
		case errors.Is(err, instance.ErrCapabilityName):
			writeErr(w, 400, types.ErrCodeBadCapability, err.Error())
		default:
			writeErr(w, 400, types.ErrCodeBadSpec, err.Error())
		}
		return
	}
	writeJSON(w, 201, map[string]any{"instance": meta})
}

func (a *api) listInstances(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	var status types.InstanceStatus
	if q := r.URL.Query().Get("status"); q != "" {
		status = types.InstanceStatus(q)
		if !status.Valid() {
			writeErr(w, 400, types.ErrCodeBadRequest, "invalid status filter")
			return
		}
	}
	lst, err := a.d.Manager.List(r.Context(), p.User.ID, status)
	if err != nil {
		writeErr(w, 500, types.ErrCodeInternal, err.Error())
		return
	}
	if lst == nil {
		lst = []*store.Instance{}
	}
	writeJSON(w, 200, map[string]any{"instances": lst})
}

// ownedInstance fetches the instance enforcing ownership: other users'
// resources are 404.
func (a *api) ownedInstance(w http.ResponseWriter, r *http.Request) (*store.Instance, bool) {
	p := principal(r)
	in, err := a.d.Manager.Get(r.Context(), r.PathValue("id"))
	if err != nil || in.UserID != p.User.ID {
		writeErr(w, 404, types.ErrCodeNotFound, "instance not found")
		return nil, false
	}
	return in, true
}

func (a *api) getInstance(w http.ResponseWriter, r *http.Request) {
	in, ok := a.ownedInstance(w, r)
	if !ok {
		return
	}
	writeJSON(w, 200, map[string]any{"instance": in})
}

func (a *api) deleteInstance(w http.ResponseWriter, r *http.Request) {
	in, ok := a.ownedInstance(w, r)
	if !ok {
		return
	}
	if err := a.d.Manager.Delete(r.Context(), in.ID); err != nil {
		if errors.Is(err, instance.ErrNotFound) {
			writeErr(w, 404, types.ErrCodeNotFound, "instance not found")
			return
		}
		writeErr(w, 500, types.ErrCodeInternal, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (a *api) keepalive(w http.ResponseWriter, r *http.Request) {
	in, ok := a.ownedInstance(w, r)
	if !ok {
		return
	}
	meta, err := a.d.Manager.Keepalive(r.Context(), in.ID)
	if err != nil {
		if errors.Is(err, instance.ErrNotRunning) {
			writeErr(w, 409, types.ErrCodeNotRunning, "instance not running")
			return
		}
		writeErr(w, 404, types.ErrCodeNotFound, "instance not found")
		return
	}
	writeJSON(w, 200, map[string]any{"instance": meta})
}

func (a *api) exec(w http.ResponseWriter, r *http.Request) {
	in, ok := a.ownedInstance(w, r)
	if !ok {
		return
	}
	if in.Status != types.InstanceRunning {
		writeErr(w, 409, types.ErrCodeNotRunning, "instance not running")
		return
	}
	var body struct {
		Code      string `json:"code"`
		TimeoutMs int64  `json:"timeout_ms"`
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxCodeBytes+1024))
	if err != nil {
		writeErr(w, 413, types.ErrCodeTooLarge, "request body too large")
		return
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		writeErr(w, 400, types.ErrCodeBadRequest, "invalid JSON body")
		return
	}
	if len(body.Code) > maxCodeBytes {
		writeErr(w, 413, types.ErrCodeTooLarge, "code exceeds 256KB")
		return
	}
	p := principal(r)
	record := func(res *engine.Result, execID string) {
		sum := sha256.Sum256([]byte(body.Code))
		snippet := body.Code
		if len(snippet) > snippetBytes {
			snippet = snippet[:snippetBytes]
		}
		status := types.ExecOK
		var errType types.ExecErrorType
		if res.Error != nil {
			status = types.ExecError
			errType = res.Error.Type
		}
		_ = a.d.Store.CreateExecution(r.Context(), &store.Execution{
			ID: execID, InstanceID: in.ID, UserID: p.User.ID,
			CodeSHA256: hex.EncodeToString(sum[:]), CodeSnippet: snippet,
			Status: status, ErrorType: errType, DurationMs: res.DurationMs,
			Steps: res.Steps, OutputBytes: int64(len(res.Output)),
		})
	}
	res, err := a.d.Manager.Exec(r.Context(), in.ID, body.Code,
		time.Duration(body.TimeoutMs)*time.Millisecond, record)
	if err != nil {
		switch {
		case errors.Is(err, instance.ErrNotFound):
			writeErr(w, 404, types.ErrCodeNotFound, "instance not found")
		case errors.Is(err, instance.ErrNotRunning):
			writeErr(w, 409, types.ErrCodeNotRunning, "instance not running")
		default:
			writeErr(w, 400, types.ErrCodeBadRequest, err.Error())
		}
		return
	}
	writeJSON(w, 200, res)
}

// files serves GET /instances/{id}/files?path=/work/... through the fs
// capability binding inside a gated "console" session.
func (a *api) files(w http.ResponseWriter, r *http.Request) {
	in, ok := a.ownedInstance(w, r)
	if !ok {
		return
	}
	if in.Status != types.InstanceRunning {
		writeErr(w, 409, types.ErrCodeNotRunning, "instance not running")
		return
	}
	if !a.d.Manager.HasCapability(in.ID, string(types.CapFS)) {
		writeErr(w, 400, types.ErrCodeNoFS, "instance has no fs capability")
		return
	}
	p := r.URL.Query().Get("path")
	if p == "" {
		p = "/work"
	}
	var stat map[string]any
	var listing []any
	var content string
	err := a.d.Manager.WithSession(in.ID, func(s *engine.Session, gate *capability.Gate) error {
		fsv, ok := s.Predeclared[string(types.CapFS)]
		if !ok {
			return errors.New("no fs binding")
		}
		mod, ok := fsv.(starlark.HasAttrs)
		if !ok {
			return errors.New("bad fs binding")
		}
		thread := &starlark.Thread{Name: "console"}
		thread.SetLocal(capability.ContextKey, r.Context())
		call := func(method string, args ...starlark.Value) (starlark.Value, error) {
			fn, err := mod.Attr(method)
			if err != nil {
				return nil, err
			}
			return starlark.Call(thread, fn, starlark.Tuple(args), nil)
		}
		statV, err := call("stat", starlark.String(p))
		if err != nil {
			return err
		}
		stat, err = toGo(statV)
		if err != nil {
			return err
		}
		if d, _ := stat["is_dir"].(bool); d {
			listV, err := call("list", starlark.String(p))
			if err != nil {
				return err
			}
			v, err := toGoAny(listV)
			if err != nil {
				return err
			}
			listing, _ = v.([]any)
			return nil
		}
		readV, err := call("read", starlark.String(p))
		if err != nil {
			return err
		}
		if s, ok := readV.(starlark.String); ok {
			content = string(s)
		}
		return nil
	})
	if err != nil {
		switch {
		case errors.Is(err, instance.ErrNotFound):
			writeErr(w, 404, types.ErrCodeNotFound, "instance not found")
		case errors.Is(err, instance.ErrNotRunning):
			writeErr(w, 409, types.ErrCodeNotRunning, "instance not running")
		case strings.Contains(err.Error(), "does not exist"):
			writeErr(w, 404, types.ErrCodeNotFound, err.Error())
		default:
			writeErr(w, 400, types.ErrCodeFSError, err.Error())
		}
		return
	}
	if d, _ := stat["is_dir"].(bool); d {
		if listing == nil {
			listing = []any{}
		}
		writeJSON(w, 200, map[string]any{"entries": listing})
		return
	}
	writeJSON(w, 200, map[string]any{"path": stat["path"], "content": a.d.Manager.Redact(in.ID, content)})
}

// toGo converts starlark dict/list primitives to Go values.
func toGo(v starlark.Value) (map[string]any, error) {
	switch t := v.(type) {
	case *starlark.Dict:
		out := map[string]any{}
		for _, item := range t.Items() {
			k, _ := starlark.AsString(item[0])
			gv, err := toGoAny(item[1])
			if err != nil {
				return nil, err
			}
			out[k] = gv
		}
		return out, nil
	}
	return nil, errors.New("not a dict")
}

func toGoAny(v starlark.Value) (any, error) {
	switch t := v.(type) {
	case starlark.String:
		return string(t), nil
	case starlark.Bool:
		return bool(t), nil
	case starlark.Int:
		n, ok := t.Int64()
		if !ok {
			return nil, errors.New("int too large")
		}
		return n, nil
	case *starlark.List:
		out := make([]any, t.Len())
		for i := 0; i < t.Len(); i++ {
			gv, err := toGoAny(t.Index(i))
			if err != nil {
				return nil, err
			}
			out[i] = gv
		}
		return out, nil
	case *starlark.Dict:
		return toGo(t)
	default:
		return t.String(), nil
	}
}

func (a *api) executions(w http.ResponseWriter, r *http.Request) {
	in, ok := a.ownedInstance(w, r)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	lst, err := a.d.Store.ListExecutions(r.Context(), in.ID, limit)
	if err != nil {
		writeErr(w, 500, types.ErrCodeInternal, err.Error())
		return
	}
	if lst == nil {
		lst = []*store.Execution{}
	}
	writeJSON(w, 200, map[string]any{"executions": lst})
}

func (a *api) auditEvents(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	q := r.URL.Query()
	f := store.AuditFilter{UserID: p.User.ID, InstanceID: q.Get("instance_id"), ExecID: q.Get("exec_id")}
	if l := q.Get("limit"); l != "" {
		f.Limit, _ = strconv.Atoi(l)
	}
	if b := q.Get("before"); b != "" {
		if t, err := time.Parse(time.RFC3339Nano, b); err == nil {
			f.Before = &t
		}
	}
	lst, err := a.d.Store.ListAuditEvents(r.Context(), f)
	if err != nil {
		writeErr(w, 500, types.ErrCodeInternal, err.Error())
		return
	}
	if lst == nil {
		lst = []*store.AuditEvent{}
	}
	writeJSON(w, 200, map[string]any{"events": lst})
}

// --- policies ---

func (a *api) listPolicies(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	lst, err := a.d.Store.ListPolicies(r.Context(), p.User.ID)
	if err != nil {
		writeErr(w, 500, types.ErrCodeInternal, err.Error())
		return
	}
	if lst == nil {
		lst = []*store.Policy{}
	}
	writeJSON(w, 200, map[string]any{"policies": lst})
}

func (a *api) createPolicy(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	var body struct {
		Name    string `json:"name"`
		Rego    string `json:"rego"`
		Enabled *bool  `json:"enabled"`
	}
	if err := decode(w, r, &body, maxBodyBytes); err != nil {
		writeErr(w, 400, types.ErrCodeBadRequest, "invalid JSON body")
		return
	}
	if body.Name == "" || body.Rego == "" {
		writeErr(w, 400, types.ErrCodeBadRequest, "name and rego required")
		return
	}
	if err := policy.Validate(body.Rego); err != nil {
		writeErr(w, 400, types.ErrCodeBadPolicy, err.Error())
		return
	}
	pol := &store.Policy{UserID: p.User.ID, Name: body.Name, Rego: body.Rego, Enabled: true}
	if body.Enabled != nil {
		pol.Enabled = *body.Enabled
	}
	if err := a.d.Store.CreatePolicy(r.Context(), pol); err != nil {
		writeErr(w, 500, types.ErrCodeInternal, err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"policy": pol})
}

func (a *api) getPolicy(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	pol, err := a.d.Store.GetPolicy(r.Context(), r.PathValue("id"))
	if err != nil || pol.UserID != p.User.ID {
		writeErr(w, 404, types.ErrCodeNotFound, "policy not found")
		return
	}
	writeJSON(w, 200, map[string]any{"policy": pol})
}

func (a *api) updatePolicy(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	pol, err := a.d.Store.GetPolicy(r.Context(), r.PathValue("id"))
	if err != nil || pol.UserID != p.User.ID {
		writeErr(w, 404, types.ErrCodeNotFound, "policy not found")
		return
	}
	var body struct {
		Name    *string `json:"name"`
		Rego    *string `json:"rego"`
		Enabled *bool   `json:"enabled"`
	}
	if err := decode(w, r, &body, maxBodyBytes); err != nil {
		writeErr(w, 400, types.ErrCodeBadRequest, "invalid JSON body")
		return
	}
	if body.Name != nil {
		pol.Name = *body.Name
	}
	if body.Rego != nil {
		if err := policy.Validate(*body.Rego); err != nil {
			writeErr(w, 400, types.ErrCodeBadPolicy, err.Error())
			return
		}
		pol.Rego = *body.Rego
	}
	if body.Enabled != nil {
		pol.Enabled = *body.Enabled
	}
	if err := a.d.Store.UpdatePolicy(r.Context(), pol); err != nil {
		writeErr(w, 500, types.ErrCodeInternal, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"policy": pol})
}

func (a *api) deletePolicy(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	pol, err := a.d.Store.GetPolicy(r.Context(), r.PathValue("id"))
	if err != nil || pol.UserID != p.User.ID {
		writeErr(w, 404, types.ErrCodeNotFound, "policy not found")
		return
	}
	if err := a.d.Store.DeletePolicy(r.Context(), pol.ID); err != nil {
		writeErr(w, 500, types.ErrCodeInternal, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (a *api) validatePolicy(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Rego string `json:"rego"`
	}
	if err := decode(w, r, &body, maxBodyBytes); err != nil {
		writeErr(w, 400, types.ErrCodeBadRequest, "invalid JSON body")
		return
	}
	if err := policy.Validate(body.Rego); err != nil {
		writeJSON(w, 200, map[string]any{"valid": false, "error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"valid": true})
}

// --- secrets vault (session-only; see sessionGate) ---

func (a *api) secretsEnabled(w http.ResponseWriter) bool {
	if a.d.Cipher == nil {
		writeErr(w, 503, types.ErrCodeSecretsDisabled, "secrets vault disabled (no --secret-key)")
		return false
	}
	return true
}

func (a *api) listSecrets(w http.ResponseWriter, r *http.Request) {
	if !a.secretsEnabled(w) {
		return
	}
	p := principal(r)
	lst, err := a.d.Store.ListSecrets(r.Context(), p.User.ID)
	if err != nil {
		writeErr(w, 500, types.ErrCodeInternal, err.Error())
		return
	}
	if lst == nil {
		lst = []*store.Secret{}
	}
	writeJSON(w, 200, map[string]any{"secrets": lst})
}

func validateSecretInput(name, value string, domains []string) error {
	if !secrets.ValidName(name) {
		return fmt.Errorf("invalid secret name %q (want [A-Z_][A-Z0-9_])", name)
	}
	if len(value) == 0 {
		return fmt.Errorf("value required")
	}
	if len(value) > secrets.MaxValueBytes {
		return fmt.Errorf("value exceeds %d bytes", secrets.MaxValueBytes)
	}
	if _, err := secrets.ValidateDomains(domains); err != nil {
		return err
	}
	return nil
}

func (a *api) createSecret(w http.ResponseWriter, r *http.Request) {
	if !a.secretsEnabled(w) {
		return
	}
	p := principal(r)
	var body struct {
		Name           string   `json:"name"`
		Value          string   `json:"value"`
		AllowedDomains []string `json:"allowed_domains"`
	}
	if err := decode(w, r, &body, maxBodyBytes); err != nil {
		writeErr(w, 400, types.ErrCodeBadRequest, "invalid JSON body")
		return
	}
	if err := validateSecretInput(body.Name, body.Value, body.AllowedDomains); err != nil {
		writeErr(w, 400, types.ErrCodeBadSecret, err.Error())
		return
	}
	ct, err := a.d.Cipher.Seal([]byte(body.Value), p.User.ID+"/"+body.Name)
	if err != nil {
		writeErr(w, 500, types.ErrCodeInternal, err.Error())
		return
	}
	s := &store.Secret{UserID: p.User.ID, Name: body.Name, Ciphertext: ct, AllowedDomains: body.AllowedDomains}
	if err := a.d.Store.CreateSecret(r.Context(), s); err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeErr(w, 409, types.ErrCodeConflict, "secret with that name already exists")
			return
		}
		writeErr(w, 500, types.ErrCodeInternal, err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"secret": s})
}

// ownedSecret fetches a secret enforcing ownership (other users ⇒ 404).
func (a *api) ownedSecret(w http.ResponseWriter, r *http.Request) (*store.Secret, bool) {
	p := principal(r)
	s, err := a.d.Store.GetSecret(r.Context(), r.PathValue("id"))
	if err != nil || s.UserID != p.User.ID {
		writeErr(w, 404, types.ErrCodeNotFound, "secret not found")
		return nil, false
	}
	return s, true
}

func (a *api) updateSecret(w http.ResponseWriter, r *http.Request) {
	if !a.secretsEnabled(w) {
		return
	}
	s, ok := a.ownedSecret(w, r)
	if !ok {
		return
	}
	p := principal(r)
	var body struct {
		Value          *string  `json:"value"`
		AllowedDomains []string `json:"allowed_domains"`
	}
	if err := decode(w, r, &body, maxBodyBytes); err != nil {
		writeErr(w, 400, types.ErrCodeBadRequest, "invalid JSON body")
		return
	}
	if body.Value != nil {
		if len(*body.Value) == 0 || len(*body.Value) > secrets.MaxValueBytes {
			writeErr(w, 400, types.ErrCodeBadSecret, "value must be 1..16KiB")
			return
		}
		ct, err := a.d.Cipher.Seal([]byte(*body.Value), p.User.ID+"/"+s.Name)
		if err != nil {
			writeErr(w, 500, types.ErrCodeInternal, err.Error())
			return
		}
		s.Ciphertext = ct
	}
	if body.AllowedDomains != nil {
		if _, err := secrets.ValidateDomains(body.AllowedDomains); err != nil {
			writeErr(w, 400, types.ErrCodeBadSecret, err.Error())
			return
		}
		s.AllowedDomains = body.AllowedDomains
	}
	if err := a.d.Store.UpdateSecret(r.Context(), s); err != nil {
		writeErr(w, 500, types.ErrCodeInternal, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"secret": s})
}

func (a *api) deleteSecret(w http.ResponseWriter, r *http.Request) {
	if !a.secretsEnabled(w) {
		return
	}
	s, ok := a.ownedSecret(w, r)
	if !ok {
		return
	}
	if err := a.d.Store.DeleteSecret(r.Context(), s.ID); err != nil {
		writeErr(w, 500, types.ErrCodeInternal, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
