package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.starlark.net/starlark"

	"calcside/internal/api/gen"
	"calcside/internal/auth"
	"calcside/internal/capability"
	"calcside/internal/engine"
	"calcside/internal/instance"
	promptpkg "calcside/internal/prompt"
	"calcside/internal/secrets"
	"calcside/internal/service"
	auditsvc "calcside/internal/service/audit"
	"calcside/internal/store"
	"calcside/internal/types"
)

// strictImpl implements gen.StrictServerInterface. Responses use the
// rawJSON wrappers below so the wire shapes stay the same as the
// pre-OpenAPI handlers (store/domain structs marshal identically).
type strictImpl struct {
	d Deps
}

// --- raw JSON response plumbing ---

type rawJSON struct {
	status int
	body   any
}

func (r rawJSON) write(w http.ResponseWriter) error {
	b, err := json.Marshal(r.body)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(r.status)
	_, err = w.Write(b)
	return err
}

func principal(ctx context.Context) *auth.Principal { return auth.FromContext(ctx) }

// realCtx unwraps *gin.Context into Request.Context(): the gin Context
// is recycled after the handler returns and must not leak into store or
// driver code (drivers spawn ctx watchers past the request lifetime).
func realCtx(ctx context.Context) context.Context {
	if gc, ok := ctx.(*gin.Context); ok {
		return gc.Request.Context()
	}
	return ctx
}

func needAuth(ctx context.Context) (*auth.Principal, *rawJSON) {
	if p := principal(ctx); p != nil {
		return p, nil
	}
	e := fail(&service.Error{Code: types.ErrCodeUnauthorized, Msg: "authentication required"})
	return nil, &e
}

// needSession rejects api-key principals for key/secret management; dev
// anonymous counts as a session.
func needSession(ctx context.Context) (*auth.Principal, *rawJSON) {
	p := principal(ctx)
	if p == nil {
		e := fail(&service.Error{Code: types.ErrCodeUnauthorized, Msg: "authentication required"})
		return nil, &e
	}
	if p.ViaKey() {
		e := fail(service.Forbidden("session required to manage API keys"))
		return nil, &e
	}
	return p, nil
}

// actorOf maps the transport principal onto the service-layer actor.
func actorOf(p *auth.Principal) service.Actor {
	return service.Actor{UserID: p.User.ID, Email: p.User.Email, Kind: p.Kind}
}

// Per-operation response wrappers (generated files are never edited).

type healthzResp struct{ rawJSON }

func (r healthzResp) VisitHealthzResponse(w http.ResponseWriter) error { return r.write(w) }

type authConfigResp struct{ rawJSON }

func (r authConfigResp) VisitAuthConfigResponse(w http.ResponseWriter) error { return r.write(w) }

type meResp struct{ rawJSON }

func (r meResp) VisitMeResponse(w http.ResponseWriter) error { return r.write(w) }

type capabilitiesResp struct{ rawJSON }

func (r capabilitiesResp) VisitCapabilitiesResponse(w http.ResponseWriter) error {
	return r.write(w)
}

type listExtensionsResp struct{ rawJSON }

func (r listExtensionsResp) VisitListExtensionsResponse(w http.ResponseWriter) error {
	return r.write(w)
}

type listKeysResp struct{ rawJSON }

func (r listKeysResp) VisitListKeysResponse(w http.ResponseWriter) error { return r.write(w) }

type createKeyResp struct{ rawJSON }

func (r createKeyResp) VisitCreateKeyResponse(w http.ResponseWriter) error { return r.write(w) }

type deleteKeyResp struct{ rawJSON }

func (r deleteKeyResp) VisitDeleteKeyResponse(w http.ResponseWriter) error { return r.write(w) }

type listInstancesResp struct{ rawJSON }

func (r listInstancesResp) VisitListInstancesResponse(w http.ResponseWriter) error {
	return r.write(w)
}

type createInstanceResp struct{ rawJSON }

