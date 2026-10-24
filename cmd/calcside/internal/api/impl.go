package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"calcside/cmd/calcside/internal/api/dto"
	"calcside/cmd/calcside/internal/api/gen"
	"calcside/cmd/calcside/internal/auth"
	"calcside/cmd/calcside/internal/service"
	"calcside/cmd/calcside/internal/service/access"
	auditsvc "calcside/cmd/calcside/internal/service/audit"
	"calcside/internal/store"
	"calcside/internal/types"
)

var (
	_ gen.StrictServerInterface = (*strictImpl)(nil)
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

func optStr(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

// actorOf maps the transport principal onto the service-layer actor.
func actorOf(p *auth.Principal) service.Actor {
	return p.Actor()
}

// actor resolves the actor for a request that accepts a workspace
// parameter: sessions may act in any workspace they belong to; a key is
// fixed to its own workspace.
func (s *strictImpl) actor(ctx context.Context, p *auth.Principal, ws *string) (service.Actor, error) {
	a := p.Actor()
	if ws == nil || *ws == "" || *ws == a.WorkspaceID {
		return a, nil
	}
	if a.ViaKey() {
		return a, service.Forbidden("a key belongs to one workspace")
	}
	a.WorkspaceID = *ws
	if err := s.d.Access.Workspace(ctx, a, *ws, access.View); err != nil {
		return a, err
	}
	return a, nil
}

// Per-operation response wrappers (generated files are never edited).

type healthzResp struct{ rawJSON }

func (r healthzResp) VisitHealthzResponse(w http.ResponseWriter) error { return r.write(w) }

type authConfigResp struct{ rawJSON }

func (r authConfigResp) VisitAuthConfigResponse(w http.ResponseWriter) error { return r.write(w) }

type meResp struct{ rawJSON }

func (r meResp) VisitMeResponse(w http.ResponseWriter) error { return r.write(w) }

type changePasswordResp struct {
	rawJSON
	auth *auth.Service
}

func (r changePasswordResp) VisitChangePasswordResponse(w http.ResponseWriter) error {
	w.Header().Set("Cache-Control", "no-store")
	if r.auth != nil {
		r.auth.ClearSessionCookie(w)
	}
	return r.write(w)
}

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

type instanceInspectResp struct{ rawJSON }

func (r instanceInspectResp) VisitInstanceInspectResponse(w http.ResponseWriter) error {
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
	return healthzResp{rawJSON{http.StatusOK, dto.OK{OK: true}}}, nil
}

func (s *strictImpl) AuthConfig(ctx context.Context, _ gen.AuthConfigRequestObject) (gen.AuthConfigResponseObject, error) {
	ctx = realCtx(ctx)
	return authConfigResp{rawJSON{http.StatusOK, dto.AuthConfig{
		Google: s.d.GoogleEnabled, Basic: s.d.Basic != nil, DevMode: s.d.Dev, Secrets: s.d.Vault.Enabled(),
	}}}, nil
}

func (s *strictImpl) Me(ctx context.Context, _ gen.MeRequestObject) (gen.MeResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return meResp{*e}, nil
	}
	return meResp{rawJSON{http.StatusOK, dto.Me{
		User: dto.NewUser(p.User), ViaKey: p.ViaKey(), Kind: p.Kind,
	}}}, nil
}

func (s *strictImpl) ChangePassword(ctx context.Context, req gen.ChangePasswordRequestObject) (gen.ChangePasswordResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return changePasswordResp{rawJSON: *e}, nil
	}
	if req.Body == nil || req.Body.CurrentPassword == nil || req.Body.NewPassword == nil {
		return changePasswordResp{rawJSON: fail(service.BadRequest("password fields are required"))}, nil
	}
	if err := s.d.IAM.ChangePassword(ctx, actorOf(p), *req.Body.CurrentPassword, *req.Body.NewPassword); err != nil {
		return changePasswordResp{rawJSON: fail(err)}, nil
	}
	return changePasswordResp{rawJSON: rawJSON{http.StatusOK, dto.OK{OK: true}}, auth: s.d.Auth}, nil
}

// --- capabilities ---

func (s *strictImpl) Capabilities(ctx context.Context, _ gen.CapabilitiesRequestObject) (gen.CapabilitiesResponseObject, error) {
	ctx = realCtx(ctx)
	if _, e := needAuth(ctx); e != nil {
		return capabilitiesResp{*e}, nil
	}
	return capabilitiesResp{rawJSON{http.StatusOK, dto.CapabilitiesEnvelope{Capabilities: s.d.Catalog.Capabilities()}}}, nil
}

