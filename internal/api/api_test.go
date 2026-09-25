package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"calcside/internal/audit"
	"calcside/internal/auth"
	"calcside/internal/capability"
	capfs "calcside/internal/capability/fs"
	capio "calcside/internal/capability/io"
	"calcside/internal/engine"
	"calcside/internal/instance"
	"calcside/internal/store"
	_ "calcside/internal/store/sqlite"
)

type env struct {
	t   *testing.T
	srv *httptest.Server
	st  store.Store
	mgr *instance.Manager
	rec *audit.Recorder
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(context.Background(), "sqlite", filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	reg := capability.NewRegistry()
	reg.Register(capfs.Factory())
	reg.Register(capio.Factory())
	e := &env{t: t, st: st}
	rec := audit.NewRecorder(st)
	e.rec = rec
	t.Cleanup(func() {
		if e.rec != nil {
			e.rec.Close()
		}
	})
	limits := instance.ServerLimits{
		MaxInstancesPerUser: 10, DefaultTTL: 15 * time.Minute,
		MaxTTL: 24 * time.Hour, MaxExecTimeout: 5 * time.Minute,
		MaxFSQuotaBytes: 256 << 20,
	}
	mgr := instance.New(st, engine.New(8), reg, rec, "", time.Second, limits, nil, nil, time.Hour)
	e.mgr = mgr
	svc := auth.NewService(st, false, true)
	h := Handler(Deps{Store: st, Manager: mgr, Registry: reg, Auth: svc})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	e.srv = srv
	return e
}

func (e *env) req(method, path, body string, hdrs map[string]string, cookies []*http.Cookie) (int, map[string]any, []*http.Cookie) {
	e.t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	r, err := http.NewRequest(method, e.srv.URL+path, rdr)
	if err != nil {
		e.t.Fatal(err)
	}
	for k, v := range hdrs {
		r.Header.Set(k, v)
	}
	for _, c := range cookies {
		r.AddCookie(c)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var m map[string]any
	if len(data) > 0 {
		_ = json.Unmarshal(data, &m)
	}
	return resp.StatusCode, m, resp.Cookies()
}

func devLogin(e *env, email string) []*http.Cookie {
	e.t.Helper()
	code, m, cookies := e.req("POST", "/auth/dev/login", `{"email":"`+email+`"}`, nil, nil)
	if code != 200 {
		e.t.Fatalf("dev login: %d %v", code, m)
	}
	return cookies
}

func TestEndToEnd(t *testing.T) {
	e := newEnv(t)
	cookies := devLogin(e, "a@x.com")

	// create API key via session
	csrf := map[string]string{"X-Requested-With": "calcside"}
	code, m, _ := e.req("POST", "/api/v1/keys", `{"name":"k1"}`, csrf, cookies)
	if code != 201 {
		t.Fatalf("create key: %d %v", code, m)
	}
	secret := m["secret"].(string)
	bearer := map[string]string{"Authorization": "Bearer " + secret}

	// create instance with bearer key
	spec := `{"ttl_seconds":900,"labels":{"team":"x"},"capabilities":{"fs":{},"io":{}},"limits":{"exec_timeout_ms":30000,"max_steps":1000000}}`
	code, m, _ = e.req("POST", "/api/v1/instances", spec, bearer, nil)
	if code != 201 {
		t.Fatalf("create instance: %d %v", code, m)
	}
	if _, ok := m["instance"].(map[string]any)["spec"].(map[string]any); !ok {
		t.Fatalf("spec should serialize as a JSON object, got %T", m["instance"].(map[string]any)["spec"])
	}
	instID := m["instance"].(map[string]any)["id"].(string)

	// exec: write + read file
	codeBody := `{"code":"fs.write('hello.txt','world')\nprint(fs.read('hello.txt'))"}`
	code, m, _ = e.req("POST", "/api/v1/instances/"+instID+"/exec", codeBody, bearer, nil)
	if code != 200 {
		t.Fatalf("exec: %d %v", code, m)
	}
	if m["output"] != "world\n" || m["error"] != nil {
		t.Fatalf("exec result: %v", m)
	}

	// files endpoint: list dir + read file
	code, m, _ = e.req("GET", "/api/v1/instances/"+instID+"/files?path=/work", "", bearer, nil)
	if code != 200 || len(m["entries"].([]any)) == 0 {
		t.Fatalf("files list: %d %v", code, m)
	}
	code, m, _ = e.req("GET", "/api/v1/instances/"+instID+"/files?path=/work/hello.txt", "", bearer, nil)
	if code != 200 || m["content"] != "world" {
		t.Fatalf("files read: %d %v", code, m)
	}

	// audit: flush then list
	e.rec.Close()
	e.rec = nil
	// a fresh recorder is unnecessary; events are already flushed
	code, m, _ = e.req("GET", "/api/v1/audit?instance_id="+instID, "", bearer, nil)
	if code != 200 {
		t.Fatalf("audit: %d %v", code, m)
	}
	evs := m["events"].([]any)
	if len(evs) == 0 {
		t.Fatal("expected audit events")
	}
	foundWrite := false
	for _, ev := range evs {
		e := ev.(map[string]any)
		if e["capability"] == "fs" && e["op"] == "write" && e["decision"] == "allow" {
			foundWrite = true
		}
	}
	if !foundWrite {
		t.Fatalf("no fs.write allow event in %v", evs)
	}

	// executions list
	code, m, _ = e.req("GET", "/api/v1/instances/"+instID+"/executions", "", bearer, nil)
	if code != 200 || len(m["executions"].([]any)) != 1 {
		t.Fatalf("executions: %d %v", code, m)
	}

	// delete instance, then exec -> 404 (instance ended → not found)
	code, m, _ = e.req("DELETE", "/api/v1/instances/"+instID, "", bearer, nil)
	if code != 200 {
		t.Fatalf("delete: %d %v", code, m)
	}
	code, _, _ = e.req("POST", "/api/v1/instances/"+instID+"/exec", `{"code":"print(1)"}`, bearer, nil)
	if code != 404 && code != 409 {
		t.Fatalf("exec after delete: expected 404/409, got %d", code)
	}
}

func TestExecOnEndedInstance409(t *testing.T) {
	e := newEnv(t)
	cookies := devLogin(e, "a@x.com")
	csrf := map[string]string{"X-Requested-With": "calcside"}
	_, m, _ := e.req("POST", "/api/v1/keys", `{"name":"k"}`, csrf, cookies)
	bearer := map[string]string{"Authorization": "Bearer " + m["secret"].(string)}
	_, m, _ = e.req("POST", "/api/v1/instances", `{"capabilities":{"io":{}}}`, bearer, nil)
	instID := m["instance"].(map[string]any)["id"].(string)
	// Expire it via manager directly.
	in, _ := e.mgr.Get(context.Background(), instID)
	in.ExpiresAt = time.Now().Add(-time.Second)
	e.mgr.Reap(context.Background())
	code, _, _ := e.req("POST", "/api/v1/instances/"+instID+"/exec", `{"code":"print(1)"}`, bearer, nil)
	if code != 409 {
		t.Fatalf("expected 409, got %d", code)
	}
}

func TestUnauth401(t *testing.T) {
	e := newEnv(t)
	code, _, _ := e.req("GET", "/api/v1/me", "", nil, nil)
	if code != 401 {
		t.Fatalf("expected 401, got %d", code)
	}
}

func TestOtherUser404(t *testing.T) {
	e := newEnv(t)
	cookiesA := devLogin(e, "a@x.com")
	csrf := map[string]string{"X-Requested-With": "calcside"}
	_, m, _ := e.req("POST", "/api/v1/keys", `{"name":"k"}`, csrf, cookiesA)
	bearerA := map[string]string{"Authorization": "Bearer " + m["secret"].(string)}
	_, m, _ = e.req("POST", "/api/v1/instances", `{}`, bearerA, nil)
	instID := m["instance"].(map[string]any)["id"].(string)

	cookiesB := devLogin(e, "b@x.com")
	_, m, _ = e.req("POST", "/api/v1/keys", `{"name":"k"}`, csrf, cookiesB)
	bearerB := map[string]string{"Authorization": "Bearer " + m["secret"].(string)}
	code, _, _ := e.req("GET", "/api/v1/instances/"+instID, "", bearerB, nil)
	if code != 404 {
		t.Fatalf("other user's instance: expected 404, got %d", code)
	}
}

func TestCookiePostWithoutCSRF403(t *testing.T) {
	e := newEnv(t)
	cookies := devLogin(e, "a@x.com")
	code, _, _ := e.req("POST", "/api/v1/keys", `{"name":"k"}`, nil, cookies)
	if code != 403 {
		t.Fatalf("expected 403, got %d", code)
	}
	// GET works without CSRF header
	code, _, _ = e.req("GET", "/api/v1/keys", "", nil, cookies)
	if code != 200 {
		t.Fatalf("GET keys: %d", code)
	}
}

func TestBearerKeyCannotManageKeys(t *testing.T) {
	e := newEnv(t)
	cookies := devLogin(e, "a@x.com")
	csrf := map[string]string{"X-Requested-With": "calcside"}
	_, m, _ := e.req("POST", "/api/v1/keys", `{"name":"k"}`, csrf, cookies)
	bearer := map[string]string{"Authorization": "Bearer " + m["secret"].(string)}
	code, _, _ := e.req("POST", "/api/v1/keys", `{"name":"k2"}`, bearer, nil)
	if code != 403 {
		t.Fatalf("expected 403, got %d", code)
	}
	code, _, _ = e.req("GET", "/api/v1/keys", "", bearer, nil)
	if code != 403 {
		t.Fatalf("GET keys via bearer: expected 403, got %d", code)
	}
}

func TestPolicyEndpoints(t *testing.T) {
	e := newEnv(t)
	cookies := devLogin(e, "a@x.com")
	csrf := map[string]string{"X-Requested-With": "calcside"}
	valid := `package calcside.hooks
deny contains "x" if { input.op == "read" }`
	code, m, _ := e.req("POST", "/api/v1/policies/validate", `{"rego":`+jsonStr(valid)+`}`, csrf, cookies)
	if code != 200 || m["valid"] != true {
		t.Fatalf("validate: %d %v", code, m)
	}
	code, m, _ = e.req("POST", "/api/v1/policies", `{"name":"p","rego":`+jsonStr(valid)+`}`, csrf, cookies)
	if code != 201 {
		t.Fatalf("create policy: %d %v", code, m)
	}
	polID := m["policy"].(map[string]any)["id"].(string)
	code, m, _ = e.req("PUT", "/api/v1/policies/"+polID, `{"enabled":false}`, csrf, cookies)
	if code != 200 {
		t.Fatalf("update: %d %v", code, m)
	}
	code, _, _ = e.req("DELETE", "/api/v1/policies/"+polID, "", csrf, cookies)
	if code != 200 {
		t.Fatalf("delete policy: %d", code)
	}
}

func jsonStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
