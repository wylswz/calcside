package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"calcside/cmd/calcside/internal/auth"
	"calcside/cmd/calcside/internal/vaultcipher"
	"calcside/internal/store"
)

const waitProg = `def on_ok(h):
    return {"ok": h["event"]["approved"]}
def on_exp(h):
    return {"ok": False}
def main(input):
    i = interrupt.new()
    interrupt.wait(i["id"], handler="on_ok", on_expire="on_exp", context={"x": 1})
    return {"unreachable": True}
`

// mkApp creates an application and returns its id.
func mkApp(e *env, cookies []*http.Cookie, code string) string {
	body := `{"name":"w", "code": ` + jsonStr(code) + `}`
	c, m, _ := e.req("POST", "/api/v1/applications", body,
		map[string]string{"X-Requested-With": "calcside"}, cookies)
	if c != 200 {
		e.t.Fatalf("create app: %d %v", c, m)
	}
	return m["application"].(map[string]any)["id"].(string)
}

func testRun(e *env, cookies []*http.Cookie, appID string) map[string]any {
	c, m, _ := e.req("POST", "/api/v1/applications/"+appID+"/test",
		`{"version":1,"input":{}}`, map[string]string{"X-Requested-With": "calcside"}, cookies)
	if c != 200 {
		e.t.Fatalf("test: %d %v", c, m)
	}
	return m
}

// TestResumeFlow covers the durable run path end to end: wait →
// resume → dispatch → success, with the record surfaces populated.
func TestResumeFlow(t *testing.T) {
	c, err := vaultcipher.NewCipher("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	if err != nil {
		t.Fatal(err)
	}
	e := newEnvWith(t, c)
	cookies := login(e, "a@x.com")
	csrf := map[string]string{"X-Requested-With": "calcside"}
	appID := mkApp(e, cookies, waitProg)

	m := testRun(e, cookies, appID)
	if m["run_status"] != "waiting" {
		t.Fatalf("run status: %v", m)
	}
	waiting, _ := m["waiting"].(map[string]any)
	runID, _ := m["run_id"].(string)
	intID, _ := waiting["interrupt_id"].(string)
	if runID == "" || intID == "" || !strings.Contains(waiting["console_url"].(string), "/inbox?interrupt="+intID) {
		t.Fatalf("waiting: %v", m)
	}
	if waiting["kind"] != nil {
		t.Fatalf("waiting fields: %v", waiting)
	}

	// Run detail: waiting, one pending interrupt, no context leaks.
	code, m, _ := e.req("GET", "/api/v1/runs/"+runID, "", nil, cookies)
	if code != 200 {
		t.Fatalf("get run: %d", code)
	}
	if m["run"].(map[string]any)["status"] != "waiting" {
		t.Fatalf("run: %v", m)
	}
	var i0 map[string]any
	for _, iv := range m["interrupts"].([]any) {
		if iv.(map[string]any)["status"] == "pending" {
			i0 = iv.(map[string]any)
		}
	}
	if i0 == nil {
		t.Fatalf("no pending interrupt: %v", m)
	}
	if i0["status"] != "pending" || i0["can_resume"] != true || i0["context_json"] != `{"x":1}` ||
		!strings.HasSuffix(i0["resume_url"].(string), "/api/v1/interrupts/"+intID+"/resume") {
		t.Fatalf("interrupt view: %v", i0)
	}

	// List + inbox.
	code, m, _ = e.req("GET", "/api/v1/applications/"+appID+"/runs", "", nil, cookies)
	if code != 200 || len(m["runs"].([]any)) != 1 {
		t.Fatalf("list runs: %d %v", code, m)
	}
	code, m, _ = e.req("GET", "/api/v1/interrupts?status=pending&resumable=true", "", nil, cookies)
	if code != 200 || len(m["interrupts"].([]any)) != 1 {
		t.Fatalf("inbox: %d %v", code, m)
	}

	// Resume (session needs CSRF) → second resume conflicts.
	if code, _, _ = e.req("POST", "/api/v1/interrupts/"+intID+"/resume", `{"approved":true}`, nil, cookies); code != 403 {
		t.Fatalf("resume without csrf: %d", code)
	}
	code, m, _ = e.req("POST", "/api/v1/interrupts/"+intID+"/resume", `{"approved":true}`, csrf, cookies)
	if code != 202 {
		t.Fatalf("resume: %d %v", code, m)
	}
	code, m, _ = e.req("POST", "/api/v1/interrupts/"+intID+"/resume", `{"approved":true}`, csrf, cookies)
	if code != 409 || m["error"].(map[string]any)["code"] != "already_resolved" {
		t.Fatalf("second resume: %d %v", code, m)
	}

	// Dispatch → handler step → run succeeded with handler output.
	e.runs.RunOnce(context.Background())
	code, m, _ = e.req("GET", "/api/v1/runs/"+runID, "", nil, cookies)
	if code != 200 {
		t.Fatalf("get run after dispatch: %d", code)
	}
	run := m["run"].(map[string]any)
	if run["status"] != "succeeded" || run["steps"].(float64) != 2 {
		t.Fatalf("final run: %v", run)
	}
	out, _ := m["run"].(map[string]any)["output_json"].(string)
	if !strings.Contains(out, `"ok":true`) {
		t.Fatalf("output_json: %v", m["run"])
	}
	steps := m["steps"].([]any)
	if len(steps) != 2 || steps[1].(map[string]any)["entry"] != "on_ok" {
		t.Fatalf("steps: %v", steps)
	}
}