func (s *strictImpl) ListExtensions(ctx context.Context, _ gen.ListExtensionsRequestObject) (gen.ListExtensionsResponseObject, error) {
	ctx = realCtx(ctx)
	if _, e := needAuth(ctx); e != nil {
		return listExtensionsResp{*e}, nil
	}
	return listExtensionsResp{rawJSON{http.StatusOK, dto.ExtensionsEnvelope(s.d.Catalog.Extensions())}}, nil
}

// --- workspaces ---

type listWorkspacesResp struct{ rawJSON }

func (r listWorkspacesResp) VisitListWorkspacesResponse(w http.ResponseWriter) error {
	return r.write(w)
}

func (s *strictImpl) ListWorkspaces(ctx context.Context, _ gen.ListWorkspacesRequestObject) (gen.ListWorkspacesResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return listWorkspacesResp{*e}, nil
	}
	ms, err := s.d.Access.Memberships(ctx, actorOf(p))
	if err != nil {
		return listWorkspacesResp{fail(err)}, nil
	}
	out := make([]gen.Workspace, 0, len(ms))
	for _, m := range ms {
		out = append(out, gen.Workspace{Id: m.Workspace.ID, Name: m.Workspace.Name, Personal: m.Workspace.Personal, Role: string(m.Role)})
	}
	return listWorkspacesResp{rawJSON{200, gen.WorkspacesResponse{Workspaces: &out}}}, nil
}

// --- workspace management ---

type createWorkspaceResp struct{ rawJSON }

func (r createWorkspaceResp) VisitCreateWorkspaceResponse(w http.ResponseWriter) error {
	return r.write(w)
}

func (s *strictImpl) CreateWorkspace(ctx context.Context, req gen.CreateWorkspaceRequestObject) (gen.CreateWorkspaceResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return createWorkspaceResp{*e}, nil
	}
	var name string
	if req.Body != nil {
		name = req.Body.Name
	}
	w, err := s.d.Workspace.Create(ctx, actorOf(p), name)
	if err != nil {
		return createWorkspaceResp{fail(err)}, nil
	}
	return createWorkspaceResp{rawJSON{201, gen.WorkspaceResponse{
		Workspace: &gen.Workspace{Id: w.ID, Name: w.Name, Personal: w.Personal, Role: string(store.RoleOwner)},
	}}}, nil
}

type updateWorkspaceResp struct{ rawJSON }

func (r updateWorkspaceResp) VisitUpdateWorkspaceResponse(w http.ResponseWriter) error {
	return r.write(w)
}

func (s *strictImpl) UpdateWorkspace(ctx context.Context, req gen.UpdateWorkspaceRequestObject) (gen.UpdateWorkspaceResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return updateWorkspaceResp{*e}, nil
	}
	var name string
	if req.Body != nil {
		name = req.Body.Name
	}
	w, err := s.d.Workspace.Rename(ctx, actorOf(p), req.Id, name)
	if err != nil {
		return updateWorkspaceResp{fail(err)}, nil
	}
	return updateWorkspaceResp{rawJSON{200, gen.WorkspaceResponse{
		Workspace: &gen.Workspace{Id: w.ID, Name: w.Name, Personal: w.Personal},
	}}}, nil
}

type deleteWorkspaceResp struct{ rawJSON }

func (r deleteWorkspaceResp) VisitDeleteWorkspaceResponse(w http.ResponseWriter) error {
	return r.write(w)
}

func (s *strictImpl) DeleteWorkspace(ctx context.Context, req gen.DeleteWorkspaceRequestObject) (gen.DeleteWorkspaceResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return deleteWorkspaceResp{*e}, nil
	}
	if err := s.d.Workspace.Delete(ctx, actorOf(p), req.Id); err != nil {
		return deleteWorkspaceResp{fail(err)}, nil
	}
	return deleteWorkspaceResp{rawJSON{200, map[string]any{"ok": true}}}, nil
}

type listMembersResp struct{ rawJSON }

func (r listMembersResp) VisitListMembersResponse(w http.ResponseWriter) error { return r.write(w) }