func (r createInstanceResp) VisitCreateInstanceResponse(w http.ResponseWriter) error {
	return r.write(w)
}

type getInstanceResp struct{ rawJSON }

func (r getInstanceResp) VisitGetInstanceResponse(w http.ResponseWriter) error { return r.write(w) }

type deleteInstanceResp struct{ rawJSON }

func (r deleteInstanceResp) VisitDeleteInstanceResponse(w http.ResponseWriter) error {
	return r.write(w)
}

type keepaliveResp struct{ rawJSON }

func (r keepaliveResp) VisitKeepaliveResponse(w http.ResponseWriter) error { return r.write(w) }

type execResp struct{ rawJSON }

func (r execResp) VisitExecResponse(w http.ResponseWriter) error { return r.write(w) }

type filesResp struct{ rawJSON }

func (r filesResp) VisitFilesResponse(w http.ResponseWriter) error { return r.write(w) }

type instancePromptResp struct{ rawJSON }

func (r instancePromptResp) VisitInstancePromptResponse(w http.ResponseWriter) error {
	return r.write(w)
}

type listExecutionsResp struct{ rawJSON }

func (r listExecutionsResp) VisitListExecutionsResponse(w http.ResponseWriter) error {
	return r.write(w)
}

type getExecutionResp struct{ rawJSON }

func (r getExecutionResp) VisitGetExecutionResponse(w http.ResponseWriter) error {
	return r.write(w)
}

type listAuditResp struct{ rawJSON }

func (r listAuditResp) VisitListAuditResponse(w http.ResponseWriter) error { return r.write(w) }

type listPoliciesResp struct{ rawJSON }

func (r listPoliciesResp) VisitListPoliciesResponse(w http.ResponseWriter) error {
	return r.write(w)
}

type createPolicyResp struct{ rawJSON }

func (r createPolicyResp) VisitCreatePolicyResponse(w http.ResponseWriter) error {
	return r.write(w)
}

type validatePolicyResp struct{ rawJSON }

func (r validatePolicyResp) VisitValidatePolicyResponse(w http.ResponseWriter) error {
	return r.write(w)
}

type getPolicyResp struct{ rawJSON }

func (r getPolicyResp) VisitGetPolicyResponse(w http.ResponseWriter) error { return r.write(w) }

type updatePolicyResp struct{ rawJSON }

func (r updatePolicyResp) VisitUpdatePolicyResponse(w http.ResponseWriter) error {
	return r.write(w)
}

type deletePolicyResp struct{ rawJSON }

func (r deletePolicyResp) VisitDeletePolicyResponse(w http.ResponseWriter) error {
	return r.write(w)
}

type listSecretsResp struct{ rawJSON }

func (r listSecretsResp) VisitListSecretsResponse(w http.ResponseWriter) error {
	return r.write(w)
}

type createSecretResp struct{ rawJSON }

func (r createSecretResp) VisitCreateSecretResponse(w http.ResponseWriter) error {
	return r.write(w)
}

type updateSecretResp struct{ rawJSON }

func (r updateSecretResp) VisitUpdateSecretResponse(w http.ResponseWriter) error {
	return r.write(w)
}

type deleteSecretResp struct{ rawJSON }

func (r deleteSecretResp) VisitDeleteSecretResponse(w http.ResponseWriter) error {
	return r.write(w)
}

// --- healthz / auth config / me ---

func (s *strictImpl) Healthz(ctx context.Context, _ gen.HealthzRequestObject) (gen.HealthzResponseObject, error) {
	ctx = realCtx(ctx)
	return healthzResp{rawJSON{http.StatusOK, map[string]any{"ok": true}}}, nil
}

func (s *strictImpl) AuthConfig(ctx context.Context, _ gen.AuthConfigRequestObject) (gen.AuthConfigResponseObject, error) {
	ctx = realCtx(ctx)
	return authConfigResp{rawJSON{http.StatusOK, map[string]any{
		"google":   s.d.GoogleEnabled,
		"dev_mode": s.d.Dev,
		"secrets":  s.d.Vault.Enabled(),
	}}}, nil
}

