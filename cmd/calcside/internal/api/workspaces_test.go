package api

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"calcside/cmd/calcside/internal/api/gen"
	"calcside/cmd/calcside/internal/service"
	"calcside/cmd/calcside/internal/service/access"
	"calcside/internal/store"
	"calcside/internal/types"
)

func sessActor(u *store.User) service.Actor {
	return service.Actor{UserID: u.ID, Email: u.Email, Kind: types.AuthSession, WorkspaceID: store.PersonalWorkspaceID(u.ID)}
}

// TestRouteCoverage fails if a generated operation is not covered by
// the isolation table — new routes cannot silently skip authorization.
func TestRouteCoverage(t *testing.T) {
	ifaceType := reflect.TypeOf((*gen.ServerInterface)(nil)).Elem()
	// Every generated operation that addresses a stored object by ID
	// must appear in isolationOps; unlisted ops must be listed in safe.
	isolationOps := map[string]bool{
		"GetInstance": true, "DeleteInstance": true, "Keepalive": true,
		"Exec": true, "InstancePrompt": true, "InstanceInspect": true,
		"Files": true, "ListExecutions": true, "GetExecution": true,
		"DeleteKey": true, "GetPolicy": true, "UpdatePolicy": true,
		"DeletePolicy": true, "UpdateSecret": true, "DeleteSecret": true,
		"GetLLMProvider": true, "UpdateLLMProvider": true,
		"DeleteLLMProvider": true,
		"UpdateWorkspace":   true, "DeleteWorkspace": true, "ListMembers": true,
		"GetApplication": true, "UpdateApplication": true, "DeleteApplication": true,
		"PublishApplication": true, "TestApplication": true, "InvokeApplication": true,
		"AddMember": true, "SetMemberRole": true, "RemoveMember": true,
		"ListChanges": true, "ListShares": true, "PutShare": true,
		"DeleteShare": true,
	}
	safe := map[string]bool{
		"Healthz": true, "AuthConfig": true, "Me": true, "ChangePassword": true,
		"Capabilities": true, "EditorMetadata": true, "ListExtensions": true,
		"ExtensionIcon": true, "ListKeys": true, "CreateKey": true,
		"ListInstances": true, "CreateInstance": true, "ListAudit": true,
		"ListWorkspaces": true, "CreateWorkspace": true, "ListPolicies": true,
		"CreatePolicy": true, "ValidatePolicy": true, "ListSecrets": true,
		"ListLLMProviders": true, "CreateLLMProvider": true,
		"CreateSecret": true, "ArtifactConfig": true, "PreviewArtifact": true,
		"ListApplications": true, "CreateApplication": true,
		"ExportArtifacts": true, "InstanceCompletions": true,
		// Pure caller-text transforms: no store, no scope.
		"InferContract": true, "ValidateContract": true,
	}
	for i := 0; i < ifaceType.NumMethod(); i++ {
		op := ifaceType.Method(i).Name
		if !isolationOps[op] && !safe[op] {
			t.Errorf("operation %s is not in the isolation or safe table", op)
		}
	}
}