func (s *strictImpl) ListMembers(ctx context.Context, req gen.ListMembersRequestObject) (gen.ListMembersResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return listMembersResp{*e}, nil
	}
	ms, err := s.d.Workspace.Members(ctx, actorOf(p), req.Id)
	if err != nil {
		return listMembersResp{fail(err)}, nil
	}
	out := make([]gen.Member, 0, len(ms))
	for _, m := range ms {
		out = append(out, gen.Member{UserId: m.UserID, Email: m.Email, Name: optStr(m.Name), Role: string(m.Role)})
	}
	return listMembersResp{rawJSON{200, gen.MembersResponse{Members: &out}}}, nil
}

type addMemberResp struct{ rawJSON }

func (r addMemberResp) VisitAddMemberResponse(w http.ResponseWriter) error { return r.write(w) }

func (s *strictImpl) AddMember(ctx context.Context, req gen.AddMemberRequestObject) (gen.AddMemberResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return addMemberResp{*e}, nil
	}
	if req.Body == nil {
		return addMemberResp{fail(service.Errf(types.ErrCodeBadRequest, "body required"))}, nil
	}
	m, err := s.d.Workspace.AddMember(ctx, actorOf(p), req.Id, req.Body.Email, types.Role(string(req.Body.Role)))
	if err != nil {
		return addMemberResp{fail(err)}, nil
	}
	return addMemberResp{rawJSON{201, gen.MemberResponse{Member: &gen.Member{
		UserId: m.UserID, Email: m.Email, Name: optStr(m.Name), Role: string(m.Role)}}}}, nil
}

type setMemberRoleResp struct{ rawJSON }

func (r setMemberRoleResp) VisitSetMemberRoleResponse(w http.ResponseWriter) error { return r.write(w) }

func (s *strictImpl) SetMemberRole(ctx context.Context, req gen.SetMemberRoleRequestObject) (gen.SetMemberRoleResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return setMemberRoleResp{*e}, nil
	}
	if req.Body == nil {
		return setMemberRoleResp{fail(service.Errf(types.ErrCodeBadRequest, "body required"))}, nil
	}
	if err := s.d.Workspace.SetRole(ctx, actorOf(p), req.Id, req.UserId, types.Role(string(req.Body.Role))); err != nil {
		return setMemberRoleResp{fail(err)}, nil
	}
	return setMemberRoleResp{rawJSON{200, map[string]any{"ok": true}}}, nil
}

type removeMemberResp struct{ rawJSON }

func (r removeMemberResp) VisitRemoveMemberResponse(w http.ResponseWriter) error { return r.write(w) }

func (s *strictImpl) RemoveMember(ctx context.Context, req gen.RemoveMemberRequestObject) (gen.RemoveMemberResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return removeMemberResp{*e}, nil
	}
	if err := s.d.Workspace.RemoveMember(ctx, actorOf(p), req.Id, req.UserId); err != nil {
		return removeMemberResp{fail(err)}, nil
	}
	return removeMemberResp{rawJSON{200, map[string]any{"ok": true}}}, nil
}

type listChangesResp struct{ rawJSON }

func (r listChangesResp) VisitListChangesResponse(w http.ResponseWriter) error { return r.write(w) }

func (s *strictImpl) ListChanges(ctx context.Context, req gen.ListChangesRequestObject) (gen.ListChangesResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return listChangesResp{*e}, nil
	}
	cs, err := s.d.Workspace.Changes(ctx, actorOf(p), req.Id)
	if err != nil {
		return listChangesResp{fail(err)}, nil
	}
	out := make([]gen.WorkspaceChange, 0, len(cs))
	for _, c := range cs {
		out = append(out, gen.WorkspaceChange{
			Id: c.ID, WorkspaceId: c.WorkspaceID, ByUserId: c.ByUserID,
			Change: string(c.Change), CreatedAt: c.CreatedAt,
			UserId: optStr(c.UserID), InstanceId: optStr(c.InstanceID), Role: optStr(string(c.Role)),
		})
	}
	return listChangesResp{rawJSON{200, gen.ChangesResponse{Changes: &out}}}, nil
}

// --- instance shares ---

type listSharesResp struct{ rawJSON }

func (r listSharesResp) VisitListSharesResponse(w http.ResponseWriter) error { return r.write(w) }