func (s *strictImpl) Me(ctx context.Context, _ gen.MeRequestObject) (gen.MeResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return meResp{*e}, nil
	}
	return meResp{rawJSON{http.StatusOK, map[string]any{
		"user": p.User, "via_key": p.ViaKey(), "kind": p.Kind,
	}}}, nil
}

// --- capabilities ---

func (s *strictImpl) Capabilities(ctx context.Context, _ gen.CapabilitiesRequestObject) (gen.CapabilitiesResponseObject, error) {
	ctx = realCtx(ctx)
	if _, e := needAuth(ctx); e != nil {
		return capabilitiesResp{*e}, nil
	}
	return capabilitiesResp{rawJSON{http.StatusOK, map[string]any{"capabilities": s.d.Catalog.Capabilities()}}}, nil
}

func (s *strictImpl) ListExtensions(ctx context.Context, _ gen.ListExtensionsRequestObject) (gen.ListExtensionsResponseObject, error) {
	ctx = realCtx(ctx)
	if _, e := needAuth(ctx); e != nil {
		return listExtensionsResp{*e}, nil
	}
	return listExtensionsResp{rawJSON{http.StatusOK, s.d.Catalog.Extensions()}}, nil
}

// --- api keys (session only) ---

func (s *strictImpl) ListKeys(ctx context.Context, _ gen.ListKeysRequestObject) (gen.ListKeysResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return listKeysResp{*e}, nil
	}
	keys, err := s.d.IAM.ListKeys(ctx, actorOf(p))
	if err != nil {
		return listKeysResp{fail(err)}, nil
	}
	return listKeysResp{rawJSON{200, map[string]any{"keys": keys}}}, nil
}

func (s *strictImpl) CreateKey(ctx context.Context, req gen.CreateKeyRequestObject) (gen.CreateKeyResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return createKeyResp{*e}, nil
	}
	var name string
	var expiresIn int64
	if req.Body != nil {
		name = req.Body.Name
		if req.Body.ExpiresInSeconds != nil {
			expiresIn = *req.Body.ExpiresInSeconds
		}
	}
	key, secret, err := s.d.IAM.CreateKey(ctx, actorOf(p), name, expiresIn)
	if err != nil {
		return createKeyResp{fail(err)}, nil
	}
	return createKeyResp{rawJSON{201, map[string]any{"key": key, "secret": secret}}}, nil
}

func (s *strictImpl) DeleteKey(ctx context.Context, req gen.DeleteKeyRequestObject) (gen.DeleteKeyResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return deleteKeyResp{*e}, nil
	}
	if err := s.d.IAM.RevokeKey(ctx, actorOf(p), req.Id); err != nil {
		return deleteKeyResp{fail(err)}, nil
	}
	return deleteKeyResp{rawJSON{200, map[string]any{"ok": true}}}, nil
}

// --- instances ---

func (s *strictImpl) ListInstances(ctx context.Context, req gen.ListInstancesRequestObject) (gen.ListInstancesResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return listInstancesResp{*e}, nil
	}
	var status types.InstanceStatus
	if req.Params.Status != nil {
		status = types.InstanceStatus(*req.Params.Status)
		if !status.Valid() {
			return listInstancesResp{fail(service.BadRequest("invalid status filter"))}, nil
		}
	}
	lst, err := s.d.Manager.List(ctx, p.User.ID, status)
	if err != nil {
		return listInstancesResp{fail(service.Internal(err))}, nil
	}
	if lst == nil {
		lst = []*store.Instance{}
	}
	return listInstancesResp{rawJSON{200, map[string]any{"instances": lst}}}, nil
}