// TestCancelRun: a waiting run is cancelled, its interrupt cancelled and
// the bound resume key revoked.
func TestCancelRun(t *testing.T) {
	c, _ := vaultcipher.NewCipher("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	e := newEnvWith(t, c)
	cookies := login(e, "a@x.com")
	csrf := map[string]string{"X-Requested-With": "calcside"}
	appID := mkApp(e, cookies, waitProg)
	m := testRun(e, cookies, appID)
	runID := m["run_id"].(string)
	intID := m["waiting"].(map[string]any)["interrupt_id"].(string)

	code, m, _ := e.req("POST", "/api/v1/runs/"+runID+"/cancel", "{}", csrf, cookies)
	if code != 200 {
		t.Fatalf("cancel: %d %v", code, m)
	}
	run, _ := e.st.GetRun(context.Background(), runID)
	intr, _ := e.st.GetInterrupt(context.Background(), intID)
	if run.Status != store.RunCancelled || intr.Status != store.InterruptCancelled {
		t.Fatalf("cancelled: %+v %+v", run, intr)
	}
	key, err := e.st.GetAPIKey(context.Background(), intr.KeyID)
	if err != nil || key.RevokedAt == nil {
		t.Fatalf("bound key not revoked: %v %+v", err, key)
	}
}

// TestInterruptExpiry exercises the dispatcher's due list on an
// injectable clock: waitProg has on_expire, so the expiry handler runs
// and the run succeeds.
func TestInterruptExpiry(t *testing.T) {
	c, _ := vaultcipher.NewCipher("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	e := newEnvWith(t, c)
	cookies := login(e, "a@x.com")
	appID2 := mkApp(e, cookies, waitProg)
	m2 := testRun(e, cookies, appID2)
	runID2 := m2["run_id"].(string)
	e.now = func() time.Time { return time.Now().Add(8 * 24 * time.Hour) }
	e.runs.RunOnce(context.Background())
	e.runs.RunOnce(context.Background())
	run2, _ := e.st.GetRun(context.Background(), runID2)
	if run2.Status != store.RunSucceeded {
		t.Fatalf("expiry run: %+v", run2)
	}
}

// TestResumeTokenAuth: the interrupt's own token resumes it, early on
// a reserved slot; other tokens and sessions cannot see the slot.
func TestResumeTokenAuth(t *testing.T) {
	c, _ := vaultcipher.NewCipher("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	e := newEnvWith(t, c)
	cookies := login(e, "a@x.com")
	ctx := context.Background()

	// Hand-craft a run with one reserved callback slot + bound key.
	u, _ := e.st.UpsertUserByEmail(ctx, "cb@x.com", "CB", "sub-cb")
	now := time.Now().UTC()
	run := &store.Run{ID: "run_cb", WorkspaceID: store.PersonalWorkspaceID(u.ID), ApplicationID: "app_x",
		ReleaseID: "rel_x", UserID: u.ID, Trigger: "test", Status: store.RunRunning,
		Steps: 1, CreatedAt: now, UpdatedAt: now}
	step := &store.RunStep{RunID: "run_cb", Seq: 0, Attempt: 1, Entry: "main",
		Status: store.StepRunning, StartedAt: now}
	secret, key := auth.NewAPIKey(u.ID, "cb", nil)
	key.BoundID = "int_cb"
	key.WorkspaceID = run.WorkspaceID
	if err := e.st.CreateAPIKey(ctx, key); err != nil {
		t.Fatal(err)
	}
	slot := &store.Interrupt{ID: "int_cb", WorkspaceID: run.WorkspaceID, ApplicationID: "app_x",
		RunID: "run_cb", StepSeq: 0, Status: store.InterruptSlotReserved,
		KeyID: key.ID, CreatedAt: now, UpdatedAt: now}
	if err := e.st.StartRun(ctx, run, step, []*store.Interrupt{slot}); err != nil {
		t.Fatal(err)
	}

	// Session → 404 (a reserved slot is invisible to regular callers).
	code, _, _ := e.req("POST", "/api/v1/interrupts/int_cb/resume", `{"x":1}`,
		map[string]string{"X-Requested-With": "calcside"}, cookies)
	if code != 404 {
		t.Fatalf("session resume: %d", code)
	}
	// Early accept on the reserved slot → 202.
	code, m, _ := e.req("POST", "/api/v1/interrupts/int_cb/resume", `{"x":1}`,
		map[string]string{"Authorization": "Bearer " + secret}, nil)
	if code != 202 {
		t.Fatalf("callback: %d %v", code, m)
	}
	// Second early resolution → 409.
	code, m, _ = e.req("POST", "/api/v1/interrupts/int_cb/resume", `{"x":2}`,
		map[string]string{"Authorization": "Bearer " + secret}, nil)
	if code != 409 {
		t.Fatalf("dup resume: %d %v", code, m)
	}
	// Wrong key → 404.
	secret2, key2 := auth.NewAPIKey(u.ID, "other", nil)
	key2.BoundID = "int_other"
	key2.WorkspaceID = run.WorkspaceID
	if err := e.st.CreateAPIKey(ctx, key2); err != nil {
		t.Fatal(err)
	}
	code, _, _ = e.req("POST", "/api/v1/interrupts/int_cb/resume", `{"x":3}`,
		map[string]string{"Authorization": "Bearer " + secret2}, nil)
	if code != 404 {
		t.Fatalf("wrong key: %d", code)
	}
	// ?token= also authenticates.
	code, _, _ = e.req("POST", "/api/v1/interrupts/int_cb/resume?token="+secret, `{"x":4}`,
		nil, nil)
	if code != 409 {
		t.Fatalf("token query resume: %d", code)
	}
}

// TestRunIsolation: viewer reads but cannot resolve/cancel/retry;
// cross-workspace is 404; retry outside unknown is 409.
func TestRunIsolation(t *testing.T) {
	c, _ := vaultcipher.NewCipher("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	e := newEnvWith(t, c)
	ctx := context.Background()
	cookiesA := login(e, "a@x.com")
	cookiesV := login(e, "v@x.com")
	cookiesB := login(e, "b@x.com")
	csrf := map[string]string{"X-Requested-With": "calcside"}
	vu, _ := e.st.GetUserByEmail(ctx, "v@x.com")
	au, _ := e.st.GetUserByEmail(ctx, "a@x.com")
	ws := store.PersonalWorkspaceID(au.ID)
	_ = e.st.AddMember(ctx, &store.Member{WorkspaceID: ws, UserID: vu.ID, Role: store.RoleViewer})

	appID := mkApp(e, cookiesA, waitProg)
	m := testRun(e, cookiesA, appID)
	runID := m["run_id"].(string)
	intID := m["waiting"].(map[string]any)["interrupt_id"].(string)

	// Viewer: GET run + interrupt OK.
	if code, _, _ := e.req("GET", "/api/v1/runs/"+runID, "", nil, cookiesV); code != 200 {
		t.Fatalf("viewer run: %d", code)
	}
	if code, _, _ := e.req("GET", "/api/v1/interrupts/"+intID, "", nil, cookiesV); code != 200 {
		t.Fatalf("viewer interrupt: %d", code)
	}
	// …but not resolve/cancel/retry.
	for _, tc := range []struct{ name, method, path, body string }{
		{"resume", "POST", "/api/v1/interrupts/" + intID + "/resume", `{"approved":true}`},
		{"cancel", "POST", "/api/v1/runs/" + runID + "/cancel", `{}`},
		{"retry", "POST", "/api/v1/runs/" + runID + "/retry", `{}`},
	} {
		if code, _, _ := e.req(tc.method, tc.path, tc.body, csrf, cookiesV); code != 403 {
			t.Fatalf("viewer %s: %d", tc.name, code)
		}
	}
	// Retry on a waiting run → 409.
	if code, _, _ := e.req("POST", "/api/v1/runs/"+runID+"/retry", `{}`, csrf, cookiesA); code != 409 {
		t.Fatalf("waiting retry: %d", code)
	}
	// Cross-workspace: b@x.com gets 404 on run + interrupt.
	for _, path := range []string{"/api/v1/runs/" + runID, "/api/v1/interrupts/" + intID} {
		if code, _, _ := e.req("GET", path, "", nil, cookiesB); code != 404 {
			t.Fatalf("cross-workspace %s: %d", path, code)
		}
	}
}

// TestResumeAccess: a pending interrupt resumes with Run access on the
// app; members without it see can_resume=false. Any JSON object event
// is accepted.
func TestResumeAccess(t *testing.T) {
	c, _ := vaultcipher.NewCipher("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	e := newEnvWith(t, c)
	ctx := context.Background()
	cookiesA := login(e, "a@x.com")
	cookiesU := login(e, "u@x.com")
	csrf := map[string]string{"X-Requested-With": "calcside"}
	uu, _ := e.st.GetUserByEmail(ctx, "u@x.com")
	au, _ := e.st.GetUserByEmail(ctx, "a@x.com")
	_ = e.st.AddMember(ctx, &store.Member{WorkspaceID: store.PersonalWorkspaceID(au.ID), UserID: uu.ID, Role: store.RoleViewer})

	appID := mkApp(e, cookiesA, waitProg)
	m := testRun(e, cookiesA, appID)
	intID := m["waiting"].(map[string]any)["interrupt_id"].(string)

	// Viewer member sees can_resume=false and resume → 403.
	code_, m, _ := e.req("GET", "/api/v1/interrupts/"+intID, "", nil, cookiesU)
	if code_ != 200 || m["interrupt"].(map[string]any)["can_resume"] != false {
		t.Fatalf("viewer view: %d %v", code_, m)
	}
	if code, _, _ := e.req("POST", "/api/v1/interrupts/"+intID+"/resume",
		`{"approved":true}`, csrf, cookiesU); code != 403 {
		t.Fatalf("viewer resume: %d", code)
	}
	// Non-object events are rejected; the owner resumes with any object.
	if code, _, _ := e.req("POST", "/api/v1/interrupts/"+intID+"/resume", `[1,2]`, csrf, cookiesA); code != 400 {
		t.Fatalf("array event: %d", code)
	}
	if code, _, _ := e.req("POST", "/api/v1/interrupts/"+intID+"/resume",
		`{"anything":"goes","list":[1,2]}`, csrf, cookiesA); code != 202 {
		t.Fatalf("owner resume: %d", code)
	}
}

// TestInterruptKeyIsolation: an int_-bound key reaches only the
// resume route.
func TestInterruptKeyIsolation(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u, _ := e.st.UpsertUserByEmail(ctx, "ik@x.com", "IK", "sub-ik")
	ws := store.PersonalWorkspaceID(u.ID)
	secret, key := auth.NewAPIKey(u.ID, "ik", nil)
	key.BoundID = "int_x"
	key.WorkspaceID = ws
	if err := e.st.CreateAPIKey(ctx, key); err != nil {
		t.Fatal(err)
	}
	hdr := map[string]string{"Authorization": "Bearer " + secret}
	for _, tc := range []struct{ name, method, path string }{
		{"applications", "GET", "/api/v1/applications"},
		{"app get", "GET", "/api/v1/applications/app_x"},
		{"instances", "GET", "/api/v1/instances"},
		{"keys", "GET", "/api/v1/keys"},
		{"secrets", "GET", "/api/v1/secrets"},
		{"policies", "GET", "/api/v1/policies"},
		{"audit", "GET", "/api/v1/audit"},
		{"run", "GET", "/api/v1/runs/run_x"},
		{"interrupts", "GET", "/api/v1/interrupts"},
		{"interrupt", "GET", "/api/v1/interrupts/int_x"},
		{"members", "GET", "/api/v1/workspaces/" + ws + "/members"},
	} {
		code, _, _ := e.req(tc.method, tc.path, `{"event":{}}`, hdr, nil)
		if code != 404 {
			t.Fatalf("int_ key on %s: %d", tc.name, code)
		}
	}
	// Its own resume route: 404 for a nonexistent interrupt, never 401.
	code, _, _ := e.req("POST", "/api/v1/interrupts/int_x/resume", `{"x":1}`, hdr, nil)
	if code != 404 {
		t.Fatalf("own resume: %d", code)
	}
	// MCP also refuses it.
	code, _, _ = e.req("POST", "/mcp", `{}`, hdr, nil)
	if code != 403 && code != 404 {
		t.Fatalf("mcp: %d", code)
	}
}