func (s *strictImpl) ListShares(ctx context.Context, req gen.ListSharesRequestObject) (gen.ListSharesResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return listSharesResp{*e}, nil
	}
	shs, err := s.d.Workspace.Shares(ctx, actorOf(p), req.Id)
	if err != nil {
		return listSharesResp{fail(err)}, nil
	}
	out := make([]gen.Share, 0, len(shs))
	for _, sh := range shs {
		out = append(out, gen.Share{InstanceId: sh.InstanceID, UserId: sh.UserID,
			Email: optStr(sh.Email), Name: optStr(sh.Name), Role: string(sh.Role),
			CreatedBy: optStr(sh.CreatedBy), CreatedAt: sh.CreatedAt})
	}
	return listSharesResp{rawJSON{200, gen.SharesResponse{Shares: &out}}}, nil
}

type putShareResp struct{ rawJSON }

func (r putShareResp) VisitPutShareResponse(w http.ResponseWriter) error { return r.write(w) }

func (s *strictImpl) PutShare(ctx context.Context, req gen.PutShareRequestObject) (gen.PutShareResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return putShareResp{*e}, nil
	}
	if req.Body == nil {
		return putShareResp{fail(service.Errf(types.ErrCodeBadRequest, "body required"))}, nil
	}
	sh, err := s.d.Workspace.PutShare(ctx, actorOf(p), req.Id, req.Body.Email, types.Role(string(req.Body.Role)))
	if err != nil {
		return putShareResp{fail(err)}, nil
	}
	return putShareResp{rawJSON{200, gen.ShareResponse{Share: &gen.Share{
		InstanceId: sh.InstanceID, UserId: sh.UserID, Email: optStr(sh.Email),
		Name: optStr(sh.Name), Role: string(sh.Role), CreatedBy: optStr(sh.CreatedBy),
		CreatedAt: sh.CreatedAt}}}}, nil
}

type deleteShareResp struct{ rawJSON }

func (r deleteShareResp) VisitDeleteShareResponse(w http.ResponseWriter) error { return r.write(w) }

func (s *strictImpl) DeleteShare(ctx context.Context, req gen.DeleteShareRequestObject) (gen.DeleteShareResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return deleteShareResp{*e}, nil
	}
	if err := s.d.Workspace.DeleteShare(ctx, actorOf(p), req.Id, req.UserId); err != nil {
		return deleteShareResp{fail(err)}, nil
	}
	return deleteShareResp{rawJSON{200, map[string]any{"ok": true}}}, nil
}

// --- api keys (session only) ---

func (s *strictImpl) ListKeys(ctx context.Context, req gen.ListKeysRequestObject) (gen.ListKeysResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return listKeysResp{*e}, nil
	}
	a, err := s.actor(ctx, p, req.Params.Workspace)
	if err != nil {
		return listKeysResp{fail(err)}, nil
	}
	keys, err := s.d.IAM.ListKeys(ctx, a)
	if err != nil {
		return listKeysResp{fail(err)}, nil
	}
	return listKeysResp{rawJSON{200, dto.KeysEnvelope{Keys: dto.NewAPIKeys(keys)}}}, nil
}