func (s *strictImpl) CreateInstance(ctx context.Context, req gen.CreateInstanceRequestObject) (gen.CreateInstanceResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return createInstanceResp{*e}, nil
	}
	raw := []byte(`{}`)
	if req.Body != nil {
		b, err := json.Marshal(req.Body)
		if err != nil {
			return createInstanceResp{fail(service.BadRequest("invalid JSON body"))}, nil
		}
		raw = b
	}
	meta, err := s.d.Manager.Create(ctx, p.User, raw)
	if err != nil {
		switch {
		case errors.Is(err, instance.ErrTooMany):
			return createInstanceResp{fail(service.Errf(types.ErrCodeTooMany, "%s", err.Error()))}, nil
		case errors.Is(err, instance.ErrCapabilityName):
			return createInstanceResp{fail(service.Errf(types.ErrCodeBadCapability, "%s", err.Error()))}, nil
		default:
			return createInstanceResp{fail(service.Errf(types.ErrCodeBadSpec, "%s", err.Error()))}, nil
		}
	}
	return createInstanceResp{rawJSON{201, map[string]any{"instance": meta}}}, nil
}

// ownedInstance fetches the instance enforcing ownership: other users'
// resources are 404.
func (s *strictImpl) ownedInstance(ctx context.Context, id string) (*store.Instance, *rawJSON) {
	ctx = realCtx(ctx)
	p := principal(ctx)
	in, err := s.d.Manager.Get(ctx, id)
	if err != nil || in.UserID != p.User.ID {
		e := fail(service.NotFound("instance not found"))
		return nil, &e
	}
	return in, nil
}

func (s *strictImpl) GetInstance(ctx context.Context, req gen.GetInstanceRequestObject) (gen.GetInstanceResponseObject, error) {
	ctx = realCtx(ctx)
	if _, e := needAuth(ctx); e != nil {
		return getInstanceResp{*e}, nil
	}
	in, e := s.ownedInstance(ctx, req.Id)
	if e != nil {
		return getInstanceResp{*e}, nil
	}
	return getInstanceResp{rawJSON{200, map[string]any{"instance": in}}}, nil
}

func (s *strictImpl) DeleteInstance(ctx context.Context, req gen.DeleteInstanceRequestObject) (gen.DeleteInstanceResponseObject, error) {
	ctx = realCtx(ctx)
	if _, e := needAuth(ctx); e != nil {
		return deleteInstanceResp{*e}, nil
	}
	in, e := s.ownedInstance(ctx, req.Id)
	if e != nil {
		return deleteInstanceResp{*e}, nil
	}
	if err := s.d.Manager.Delete(ctx, in.ID); err != nil {
		if errors.Is(err, instance.ErrNotFound) {
			return deleteInstanceResp{fail(service.NotFound("instance not found"))}, nil
		}
		return deleteInstanceResp{fail(service.Internal(err))}, nil
	}
	return deleteInstanceResp{rawJSON{200, map[string]any{"ok": true}}}, nil
}

func (s *strictImpl) Keepalive(ctx context.Context, req gen.KeepaliveRequestObject) (gen.KeepaliveResponseObject, error) {
	ctx = realCtx(ctx)
	if _, e := needAuth(ctx); e != nil {
		return keepaliveResp{*e}, nil
	}
	in, e := s.ownedInstance(ctx, req.Id)
	if e != nil {
		return keepaliveResp{*e}, nil
	}
	meta, err := s.d.Manager.Keepalive(ctx, in.ID)
	if err != nil {
		if errors.Is(err, instance.ErrNotRunning) {
			return keepaliveResp{fail(service.Errf(types.ErrCodeNotRunning, "instance not running"))}, nil
		}
		return keepaliveResp{fail(service.NotFound("instance not found"))}, nil
	}
	return keepaliveResp{rawJSON{200, map[string]any{"instance": meta}}}, nil
}

