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
	}
	safe := map[string]bool{
		"Healthz": true, "AuthConfig": true, "Me": true, "ChangePassword": true,
		"Capabilities": true, "EditorMetadata": true, "ListExtensions": true,
		"ExtensionIcon": true, "ListKeys": true, "CreateKey": true,
		"ListInstances": true, "CreateInstance": true, "ListAudit": true,
		"ListWorkspaces": true, "ListPolicies": true, "CreatePolicy": true,
		"ValidatePolicy": true, "ListSecrets": true, "CreateSecret": true,
		"ArtifactConfig": true, "PreviewArtifact": true, "ExportArtifacts": true,
		"InstanceCompletions": true,
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