func (s *strictImpl) CreateKey(ctx context.Context, req gen.CreateKeyRequestObject) (gen.CreateKeyResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return createKeyResp{*e}, nil
	}
	var name string
	var expiresIn int64
	var spec json.RawMessage
	var maxInstances int
	var viewOnly bool
	var instanceID string
	if req.Body != nil {
		name = req.Body.Name
		if req.Body.ExpiresInSeconds != nil {
			expiresIn = *req.Body.ExpiresInSeconds
		}
		if req.Body.Spec != nil {
			if b, err := json.Marshal(req.Body.Spec); err == nil {
				spec = b
			}
		}
		if req.Body.MaxInstances != nil {
			maxInstances = *req.Body.MaxInstances
		}
		if req.Body.ViewOnly != nil {
			viewOnly = *req.Body.ViewOnly
		}
		if req.Body.InstanceId != nil {
			instanceID = *req.Body.InstanceId
		}
	}
	a, err := s.actor(ctx, p, req.Params.Workspace)
	if err != nil {
		return createKeyResp{fail(err)}, nil
	}
	key, secret, err := s.d.IAM.CreateKey(ctx, a, name, expiresIn, spec, maxInstances, viewOnly, instanceID)
	if err != nil {
		return createKeyResp{fail(err)}, nil
	}
	return createKeyResp{rawJSON{201, dto.CreatedKey{Key: dto.NewAPIKey(key), Secret: secret}}}, nil
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
	return deleteKeyResp{rawJSON{200, dto.OK{OK: true}}}, nil
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
	if req.Params.Shared != nil && *req.Params.Shared {
		shs, err := s.d.Access.SharedInstances(ctx, actorOf(p))
		if err != nil {
			return listInstancesResp{fail(err)}, nil
		}
		out := make([]dto.SharedInstance, 0, len(shs))
		for _, sh := range shs {
			in, ierr := s.d.Sandbox.Get(ctx, actorOf(p), sh.InstanceID)
			if ierr != nil {
				continue // unshared or gone since
			}
			w, _ := s.d.Workspace.Get(ctx, actorOf(p), in.WorkspaceID)
			entry := dto.SharedInstance{Instance: dto.NewInstance(in), Role: string(sh.Role)}
			if w != nil {
				entry.WorkspaceName = w.Name
			}
			out = append(out, entry)
		}
		return listInstancesResp{rawJSON{200, dto.SharedInstancesEnvelope{Instances: out}}}, nil
	}
	a, err := s.actor(ctx, p, req.Params.Workspace)
	if err != nil {
		return listInstancesResp{fail(err)}, nil
	}
	lst, err := s.d.Sandbox.List(ctx, a, status)
	if err != nil {
		return listInstancesResp{fail(err)}, nil
	}
	return listInstancesResp{rawJSON{200, dto.InstancesEnvelope{Instances: dto.NewInstances(lst)}}}, nil
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
	a, err := s.actor(ctx, p, req.Params.Workspace)
	if err != nil {
		return createInstanceResp{fail(err)}, nil
	}
	meta, err := s.d.Sandbox.Create(ctx, a, raw)
	if err != nil {
		return createInstanceResp{fail(err)}, nil
	}
	return createInstanceResp{rawJSON{201, dto.InstanceEnvelope{Instance: dto.NewInstance(meta)}}}, nil
}

func (s *strictImpl) GetInstance(ctx context.Context, req gen.GetInstanceRequestObject) (gen.GetInstanceResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return getInstanceResp{*e}, nil
	}
	in, err := s.d.Sandbox.Get(ctx, actorOf(p), req.Id)
	if err != nil {
		return getInstanceResp{fail(err)}, nil
	}
	return getInstanceResp{rawJSON{200, dto.InstanceEnvelope{Instance: dto.NewInstance(in)}}}, nil
}

func (s *strictImpl) DeleteInstance(ctx context.Context, req gen.DeleteInstanceRequestObject) (gen.DeleteInstanceResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return deleteInstanceResp{*e}, nil
	}
	if err := s.d.Sandbox.Delete(ctx, actorOf(p), req.Id); err != nil {
		return deleteInstanceResp{fail(err)}, nil
	}
	return deleteInstanceResp{rawJSON{200, dto.OK{OK: true}}}, nil
}

func (s *strictImpl) Keepalive(ctx context.Context, req gen.KeepaliveRequestObject) (gen.KeepaliveResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return keepaliveResp{*e}, nil
	}
	meta, err := s.d.Sandbox.Keepalive(ctx, actorOf(p), req.Id)
	if err != nil {
		return keepaliveResp{fail(err)}, nil
	}
	return keepaliveResp{rawJSON{200, dto.InstanceEnvelope{Instance: dto.NewInstance(meta)}}}, nil
}

// Exec a code snippet
// 1. loop-up or create an instance
func (s *strictImpl) Exec(ctx context.Context, req gen.ExecRequestObject) (gen.ExecResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return execResp{*e}, nil
	}
	var body gen.ExecRequest
	if req.Body != nil {
		body = *req.Body
	}
	var timeoutMs int64
	if body.TimeoutMs != nil {
		timeoutMs = *body.TimeoutMs
	}
	res, err := s.d.Sandbox.Exec(ctx, actorOf(p), req.Id, body.Code,
		time.Duration(timeoutMs)*time.Millisecond)
	if err != nil {
		return execResp{fail(err)}, nil
	}
	return execResp{rawJSON{200, dto.NewExecResult(res)}}, nil
}