// Exec a code snippet
// 1. loop-up or create an instance
func (s *strictImpl) Exec(ctx context.Context, req gen.ExecRequestObject) (gen.ExecResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return execResp{*e}, nil
	}
	in, e := s.ownedInstance(ctx, req.Id)
	if e != nil {
		return execResp{*e}, nil
	}
	if in.Status != types.InstanceRunning {
		return execResp{fail(service.Errf(types.ErrCodeNotRunning, "instance not running"))}, nil
	}
	var body gen.ExecRequest
	if req.Body != nil {
		body = *req.Body
	}
	var timeoutMs int64
	if body.TimeoutMs != nil {
		timeoutMs = *body.TimeoutMs
	}
	if len(body.Code) > maxCodeBytes {
		return execResp{fail(service.Errf(types.ErrCodeTooLarge, "code exceeds 256KB"))}, nil
	}
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
		_ = s.d.Store.CreateExecution(ctx, &store.Execution{
			ID: execID, InstanceID: in.ID, UserID: p.User.ID,
			CodeSHA256: hex.EncodeToString(sum[:]), CodeSnippet: snippet, Code: body.Code,
			Status: status, ErrorType: errType, DurationMs: res.DurationMs,
			Steps: res.Steps, OutputBytes: int64(len(res.Output)),
		})
	}
	res, err := s.d.Manager.Exec(ctx, in.ID, body.Code,
		time.Duration(timeoutMs)*time.Millisecond, record)
	if err != nil {
		switch {
		case errors.Is(err, instance.ErrNotFound):
			return execResp{fail(service.NotFound("instance not found"))}, nil
		case errors.Is(err, instance.ErrNotRunning):
			return execResp{fail(service.Errf(types.ErrCodeNotRunning, "instance not running"))}, nil
		default:
			return execResp{fail(service.BadRequest("%s", err.Error()))}, nil
		}
	}
	return execResp{rawJSON{200, res}}, nil
}

var toolPrefixRe = regexp.MustCompile(`^[A-Za-z0-9_]{0,32}$`)

// InstancePrompt renders the server-side agent system prompt for a live
// instance.
func (s *strictImpl) InstancePrompt(ctx context.Context, req gen.InstancePromptRequestObject) (gen.InstancePromptResponseObject, error) {
	ctx = realCtx(ctx)
	if _, e := needAuth(ctx); e != nil {
		return instancePromptResp{*e}, nil
	}
	in, e := s.ownedInstance(ctx, req.Id)
	if e != nil {
		return instancePromptResp{*e}, nil
	}
	prefix := "calcside_"
	if req.Params.ToolPrefix != nil {
		prefix = *req.Params.ToolPrefix
	}
	if !toolPrefixRe.MatchString(prefix) {
		return instancePromptResp{fail(service.BadRequest("invalid tool_prefix"))}, nil
	}
	if in.Status != types.InstanceRunning {
		return instancePromptResp{fail(service.Errf(types.ErrCodeNotRunning, "instance not running"))}, nil
	}
	data, err := s.d.Manager.PromptData(in.ID)
	if err != nil {
		switch {
		case errors.Is(err, instance.ErrNotFound):
			return instancePromptResp{fail(service.NotFound("instance not found"))}, nil
		case errors.Is(err, instance.ErrNotRunning):
			return instancePromptResp{fail(service.Errf(types.ErrCodeNotRunning, "instance not running"))}, nil
		default:
			return instancePromptResp{fail(service.Internal(err))}, nil
		}
	}
	var fragments []string
	var names []types.CapabilityName
	for _, p := range data.Parts {
		fragments = append(fragments, p.Factory.Prompt(p.Config))
		names = append(names, p.Factory.Name())
	}
	var secretsInfo []promptpkg.SecretInfo
	for _, sec := range data.Secrets {
		secretsInfo = append(secretsInfo, promptpkg.SecretInfo{Name: sec.Name, Domains: sec.Domains})
	}
	var netHost string
	if len(data.NetHosts) > 0 {
		netHost = data.NetHosts[0]
	} else if slices.Contains(capNamesToStrings(names), "net") {
		// Unrestricted net: still show a worked example.
		netHost = "api.example.com"
	}
	text, err := promptpkg.Render(promptpkg.Input{
		InstanceID:     in.ID,
		Prefix:         prefix,
		Fragments:      fragments,
		CapNames:       capNamesToStrings(names),
		Env:            data.Env,
		Secrets:        secretsInfo,
		ExecTimeoutMs:  data.Limits.ExecTimeoutMs,
		MaxSteps:       data.Limits.MaxSteps,
		MaxOutputBytes: data.Limits.MaxOutputBytes,
		TTLSeconds:     data.TTLSeconds,
		NetExampleHost: netHost,
		Persistent:     true,
	})
	if err != nil {
		return instancePromptResp{fail(service.Internal(err))}, nil
	}
	return instancePromptResp{rawJSON{200, map[string]any{
		"instance_id":  in.ID,
		"prompt":       text,
		"capabilities": names,
		"tools": map[string]string{
			"exec":       prefix + "exec",
			"list_files": prefix + "list_files",
			"read_file":  prefix + "read_file",
		},
	}}}, nil
}

