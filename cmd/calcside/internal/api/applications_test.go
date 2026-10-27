package api

import (
	"context"
	"strings"
	"testing"

	"calcside/internal/store"
)

// Application endpoint isolation: every ID-addressed route is checked
// against real authn/authz — not just listed in the coverage table.
func TestApplicationIsolation(t *testing.T) {
	e := newEnv(t)
	cookiesA := login(e, "a@x.com")
	cookiesB := login(e, "b@x.com")
	csrf := map[string]string{"X-Requested-With": "calcside"}

	_, m, _ := e.req("POST", "/api/v1/applications", `{"name":"svc"}`, csrf, cookiesA)
	appID := m["application"].(map[string]any)["id"].(string)

	for _, tc := range []struct{ name, method, path, body string }{
		{"get", "GET", "/api/v1/applications/" + appID, ""},
		{"update", "PUT", "/api/v1/applications/" + appID, `{"version":1,"name":"x","code":"y"}`},
		{"delete", "DELETE", "/api/v1/applications/" + appID + "?version=1", ""},
		{"publish", "POST", "/api/v1/applications/" + appID + "/publish", `{"version":1}`},
		{"test", "POST", "/api/v1/applications/" + appID + "/test", `{"version":1,"input":{}}`},
		{"invoke", "POST", "/api/v1/applications/" + appID + "/invoke", `{}`},
	} {
		code, _, _ := e.req(tc.method, tc.path, tc.body, csrf, cookiesB)
		if code != 404 {
			t.Fatalf("%s foreign workspace: %d", tc.name, code)
		}
	}

	// unauthenticated — all 8 operations
	for _, tc := range []struct{ name, method, path, body string }{
		{"list", "GET", "/api/v1/applications", ""},
		{"create", "POST", "/api/v1/applications", `{"name":"x"}`},
		{"get", "GET", "/api/v1/applications/" + appID, ""},
		{"update", "PUT", "/api/v1/applications/" + appID, `{"version":1,"name":"x","code":"y"}`},
		{"delete", "DELETE", "/api/v1/applications/" + appID + "?version=1", ""},
		{"publish", "POST", "/api/v1/applications/" + appID + "/publish", `{"version":1}`},
		{"test", "POST", "/api/v1/applications/" + appID + "/test", `{"version":1,"input":{}}`},
		{"invoke", "POST", "/api/v1/applications/" + appID + "/invoke", `{}`},
	} {
		code, _, _ := e.req(tc.method, tc.path, tc.body, nil, nil)
		if code != 401 {
			t.Fatalf("unauth %s: %d", tc.name, code)
		}
	}

	// session mutations without CSRF
	for _, tc := range []struct{ name, method, path, body string }{
		{"create", "POST", "/api/v1/applications", `{"name":"x"}`},
		{"update", "PUT", "/api/v1/applications/" + appID, `{"version":1,"name":"x","code":"y"}`},
		{"delete", "DELETE", "/api/v1/applications/" + appID + "?version=1", ""},
		{"publish", "POST", "/api/v1/applications/" + appID + "/publish", `{"version":1}`},
		{"test", "POST", "/api/v1/applications/" + appID + "/test", `{"version":1,"input":{}}`},
		{"invoke", "POST", "/api/v1/applications/" + appID + "/invoke", `{}`},
	} {
		code, _, _ := e.req(tc.method, tc.path, tc.body, nil, cookiesA)
		if code != 403 {
			t.Fatalf("no-CSRF %s: %d", tc.name, code)
		}
	}

	// session list is workspace-scoped — the app's personal workspace is
	// different from B's, so the id never leaks across.
	_, m, _ = e.req("GET", "/api/v1/applications", "", nil, cookiesB)
	if arr, ok := m["applications"].([]any); !ok || len(arr) != 0 {
		t.Fatalf("foreign workspace list leaked: %v", m)
	}
}