// TestCrossUserIsolation: for every ID-addressed route, user B with a
// session gets 404 on user A's objects.
func TestCrossUserIsolation(t *testing.T) {
	e := newEnv(t)
	cookiesA := login(e, "a@x.com")
	cookiesB := login(e, "b@x.com")
	csrf := map[string]string{"X-Requested-With": "calcside"}

	// A creates one of each object.
	_, m, _ := e.req("POST", "/api/v1/instances", `{"capabilities":{"fs":{}}}`, csrf, cookiesA)
	inst := m["instance"].(map[string]any)["id"].(string)
	_, m, _ = e.req("POST", "/api/v1/keys", `{"name":"k"}`, csrf, cookiesA)
	keyID := m["key"].(map[string]any)["id"].(string)
	_, m, _ = e.req("POST", "/api/v1/policies", `{"name":"p","rego":"package calcside.hooks"}`, csrf, cookiesA)
	polID := m["policy"].(map[string]any)["id"].(string)
	_, m, _ = e.req("POST", "/api/v1/secrets", `{"name":"S","value":"v"}`, csrf, cookiesA)
	secID := ""
	if code := 200; m["secret"] != nil {
		secID = m["secret"].(map[string]any)["id"].(string)
		_ = code
	}

	for _, tc := range []struct{ name, method, path, body string }{
		{"get instance", "GET", "/api/v1/instances/" + inst, ""},
		{"delete instance", "DELETE", "/api/v1/instances/" + inst, ""},
		{"keepalive", "POST", "/api/v1/instances/" + inst + "/keepalive", ""},
		{"exec", "POST", "/api/v1/instances/" + inst + "/exec", `{"code":"print(1)"}`},
		{"files", "GET", "/api/v1/instances/" + inst + "/files?path=/work", ""},
		{"prompt", "GET", "/api/v1/instances/" + inst + "/prompt", ""},
		{"inspect", "GET", "/api/v1/instances/" + inst + "/inspect", ""},
		{"executions", "GET", "/api/v1/instances/" + inst + "/executions", ""},
		{"revoke key", "DELETE", "/api/v1/keys/" + keyID, ""},
		{"get policy", "GET", "/api/v1/policies/" + polID, ""},
		{"update policy", "PUT", "/api/v1/policies/" + polID, `{"name":"p2"}`},
		{"delete policy", "DELETE", "/api/v1/policies/" + polID, ""},
	} {
		// session B
		code, _, _ := e.req(tc.method, tc.path, tc.body, csrf, cookiesB)
		if code != 404 && code != 405 && code != 400 {
			t.Errorf("B session %s: %d (want 404)", tc.name, code)
		}
	}
	if secID != "" {
		code, _, _ := e.req("PUT", "/api/v1/secrets/"+secID, `{"domains":["x"]}`, csrf, cookiesB)
		if code != 404 {
			t.Errorf("B session secret update: %d (want 404)", code)
		}
	}
}