func capNamesToStrings(in []types.CapabilityName) []string {
	out := make([]string, len(in))
	for i, n := range in {
		out[i] = string(n)
	}
	return out
}

// files serves GET /instances/{id}/files?path=/work/... through the fs
// capability binding inside a gated "console" session.
func (s *strictImpl) Files(ctx context.Context, req gen.FilesRequestObject) (gen.FilesResponseObject, error) {
	ctx = realCtx(ctx)
	if _, e := needAuth(ctx); e != nil {
		return filesResp{*e}, nil
	}
	in, e := s.ownedInstance(ctx, req.Id)
	if e != nil {
		return filesResp{*e}, nil
	}
	if in.Status != types.InstanceRunning {
		return filesResp{fail(service.Errf(types.ErrCodeNotRunning, "instance not running"))}, nil
	}
	if !s.d.Manager.HasCapability(in.ID, string(types.CapFS)) {
		return filesResp{fail(service.Errf(types.ErrCodeNoFS, "instance has no fs capability"))}, nil
	}
	p := "/work"
	if req.Params.Path != nil && *req.Params.Path != "" {
		p = *req.Params.Path
	}
	var stat map[string]any
	var listing []any
	var content string
	err := s.d.Manager.WithSession(in.ID, func(s *engine.Session, gate *capability.Gate) error {
		fsv, ok := s.Predeclared[string(types.CapFS)]
		if !ok {
			return errors.New("no fs binding")
		}
		mod, ok := fsv.(starlark.HasAttrs)
		if !ok {
			return errors.New("bad fs binding")
		}
		thread := &starlark.Thread{Name: "console"}
		thread.SetLocal(capability.ContextKey, ctx)
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
		if sv, ok := readV.(starlark.String); ok {
			content = string(sv)
		}
		return nil
	})
	if err != nil {
		switch {
		case errors.Is(err, instance.ErrNotFound):
			return filesResp{fail(service.NotFound("instance not found"))}, nil
		case errors.Is(err, instance.ErrNotRunning):
			return filesResp{fail(service.Errf(types.ErrCodeNotRunning, "instance not running"))}, nil
		case strings.Contains(err.Error(), "does not exist"):
			return filesResp{fail(service.NotFound(err.Error()))}, nil
		default:
			return filesResp{fail(service.Errf(types.ErrCodeFSError, "%s", err.Error()))}, nil
		}
	}
	if d, _ := stat["is_dir"].(bool); d {
		if listing == nil {
			listing = []any{}
		}
		return filesResp{rawJSON{200, map[string]any{"entries": listing}}}, nil
	}
	return filesResp{rawJSON{200, map[string]any{
		"path": stat["path"], "content": s.d.Manager.Redact(in.ID, content),
	}}}, nil
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

func (s *strictImpl) ListExecutions(ctx context.Context, req gen.ListExecutionsRequestObject) (gen.ListExecutionsResponseObject, error) {
	ctx = realCtx(ctx)
	if _, e := needAuth(ctx); e != nil {
		return listExecutionsResp{*e}, nil
	}
	in, e := s.ownedInstance(ctx, req.Id)
	if e != nil {
		return listExecutionsResp{*e}, nil
	}
	limit := 0
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}
	lst, err := s.d.Store.ListExecutions(ctx, in.ID, limit)
	if err != nil {
		return listExecutionsResp{fail(service.Internal(err))}, nil
	}
	if lst == nil {
		lst = []*store.Execution{}
	}
	return listExecutionsResp{rawJSON{200, map[string]any{"executions": lst}}}, nil
}