// A view-only key can read but not mutate; a bound key sees no apps at
// all.
func TestApplicationKeyCeiling(t *testing.T) {
	e := newEnv(t)
	cookies := login(e, "a@x.com")
	csrf := map[string]string{"X-Requested-With": "calcside"}
	_, m, _ := e.req("POST", "/api/v1/applications", `{"name":"svc"}`, csrf, cookies)
	appID := m["application"].(map[string]any)["id"].(string)

	_, m, _ = e.req("POST", "/api/v1/keys", `{"name":"vo","view_only":true}`, csrf, cookies)
	voBearer := map[string]string{"Authorization": "Bearer " + m["secret"].(string)}

	code, _, _ := e.req("GET", "/api/v1/applications/"+appID, "", voBearer, nil)
	if code != 200 {
		t.Fatalf("view-only get: %d", code)
	}
	code, _, _ = e.req("PUT", "/api/v1/applications/"+appID, `{"version":1,"name":"x","code":"y"}`, voBearer, nil)
	if code != 403 {
		t.Fatalf("view-only update: %d", code)
	}
	code, _, _ = e.req("POST", "/api/v1/applications/"+appID+"/publish", `{"version":1}`, voBearer, nil)
	if code != 403 {
		t.Fatalf("view-only publish: %d", code)
	}
	code, _, _ = e.req("POST", "/api/v1/applications/"+appID+"/invoke", `{}`, voBearer, nil)
	if code != 403 {
		t.Fatalf("view-only invoke: %d", code)
	}

	// full (unrestricted) key: Run is allowed — the app is not published,
	// so invoke reaches the service and returns 503 not 403.
	_, m, _ = e.req("POST", "/api/v1/keys", `{"name":"full"}`, csrf, cookies)
	fullBearer := map[string]string{"Authorization": "Bearer " + m["secret"].(string)}
	code, _, _ = e.req("POST", "/api/v1/applications/"+appID+"/invoke", `{}`, fullBearer, nil)
	if code != 503 {
		t.Fatalf("full-key invoke on unpublished app: %d", code)
	}

	// instance-bound key: application endpoints are hidden entirely.
	code, m, _ = e.req("POST", "/api/v1/instances", `{"capabilities":{}}`, csrf, cookies)
	inst := m["instance"].(map[string]any)["id"].(string)
	_, m, _ = e.req("POST", "/api/v1/keys", `{"name":"bound","instance_id":"`+inst+`"}`, csrf, cookies)
	bound := map[string]string{"Authorization": "Bearer " + m["secret"].(string)}
	code, _, _ = e.req("GET", "/api/v1/applications/"+appID, "", bound, nil)
	if code != 404 {
		t.Fatalf("bound-key get: %d", code)
	}
	code, _, _ = e.req("GET", "/api/v1/applications", "", bound, nil)
	if code != 404 {
		t.Fatalf("bound-key list: %d", code)
	}
}

// Schema defaults + precision through the API surface.
func TestApplicationContracts(t *testing.T) {
	e := newEnv(t)
	cookies := login(e, "a@x.com")
	csrf := map[string]string{"X-Requested-With": "calcside"}
	_, m, _ := e.req("POST", "/api/v1/applications", `{"name":"svc"}`, csrf, cookies)
	app := m["application"].(map[string]any)
	if app["input_schema"] != nil || app["output_schema"] != nil {
		t.Fatal("default schemas should be null, not empty objects")
	}
	appID := app["id"].(string)

	// $ref schema is rejected at write
	code, m, _ := e.req("PUT", "/api/v1/applications/"+appID,
		`{"version":1,"name":"x","code":"def main(i): return i","spec":{},"input_schema":"{\"$ref\":\"x\"}"}`, csrf, cookies)
	if code != 400 {
		t.Fatalf("$ref schema: %d %v", code, m)
	}

	// echo program so the input number appears in the output verbatim
	code, m, _ = e.req("PUT", "/api/v1/applications/"+appID,
		`{"version":1,"name":"x","code":"def main(i): return i","spec":{}}`, csrf, cookies)
	if code != 200 {
		t.Fatalf("echo update: %d %v", code, m)
	}
	// raw bigint in test input survives output_json untouched
	code, m, _ = e.req("POST", "/api/v1/applications/"+appID+"/test",
		`{"version":2,"input":{"n":9007199254740993}}`, csrf, cookies)
	if code != 200 {
		t.Fatalf("test: %d %v", code, m)
	}
	out, _ := m["output_json"].(string)
	if out == "" {
		t.Fatal("empty output_json")
	}
	// the literal must not have been re-parsed into a rounded float
	if !strings.Contains(out, "9007199254740993") {
		t.Fatalf("bigint rounded: %s", out)
	}
}

// A session member with workspace Viewer role can read but not mutate.
func TestApplicationSessionViewer(t *testing.T) {
	e := newEnv(t)
	cookiesA := login(e, "a@x.com")
	cookiesV := login(e, "v@x.com")
	csrf := map[string]string{"X-Requested-With": "calcside"}
	vu, _ := e.st.GetUserByEmail(context.Background(), "v@x.com")
	au, _ := e.st.GetUserByEmail(context.Background(), "a@x.com")
	ws := store.PersonalWorkspaceID(au.ID)
	_ = e.st.AddMember(context.Background(), &store.Member{WorkspaceID: ws, UserID: vu.ID, Role: store.RoleViewer})
	_, m, _ := e.req("POST", "/api/v1/applications?workspace="+ws, `{"name":"svc"}`, csrf, cookiesA)
	appID := m["application"].(map[string]any)["id"].(string)
	hdrV := map[string]string{"X-Requested-With": "calcside"}

	code, _, _ := e.req("GET", "/api/v1/applications/"+appID, "", nil, cookiesV)
	if code != 200 {
		t.Fatalf("viewer get: %d", code)
	}
	for _, tc := range []struct{ name, method, path, body string }{
		{"update", "PUT", "/api/v1/applications/" + appID, `{"version":1,"name":"x","code":"y"}`},
		{"delete", "DELETE", "/api/v1/applications/" + appID + "?version=1", ""},
		{"publish", "POST", "/api/v1/applications/" + appID + "/publish", `{"version":1}`},
		{"test", "POST", "/api/v1/applications/" + appID + "/test", `{"version":1,"input":{}}`},
		{"invoke", "POST", "/api/v1/applications/" + appID + "/invoke", `{}`},
	} {
		code, _, _ := e.req(tc.method, tc.path, tc.body, hdrV, cookiesV)
		if code != 403 {
			t.Fatalf("viewer %s: %d", tc.name, code)
		}
	}
}