// InstancePrompt renders the server-side agent system prompt for a live
// instance.
func (s *strictImpl) InstancePrompt(ctx context.Context, req gen.InstancePromptRequestObject) (gen.InstancePromptResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return instancePromptResp{*e}, nil
	}
	prefix := "calcside_"
	if req.Params.ToolPrefix != nil {
		prefix = *req.Params.ToolPrefix
	}
	v, err := s.d.Sandbox.Prompt(ctx, actorOf(p), req.Id, prefix)
	if err != nil {
		return instancePromptResp{fail(err)}, nil
	}
	return instancePromptResp{rawJSON{200, dto.NewInstancePrompt(v)}}, nil
}

// InstanceInspect snapshots a live instance's globals and resource
// usage for the console's inspect view.
func (s *strictImpl) InstanceInspect(ctx context.Context, req gen.InstanceInspectRequestObject) (gen.InstanceInspectResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return instanceInspectResp{*e}, nil
	}
	v, err := s.d.Sandbox.Inspect(ctx, actorOf(p), req.Id)
	if err != nil {
		return instanceInspectResp{fail(err)}, nil
	}
	return instanceInspectResp{rawJSON{200, dto.NewInstanceInspect(v)}}, nil
}

// files serves GET /instances/{id}/files?path=/work/... through the fs
// capability binding inside a gated "console" session.
func (s *strictImpl) Files(ctx context.Context, req gen.FilesRequestObject) (gen.FilesResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return filesResp{*e}, nil
	}
	path := "/work"
	if req.Params.Path != nil && *req.Params.Path != "" {
		path = *req.Params.Path
	}
	v, err := s.d.Sandbox.Browse(ctx, actorOf(p), req.Id, path, req.Params.ListOnly != nil && *req.Params.ListOnly)
	if err != nil {
		return filesResp{fail(err)}, nil
	}
	if v.IsDir {
		return filesResp{rawJSON{200, dto.FilesDir{Entries: dto.NewFileEntries(v.Entries)}}}, nil
	}
	return filesResp{rawJSON{200, dto.FilesFile{Path: v.Path, Content: v.Content}}}, nil
}

func (s *strictImpl) ListExecutions(ctx context.Context, req gen.ListExecutionsRequestObject) (gen.ListExecutionsResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return listExecutionsResp{*e}, nil
	}
	limit := 0
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}
	lst, err := s.d.Sandbox.ListExecutions(ctx, actorOf(p), req.Id, limit)
	if err != nil {
		return listExecutionsResp{fail(err)}, nil
	}
	return listExecutionsResp{rawJSON{200, dto.ExecutionsEnvelope{Executions: dto.NewExecutions(lst)}}}, nil
}

// GetExecution returns one execution with its full code. Ownership is
// enforced via the execution's user_id: other users' execs are 404.
func (s *strictImpl) GetExecution(ctx context.Context, req gen.GetExecutionRequestObject) (gen.GetExecutionResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return getExecutionResp{*e}, nil
	}
	ex, err := s.d.Sandbox.GetExecution(ctx, actorOf(p), req.Id)
	if err != nil {
		return getExecutionResp{fail(err)}, nil
	}
	return getExecutionResp{rawJSON{200, dto.NewExecutionDetail(ex)}}, nil
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
	a, err := s.actor(ctx, p, req.Params.Workspace)
	if err != nil {
		return listAuditResp{fail(err)}, nil
	}
	lst, err := s.d.Audit.List(ctx, a, f)
	if err != nil {
		return listAuditResp{fail(err)}, nil
	}
	return listAuditResp{rawJSON{200, dto.AuditEnvelope{Events: dto.NewAuditEvents(lst)}}}, nil
}

// --- policies ---

func (s *strictImpl) ListPolicies(ctx context.Context, req gen.ListPoliciesRequestObject) (gen.ListPoliciesResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return listPoliciesResp{*e}, nil
	}
	a, err := s.actor(ctx, p, req.Params.Workspace)
	if err != nil {
		return listPoliciesResp{fail(err)}, nil
	}
	lst, err := s.d.Policy.List(ctx, a)
	if err != nil {
		return listPoliciesResp{fail(err)}, nil
	}
	return listPoliciesResp{rawJSON{200, dto.PoliciesEnvelope{Policies: dto.NewPolicies(lst)}}}, nil
}