// GetExecution returns one execution with its full code. Ownership is
// enforced via the execution's user_id: other users' execs are 404.
func (s *strictImpl) GetExecution(ctx context.Context, req gen.GetExecutionRequestObject) (gen.GetExecutionResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return getExecutionResp{*e}, nil
	}
	ex, err := s.d.Store.GetExecution(ctx, req.Id)
	if err != nil || ex.UserID != p.User.ID {
		return getExecutionResp{fail(service.NotFound("execution not found"))}, nil
	}
	return getExecutionResp{rawJSON{200, map[string]any{"execution": ex, "code": ex.Code}}}, nil
}

// --- audit ---

func (s *strictImpl) ListAudit(ctx context.Context, req gen.ListAuditRequestObject) (gen.ListAuditResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return listAuditResp{*e}, nil
	}
	var f auditsvc.Filter
	if req.Params.InstanceId != nil {
		f.InstanceID = *req.Params.InstanceId
	}
	if req.Params.ExecId != nil {
		f.ExecID = *req.Params.ExecId
	}
	if req.Params.Limit != nil {
		f.Limit = *req.Params.Limit
	}
	if req.Params.Before != nil {
		b := req.Params.Before.UTC()
		f.Before = &b
	}
	lst, err := s.d.Audit.List(ctx, actorOf(p), f)
	if err != nil {
		return listAuditResp{fail(err)}, nil
	}
	return listAuditResp{rawJSON{200, map[string]any{"events": lst}}}, nil
}

// --- policies ---

func (s *strictImpl) ListPolicies(ctx context.Context, _ gen.ListPoliciesRequestObject) (gen.ListPoliciesResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return listPoliciesResp{*e}, nil
	}
	lst, err := s.d.Policy.List(ctx, actorOf(p))
	if err != nil {
		return listPoliciesResp{fail(err)}, nil
	}
	return listPoliciesResp{rawJSON{200, map[string]any{"policies": lst}}}, nil
}

func (s *strictImpl) CreatePolicy(ctx context.Context, req gen.CreatePolicyRequestObject) (gen.CreatePolicyResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return createPolicyResp{*e}, nil
	}
	var name, rego string
	var enabled *bool
	if req.Body != nil {
		name, rego = req.Body.Name, req.Body.Rego
		enabled = req.Body.Enabled
	}
	pol, err := s.d.Policy.Create(ctx, actorOf(p), name, rego, enabled)
	if err != nil {
		return createPolicyResp{fail(err)}, nil
	}
	return createPolicyResp{rawJSON{201, map[string]any{"policy": pol}}}, nil
}

func (s *strictImpl) ValidatePolicy(ctx context.Context, req gen.ValidatePolicyRequestObject) (gen.ValidatePolicyResponseObject, error) {
	ctx = realCtx(ctx)
	if _, e := needAuth(ctx); e != nil {
		return validatePolicyResp{*e}, nil
	}
	var rego string
	if req.Body != nil {
		rego = req.Body.Rego
	}
	if err := s.d.Policy.ValidateRego(rego); err != nil {
		return validatePolicyResp{rawJSON{200, map[string]any{"valid": false, "error": err.Error()}}}, nil
	}
	return validatePolicyResp{rawJSON{200, map[string]any{"valid": true}}}, nil
}