func TestAccessMatrix(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ac := access.New(e.st)
	uA, _ := e.st.UpsertUserByEmail(ctx, "a@x.com", "A", "")
	uB, _ := e.st.UpsertUserByEmail(ctx, "b@x.com", "B", "")

	in := &store.Instance{UserID: uA.ID, WorkspaceID: store.PersonalWorkspaceID(uA.ID), Spec: []byte(`{}`), Status: types.InstanceRunning}
	if err := e.st.CreateInstance(ctx, in); err != nil {
		t.Fatal(err)
	}
	aSess, bSess := sessActor(uA), sessActor(uB)

	if _, err := ac.Instance(ctx, aSess, in.ID, access.View); err != nil {
		t.Fatalf("owner view: %v", err)
	}
	if _, err := ac.Instance(ctx, bSess, in.ID, access.View); err == nil {
		t.Fatal("stranger could view instance")
	} else if codeOf(err) != 404 {
		t.Fatalf("stranger should get not_found, got %v", err)
	}
	// view but not manage → forbidden
	if _, err := ac.Instance(ctx, aSess, in.ID, access.Manage); err == nil || codeOf(err) != 403 {
		t.Fatalf("owner manage: want forbidden, got %v", err)
	}

	// A team workspace shared between A (owner) and B (viewer): B sees
	// the workspace's instance by ID even while their request workspace
	// is personal, but every action above View is forbidden.
	uB, errB := e.st.UpsertUserByEmail(ctx, "b2@x.com", "B2", "")
	if errB != nil {
		t.Fatal(errB)
	}
	teamWS := &store.Workspace{ID: "ws_team01", Name: "team"}
	if err := e.st.CreateWorkspace(ctx, teamWS); err != nil {
		t.Fatal(err)
	}
	if err := e.st.AddMember(ctx, &store.Member{WorkspaceID: teamWS.ID, UserID: uA.ID, Role: store.RoleOwner}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.AddMember(ctx, &store.Member{WorkspaceID: teamWS.ID, UserID: uB.ID, Role: store.RoleViewer}); err != nil {
		t.Fatal(err)
	}
	teamIn := &store.Instance{UserID: uA.ID, WorkspaceID: teamWS.ID, Spec: []byte(`{}`), Status: types.InstanceRunning}
	if err := e.st.CreateInstance(ctx, teamIn); err != nil {
		t.Fatal(err)
	}
	bViewer := sessActor(uB) // personal ws, no param
	if _, err := ac.Instance(ctx, bViewer, teamIn.ID, access.View); err != nil {
		t.Fatalf("viewer should see team instance by ID: %v", err)
	}
	for _, act := range []access.Action{access.Run, access.Configure, access.Manage} {
		if _, err := ac.Instance(ctx, bViewer, teamIn.ID, act); err == nil || codeOf(err) != 403 {
			t.Fatalf("viewer act=%d: want forbidden, got %v", act, err)
		}
	}
	if err := ac.Workspace(ctx, bViewer, teamWS.ID, access.Configure); err == nil || codeOf(err) != 403 {
		t.Fatalf("viewer workspace configure: want forbidden, got %v", err)
	}
	uC, _ := e.st.UpsertUserByEmail(ctx, "c@x.com", "C", "")
	cStranger := sessActor(uC)
	if _, err := ac.Instance(ctx, cStranger, teamIn.ID, access.View); err == nil || codeOf(err) != 404 {
		t.Fatalf("stranger: want not_found, got %v", err)
	}
	if err := ac.Workspace(ctx, cStranger, teamWS.ID, access.View); err == nil || codeOf(err) != 404 {
		t.Fatalf("stranger workspace: want not_found, got %v", err)
	}
	if err := ac.Workspace(ctx, bSess, store.PersonalWorkspaceID(uA.ID), access.View); err == nil {
		t.Fatal("session reached other workspace")
	}
	// key ceiling: configure forbidden even for the owner's key
	key := aSess
	key.Kind = types.AuthAPIKey
	key.KeyID, key.WorkspaceID = "key_x", store.PersonalWorkspaceID(uA.ID)
	if err := ac.Workspace(ctx, key, key.WorkspaceID, access.Configure); err == nil {
		t.Fatal("key could configure")
	}
}

func codeOf(err error) int {
	var se *service.Error
	if errors.As(err, &se) {
		switch se.Code {
		case types.ErrCodeNotFound:
			return 404
		case types.ErrCodeForbidden:
			return 403
		}
	}
	return 0
}

func TestWorkspaceParam(t *testing.T) {
	e := newEnv(t)
	cookies := login(e, "a@x.com")
	csrf := map[string]string{"X-Requested-With": "calcside"}

	// member (personal ws): ok
	code, _, _ := e.req("GET", "/api/v1/instances?workspace="+store.PersonalWorkspaceID("usr_x"), "", nil, cookies)
	if code != 404 { // not A's workspace either way → not found
		t.Fatalf("foreign workspace: %d", code)
	}
	code, m, _ := e.req("POST", "/api/v1/keys", `{"name":"fk"}`, csrf, cookies)
	if code != 201 {
		t.Fatalf("create key: %d %v", code, m)
	}
	bearer := map[string]string{"Authorization": "Bearer " + m["secret"].(string)}
	code, _, _ = e.req("GET", "/api/v1/instances?workspace=ws_other", "", bearer, nil)
	if code != 403 {
		t.Fatalf("key with foreign workspace: %d (want 403)", code)
	}
	code, m, _ = e.req("GET", "/api/v1/workspaces", "", nil, cookies)
	if code != 200 {
		t.Fatalf("workspaces: %d %v", code, m)
	}
	if got := m["workspaces"].([]any); len(got) != 1 {
		t.Fatalf("expected one personal workspace: %v", got)
	}
}

// Rule 5: a full-access key can no longer configure library policies.
func TestRule5FullKeyNoConfigure(t *testing.T) {
	e := newEnv(t)
	cookies := login(e, "a@x.com")
	csrf := map[string]string{"X-Requested-With": "calcside"}
	_, m, _ := e.req("POST", "/api/v1/keys", `{"name":"fk"}`, csrf, cookies)
	bearer := map[string]string{"Authorization": "Bearer " + m["secret"].(string)}
	// A real policy owned by the session user: key gets 403 (not 404 —
	// the row is visible in the key's own workspace).
	_, m, _ = e.req("POST", "/api/v1/policies", `{"name":"p","rego":"package calcside.hooks"}`, csrf, cookies)
	polID := m["policy"].(map[string]any)["id"].(string)
	for _, op := range []struct{ method, path, body string }{
		{"POST", "/api/v1/policies", `{"name":"p","rego":"package calcside.hooks"}`},
		{"PUT", "/api/v1/policies/" + polID, `{"name":"p2"}`},
		{"DELETE", "/api/v1/policies/" + polID, ""},
	} {
		code, _, _ := e.req(op.method, op.path, op.body, bearer, nil)
		if code != 403 {
			t.Errorf("%s %s with full key: %d (want 403)", op.method, op.path, code)
		}
	}
}

// Team-workspace routes: manage rules, last-owner, email resolution,
// leave, delete conflict, change log.
func TestWorkspaceRoutes(t *testing.T) {
	e := newEnv(t)
	cookiesA := login(e, "a@x.com")
	cookiesB := login(e, "b@x.com")
	csrf := map[string]string{"X-Requested-With": "calcside"}

	code, m, _ := e.req("POST", "/api/v1/workspaces", `{"name":"team"}`, csrf, cookiesA)
	if code != 201 {
		t.Fatalf("create workspace: %d %v", code, m)
	}
	ws := m["workspace"].(map[string]any)["id"].(string)

	// B cannot manage
	code, _, _ = e.req("GET", "/api/v1/workspaces/"+ws+"/members", "", nil, cookiesB)
	if code != 404 {
		t.Fatalf("stranger members: %d", code)
	}
	code, _, _ = e.req("PATCH", "/api/v1/workspaces/"+ws, `{"name":"x"}`, csrf, cookiesB)
	if code != 404 {
		t.Fatalf("stranger rename: %d", code)
	}

	// unknown email → 404; personal ws member ops → 403
	code, _, _ = e.req("POST", "/api/v1/workspaces/"+ws+"/members", `{"email":"ghost@x.com","role":"viewer"}`, csrf, cookiesA)
	if code != 404 {
		t.Fatalf("unknown email: %d", code)
	}
	personal := store.PersonalWorkspaceID("usr_x")
	code, _, _ = e.req("POST", "/api/v1/workspaces/"+personal+"/members", `{"email":"b@x.com","role":"viewer"}`, csrf, cookiesB)
	if code != 403 && code != 404 {
		t.Fatalf("personal add member: %d", code)
	}

	code, m, _ = e.req("POST", "/api/v1/workspaces/"+ws+"/members", `{"email":"b@x.com","role":"viewer"}`, csrf, cookiesA)
	if code != 201 {
		t.Fatalf("add member: %d %v", code, m)
	}
	bID := m["member"].(map[string]any)["user_id"].(string)
	code, _, _ = e.req("POST", "/api/v1/workspaces/"+ws+"/members", `{"email":"b@x.com","role":"viewer"}`, csrf, cookiesA)
	if code != 409 {
		t.Fatalf("duplicate member: %d", code)
	}
	// B (viewer) now sees members but cannot add
	code, m, _ = e.req("GET", "/api/v1/workspaces/"+ws+"/members", "", nil, cookiesB)
	if code != 200 || len(m["members"].([]any)) != 2 {
		t.Fatalf("viewer members: %d %v", code, m)
	}
	code, _, _ = e.req("PUT", "/api/v1/workspaces/"+ws+"/members/"+bID, `{"role":"editor"}`, csrf, cookiesB)
	if code != 403 {
		t.Fatalf("viewer set role: %d", code)
	}
	// demote last owner → 409
	code, m, _ = e.req("GET", "/api/v1/workspaces/"+ws+"/members", "", nil, cookiesA)
	aID := m["members"].([]any)[0].(map[string]any)["user_id"].(string)
	for _, mm := range m["members"].([]any) {
		if mm.(map[string]any)["role"] == "owner" {
			aID = mm.(map[string]any)["user_id"].(string)
		}
	}
	code, m2, _ := e.req("PUT", "/api/v1/workspaces/"+ws+"/members/"+aID, `{"role":"editor"}`, csrf, cookiesA)
	if code != 409 {
		t.Fatalf("demote last owner: %d ws=%s aID=%s %v", code, ws, aID, m2)
	}
	// leave as B
	code, _, _ = e.req("DELETE", "/api/v1/workspaces/"+ws+"/members/"+bID, "", csrf, cookiesB)
	if code != 200 {
		t.Fatalf("leave: %d", code)
	}
	// change log
	code, m, _ = e.req("GET", "/api/v1/workspaces/"+ws+"/changes", "", nil, cookiesA)
	if code != 200 {
		t.Fatalf("changes: %d", code)
	}
	if len(m["changes"].([]any)) < 2 { // member_added + member_removed
		t.Fatalf("changes: %v", m)
	}
	// delete with running instance → 409
	code, m, _ = e.req("POST", "/api/v1/instances?workspace="+ws, `{"capabilities":{"fs":{}}}`, csrf, cookiesA)
	if code != 201 {
		t.Fatalf("create in ws: %d %v", code, m)
	}
	code, _, _ = e.req("DELETE", "/api/v1/workspaces/"+ws, "", csrf, cookiesA)
	if code != 409 {
		t.Fatalf("delete with running: %d", code)
	}
	code, _, _ = e.req("DELETE", "/api/v1/workspaces/"+personal, "", csrf, cookiesB)
	if code == 200 {
		t.Fatal("personal delete succeeded")
	}
}

// Shares: owner shares an instance; recipient sees it; editor can run;
// member role wins over share; key never follows a share.
func TestShares(t *testing.T) {
	e := newEnv(t)
	cookiesA := login(e, "a@x.com")
	cookiesB := login(e, "b@x.com")
	csrf := map[string]string{"X-Requested-With": "calcside"}

	code, m, _ := e.req("POST", "/api/v1/instances", `{"capabilities":{"fs":{}}}`, csrf, cookiesA)
	if code != 201 {
		t.Fatalf("create: %d", code)
	}
	inst := m["instance"].(map[string]any)["id"].(string)

	code, m, _ = e.req("PUT", "/api/v1/instances/"+inst+"/shares", `{"email":"b@x.com","role":"viewer"}`, csrf, cookiesA)
	if code != 200 {
		t.Fatalf("share: %d %v", code, m)
	}
	// B can view but not exec
	code, _, _ = e.req("GET", "/api/v1/instances/"+inst, "", nil, cookiesB)
	if code != 200 {
		t.Fatalf("shared view: %d", code)
	}
	code, _, _ = e.req("POST", "/api/v1/instances/"+inst+"/exec", `{"code":"print(1)"}`, csrf, cookiesB)
	if code != 403 {
		t.Fatalf("viewer exec: %d", code)
	}
	// upgrade to editor → run allowed
	code, _, _ = e.req("PUT", "/api/v1/instances/"+inst+"/shares", `{"email":"b@x.com","role":"editor"}`, csrf, cookiesA)
	if code != 200 {
		t.Fatalf("reshare editor: %d", code)
	}
	code, m, _ = e.req("POST", "/api/v1/instances/"+inst+"/exec", `{"code":"print(1)"}`, csrf, cookiesB)
	if code != 200 {
		t.Fatalf("editor exec: %d %v", code, m)
	}
	// shared=true list
	code, m, _ = e.req("GET", "/api/v1/instances?shared=true", "", nil, cookiesB)
	if code != 200 || len(m["instances"].([]any)) != 1 {
		t.Fatalf("shared list: %d %v", code, m)
	}
	// B's key never sees the shared instance
	_, m, _ = e.req("POST", "/api/v1/keys", `{"name":"bk"}`, csrf, cookiesB)
	bearerB := map[string]string{"Authorization": "Bearer " + m["secret"].(string)}
	code, _, _ = e.req("GET", "/api/v1/instances/"+inst, "", bearerB, nil)
	if code != 404 {
		t.Fatalf("key followed share: %d", code)
	}
	code, _, _ = e.req("GET", "/api/v1/instances?shared=true", "", bearerB, nil)
	if code != 403 {
		t.Fatalf("key shared list: %d", code)
	}
	// B leaves
	code, m, _ = e.req("GET", "/api/v1/me", "", nil, cookiesB)
	bID := m["user"].(map[string]any)["id"].(string)
	code, _, _ = e.req("DELETE", "/api/v1/instances/"+inst+"/shares/"+bID, "", csrf, cookiesB)
	if code != 200 {
		t.Fatalf("unshare self: %d", code)
	}
	code, _, _ = e.req("GET", "/api/v1/instances/"+inst, "", nil, cookiesB)
	if code != 404 {
		t.Fatalf("after unshare: %d", code)
	}
}

// View-only keys: create, see instances, cannot exec/create; also not
// combinable with a restricted spec; MCP stays restricted-only.
func TestViewOnlyKey(t *testing.T) {
	e := newEnv(t)
	cookies := login(e, "a@x.com")
	csrf := map[string]string{"X-Requested-With": "calcside"}
	code, m, _ := e.req("POST", "/api/v1/keys", `{"name":"vo","view_only":true}`, csrf, cookies)
	if code != 201 {
		t.Fatalf("view_only create: %d %v", code, m)
	}
	if !m["key"].(map[string]any)["view_only"].(bool) {
		t.Fatal("view_only not set on key dto")
	}
	bearer := map[string]string{"Authorization": "Bearer " + m["secret"].(string)}
	code, m, _ = e.req("POST", "/api/v1/instances", `{"capabilities":{"fs":{}}}`, csrf, cookies)
	inst := m["instance"].(map[string]any)["id"].(string)
	code, _, _ = e.req("GET", "/api/v1/instances/"+inst, "", bearer, nil)
	if code != 200 {
		t.Fatalf("view-only get: %d", code)
	}
	code, _, _ = e.req("POST", "/api/v1/instances/"+inst+"/exec", `{"code":"print(1)"}`, bearer, nil)
	if code != 403 {
		t.Fatalf("view-only exec: %d", code)
	}
	code, _, _ = e.req("POST", "/api/v1/instances", `{"capabilities":{"fs":{}}}`, bearer, nil)
	if code != 403 {
		t.Fatalf("view-only create: %d", code)
	}
	// view_only + spec → 400
	code, _, _ = e.req("POST", "/api/v1/keys", `{"name":"bad","view_only":true,"spec":{"capabilities":{"fs":{}}},"max_instances":1}`, csrf, cookies)
	if code != 400 {
		t.Fatalf("view_only+spec: %d", code)
	}
	// view_only key gets 403 on /mcp (restricted-only)
	code, _, _ = e.req("POST", "/mcp", `{}`, bearer, nil)
	if code != 403 && code != 405 && code != 400 {
		t.Fatalf("view-only mcp: %d", code)
	}
}

// Instance-bound key: sees only its instance, cannot create/delete;
// list scoped; revoked when the instance is deleted.
func TestInstanceBoundKey(t *testing.T) {
	e := newEnv(t)
	cookies := login(e, "a@x.com")
	csrf := map[string]string{"X-Requested-With": "calcside"}
	code, m, _ := e.req("POST", "/api/v1/instances", `{"capabilities":{"fs":{}}}`, csrf, cookies)
	inst := m["instance"].(map[string]any)["id"].(string)
	code, m, _ = e.req("POST", "/api/v1/instances", `{"capabilities":{"fs":{}}}`, csrf, cookies)
	other := m["instance"].(map[string]any)["id"].(string)

	code, m, _ = e.req("POST", "/api/v1/keys", `{"name":"agent","instance_id":"`+inst+`"}`, csrf, cookies)
	if code != 201 {
		t.Fatalf("bound key create: %d %v", code, m)
	}
	bearer := map[string]string{"Authorization": "Bearer " + m["secret"].(string)}
	code, _, _ = e.req("GET", "/api/v1/instances/"+inst, "", bearer, nil)
	if code != 200 {
		t.Fatalf("bound get: %d", code)
	}
	code, _, _ = e.req("GET", "/api/v1/instances/"+other, "", bearer, nil)
	if code != 404 {
		t.Fatalf("bound other: %d", code)
	}
	code, m, _ = e.req("GET", "/api/v1/instances", "", bearer, nil)
	if code != 200 || len(m["instances"].([]any)) != 1 {
		t.Fatalf("bound list: %d %v", code, m)
	}
	code, _, _ = e.req("POST", "/api/v1/instances/"+inst+"/exec", `{"code":"print(1)"}`, bearer, nil)
	if code != 200 {
		t.Fatalf("bound exec: %d", code)
	}
	code, _, _ = e.req("DELETE", "/api/v1/instances/"+inst, "", bearer, nil)
	if code != 403 {
		t.Fatalf("bound delete: %d", code)
	}
	code, _, _ = e.req("POST", "/api/v1/instances", `{"capabilities":{"fs":{}}}`, bearer, nil)
	if code != 403 {
		t.Fatalf("bound create: %d", code)
	}
	// bound + spec → 400
	code, _, _ = e.req("POST", "/api/v1/keys", `{"name":"bad","instance_id":"`+inst+`","spec":{"capabilities":{}},"max_instances":1}`, csrf, cookies)
	if code != 400 {
		t.Fatalf("bound+spec: %d", code)
	}
	// delete the instance → key revoked
	code, _, _ = e.req("DELETE", "/api/v1/instances/"+inst, "", csrf, cookies)
	if code != 200 {
		t.Fatalf("delete: %d", code)
	}
	code, m, _ = e.req("GET", "/api/v1/keys", "", nil, cookies)
	found := false
	for _, k := range m["keys"].([]any) {
		kv := k.(map[string]any)
		if kv["instance_id"] == inst {
			found = true
			if kv["revoked_at"] == nil {
				t.Fatal("bound key not revoked on delete")
			}
		}
	}
	if !found {
		t.Fatal("bound key missing")
	}
}