func (s *strictImpl) CreatePolicy(ctx context.Context, req gen.CreatePolicyRequestObject) (gen.CreatePolicyResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return createPolicyResp{*e}, nil
	}
	var name, rego string
	if req.Body != nil {
		name, rego = req.Body.Name, req.Body.Rego
	}
	a, err := s.actor(ctx, p, req.Params.Workspace)
	if err != nil {
		return createPolicyResp{fail(err)}, nil
	}
	pol, err := s.d.Policy.Create(ctx, a, name, rego)
	if err != nil {
		return createPolicyResp{fail(err)}, nil
	}
	return createPolicyResp{rawJSON{201, dto.PolicyEnvelope{Policy: dto.NewPolicy(pol)}}}, nil
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
		return validatePolicyResp{rawJSON{200, dto.ValidationResult{Valid: false, Error: err.Error()}}}, nil
	}
	return validatePolicyResp{rawJSON{200, dto.ValidationResult{Valid: true}}}, nil
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
	return getPolicyResp{rawJSON{200, dto.PolicyEnvelope{Policy: dto.NewPolicy(pol)}}}, nil
}

func (s *strictImpl) UpdatePolicy(ctx context.Context, req gen.UpdatePolicyRequestObject) (gen.UpdatePolicyResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return updatePolicyResp{*e}, nil
	}
	var name, rego *string
	if req.Body != nil {
		name, rego = req.Body.Name, req.Body.Rego
	}
	pol, err := s.d.Policy.Update(ctx, actorOf(p), req.Id, name, rego)
	if err != nil {
		return updatePolicyResp{fail(err)}, nil
	}
	return updatePolicyResp{rawJSON{200, dto.PolicyEnvelope{Policy: dto.NewPolicy(pol)}}}, nil
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
	return deletePolicyResp{rawJSON{200, dto.OK{OK: true}}}, nil
}

// --- secrets vault (session only) ---

func (s *strictImpl) ListSecrets(ctx context.Context, req gen.ListSecretsRequestObject) (gen.ListSecretsResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return listSecretsResp{*e}, nil
	}
	a, err := s.actor(ctx, p, req.Params.Workspace)
	if err != nil {
		return listSecretsResp{fail(err)}, nil
	}
	lst, err := s.d.Vault.List(ctx, a)
	if err != nil {
		return listSecretsResp{fail(err)}, nil
	}
	return listSecretsResp{rawJSON{200, dto.SecretsEnvelope{Secrets: dto.NewSecrets(lst)}}}, nil
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
	a, err := s.actor(ctx, p, req.Params.Workspace)
	if err != nil {
		return createSecretResp{fail(err)}, nil
	}
	sec, err := s.d.Vault.Create(ctx, a, name, value, domains)
	if err != nil {
		return createSecretResp{fail(err)}, nil
	}
	return createSecretResp{rawJSON{201, dto.SecretEnvelope{Secret: dto.NewSecret(sec)}}}, nil
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
	return updateSecretResp{rawJSON{200, dto.SecretEnvelope{Secret: dto.NewSecret(sec)}}}, nil
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
	return deleteSecretResp{rawJSON{200, dto.OK{OK: true}}}, nil
}

type editorMetadataResp struct{ rawJSON }

func (r editorMetadataResp) VisitEditorMetadataResponse(w http.ResponseWriter) error {
	return r.write(w)
}

type instanceCompletionsResp struct{ rawJSON }

func (r instanceCompletionsResp) VisitInstanceCompletionsResponse(w http.ResponseWriter) error {
	return r.write(w)
}

func (s *strictImpl) EditorMetadata(ctx context.Context, _ gen.EditorMetadataRequestObject) (gen.EditorMetadataResponseObject, error) {
	ctx = realCtx(ctx)
	if _, e := needAuth(ctx); e != nil {
		return editorMetadataResp{*e}, nil
	}
	return editorMetadataResp{rawJSON{http.StatusOK, dto.EditorMetadata(s.d.Catalog.Editor())}}, nil
}

func (s *strictImpl) InstanceCompletions(ctx context.Context, req gen.InstanceCompletionsRequestObject) (gen.InstanceCompletionsResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return instanceCompletionsResp{*e}, nil
	}
	v, err := s.d.Sandbox.Completions(ctx, actorOf(p), req.Id)
	if err != nil {
		return instanceCompletionsResp{fail(err)}, nil
	}
	return instanceCompletionsResp{rawJSON{http.StatusOK, (*dto.CompletionContext)(v)}}, nil
}