func (s *strictImpl) GetPolicy(ctx context.Context, req gen.GetPolicyRequestObject) (gen.GetPolicyResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return getPolicyResp{*e}, nil
	}
	pol, err := s.d.Policy.Get(ctx, actorOf(p), req.Id)
	if err != nil {
		return getPolicyResp{fail(err)}, nil
	}
	return getPolicyResp{rawJSON{200, map[string]any{"policy": pol}}}, nil
}

func (s *strictImpl) UpdatePolicy(ctx context.Context, req gen.UpdatePolicyRequestObject) (gen.UpdatePolicyResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return updatePolicyResp{*e}, nil
	}
	var name, rego *string
	var enabled *bool
	if req.Body != nil {
		name, rego, enabled = req.Body.Name, req.Body.Rego, req.Body.Enabled
	}
	pol, err := s.d.Policy.Update(ctx, actorOf(p), req.Id, name, rego, enabled)
	if err != nil {
		return updatePolicyResp{fail(err)}, nil
	}
	return updatePolicyResp{rawJSON{200, map[string]any{"policy": pol}}}, nil
}

func (s *strictImpl) DeletePolicy(ctx context.Context, req gen.DeletePolicyRequestObject) (gen.DeletePolicyResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return deletePolicyResp{*e}, nil
	}
	if err := s.d.Policy.Delete(ctx, actorOf(p), req.Id); err != nil {
		return deletePolicyResp{fail(err)}, nil
	}
	return deletePolicyResp{rawJSON{200, map[string]any{"ok": true}}}, nil
}

// --- secrets vault (session only) ---

func secretsDisabled() *rawJSON {
	e := fail(service.Errf(types.ErrCodeSecretsDisabled, "secrets vault disabled (no --secret-key)"))
	return &e
}

func (s *strictImpl) ListSecrets(ctx context.Context, _ gen.ListSecretsRequestObject) (gen.ListSecretsResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return listSecretsResp{*e}, nil
	}
	lst, err := s.d.Vault.List(ctx, actorOf(p))
	if err != nil {
		return listSecretsResp{fail(err)}, nil
	}
	return listSecretsResp{rawJSON{200, map[string]any{"secrets": lst}}}, nil
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

func (s *strictImpl) CreateSecret(ctx context.Context, req gen.CreateSecretRequestObject) (gen.CreateSecretResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return createSecretResp{*e}, nil
	}
	var name, value string
	var domains []string
	if req.Body != nil {
		name, value = req.Body.Name, req.Body.Value
		if req.Body.AllowedDomains != nil {
			domains = *req.Body.AllowedDomains
		}
	}
	sec, err := s.d.Vault.Create(ctx, actorOf(p), name, value, domains)
	if err != nil {
		return createSecretResp{fail(err)}, nil
	}
	return createSecretResp{rawJSON{201, map[string]any{"secret": sec}}}, nil
}

func (s *strictImpl) UpdateSecret(ctx context.Context, req gen.UpdateSecretRequestObject) (gen.UpdateSecretResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return updateSecretResp{*e}, nil
	}
	var value *string
	var domains *[]string
	if req.Body != nil {
		value, domains = req.Body.Value, req.Body.AllowedDomains
	}
	sec, err := s.d.Vault.Update(ctx, actorOf(p), req.Id, value, domains)
	if err != nil {
		return updateSecretResp{fail(err)}, nil
	}
	return updateSecretResp{rawJSON{200, map[string]any{"secret": sec}}}, nil
}

func (s *strictImpl) DeleteSecret(ctx context.Context, req gen.DeleteSecretRequestObject) (gen.DeleteSecretResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return deleteSecretResp{*e}, nil
	}
	if err := s.d.Vault.Delete(ctx, actorOf(p), req.Id); err != nil {
		return deleteSecretResp{fail(err)}, nil
	}
	return deleteSecretResp{rawJSON{200, map[string]any{"ok": true}}}, nil
}
