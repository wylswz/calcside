package api

import (
	"context"
	"encoding/json"
	"fmt"
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
	capnet "calcside/internal/capability/net"
	"calcside/internal/engine"
	"calcside/internal/instance"
	"calcside/internal/secrets"
	"calcside/internal/store"
	_ "calcside/internal/store/gormstore"
)

type env struct {
	t   *testing.T
	srv *httptest.Server
	st  store.Store
	mgr *instance.Manager
	rec *audit.Recorder
}

func newEnv(t *testing.T) *env { return newEnvWith(t, nil) }

// newEnvWith builds the test server; with cipher non-nil the secrets
// vault and net capability (private + http, for httptest) are enabled.
func newEnvWith(t *testing.T, cipher *secrets.Cipher) *env {
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
	if cipher != nil {
		reg.Register(capnet.Factory())
		limits.NetAllowPrivate = true
		limits.SecretsAllowHTTP = true
	}
	mgr := instance.New(st, engine.New(8), reg, rec, "", time.Second, limits, nil, cipher, nil, time.Hour)
	e.mgr = mgr
	svc := auth.NewService(st, false)
	h := Handler(Deps{Store: st, Manager: mgr, Registry: reg, Auth: svc, Cipher: cipher})
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

// login creates a user + session programmatically and returns the
// session cookie (replaces the old dev-login endpoint in tests).
func login(e *env, email string) []*http.Cookie {
	e.t.Helper()
	u, err := e.st.UpsertUserByEmail(context.Background(), email, "", "")
	if err != nil {
		e.t.Fatal(err)
	}
	svc := auth.NewService(e.st, false)
	raw, err := svc.CreateSession(context.Background(), u.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	return []*http.Cookie{{Name: auth.SessionCookie, Value: raw}}
}

func TestEndToEnd(t *testing.T) {
	e := newEnv(t)
	cookies := login(e, "a@x.com")

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
	cookies := login(e, "a@x.com")
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
	cookiesA := login(e, "a@x.com")
	csrf := map[string]string{"X-Requested-With": "calcside"}
	_, m, _ := e.req("POST", "/api/v1/keys", `{"name":"k"}`, csrf, cookiesA)
	bearerA := map[string]string{"Authorization": "Bearer " + m["secret"].(string)}
	_, m, _ = e.req("POST", "/api/v1/instances", `{}`, bearerA, nil)
	instID := m["instance"].(map[string]any)["id"].(string)

	cookiesB := login(e, "b@x.com")
	_, m, _ = e.req("POST", "/api/v1/keys", `{"name":"k"}`, csrf, cookiesB)
	bearerB := map[string]string{"Authorization": "Bearer " + m["secret"].(string)}
	code, _, _ := e.req("GET", "/api/v1/instances/"+instID, "", bearerB, nil)
	if code != 404 {
		t.Fatalf("other user's instance: expected 404, got %d", code)
	}
}

func TestCookiePostWithoutCSRF403(t *testing.T) {
	e := newEnv(t)
	cookies := login(e, "a@x.com")
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
	cookies := login(e, "a@x.com")
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
	cookies := login(e, "a@x.com")
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

// --- secrets vault API ---

func apiCipher(t *testing.T) *secrets.Cipher {
	t.Helper()
	c, err := secrets.NewCipher("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSecretsDisabled503(t *testing.T) {
	e := newEnv(t) // no cipher
	cookies := login(e, "a@x.com")
	csrf := map[string]string{"X-Requested-With": "calcside"}
	code, m, _ := e.req("GET", "/api/v1/secrets", "", nil, cookies)
	if code != 503 || m["error"].(map[string]any)["code"] != "secrets_disabled" {
		t.Fatalf("expected 503 secrets_disabled, got %d %v", code, m)
	}
	code, _, _ = e.req("POST", "/api/v1/secrets", `{"name":"T","value":"v","allowed_domains":["x.com"]}`, csrf, cookies)
	if code != 503 {
		t.Fatalf("POST secrets: expected 503, got %d", code)
	}
	// auth/config reports disabled
	code, m, _ = e.req("GET", "/api/v1/auth/config", "", nil, nil)
	if code != 200 || m["secrets"] != false {
		t.Fatalf("auth/config: %d %v", code, m)
	}
	// vault ref in instance spec rejected; inline still works
	_, m, _ = e.req("POST", "/api/v1/keys", `{"name":"k"}`, csrf, cookies)
	bearer := map[string]string{"Authorization": "Bearer " + m["secret"].(string)}
	code, m, _ = e.req("POST", "/api/v1/instances", `{"secrets":{"T":{"ref":"T"}}}`, bearer, nil)
	if code != 400 {
		t.Fatalf("vault ref w/o key: expected 400, got %d %v", code, m)
	}
}

func TestSecretsAPI(t *testing.T) {
	e := newEnvWith(t, apiCipher(t))
	cookies := login(e, "a@x.com")
	csrf := map[string]string{"X-Requested-With": "calcside"}

	code, m, _ := e.req("GET", "/api/v1/auth/config", "", nil, nil)
	if code != 200 || m["secrets"] != true {
		t.Fatalf("auth/config secrets flag: %d %v", code, m)
	}

	code, m, _ = e.req("POST", "/api/v1/secrets",
		`{"name":"T","value":"s3cr3t","allowed_domains":["api.x.com"]}`, csrf, cookies)
	if code != 201 {
		t.Fatalf("create secret: %d %v", code, m)
	}
	sec := m["secret"].(map[string]any)
	if sec["name"] != "T" {
		t.Fatalf("bad secret meta: %v", sec)
	}
	secJSON, _ := json.Marshal(sec)
	if strings.Contains(string(secJSON), "s3cr3t") || strings.Contains(string(secJSON), "ciphertext") {
		t.Fatalf("secret value leaked in response: %s", secJSON)
	}
	secID := sec["id"].(string)

	// duplicate name -> 409
	code, _, _ = e.req("POST", "/api/v1/secrets",
		`{"name":"T","value":"v2","allowed_domains":["x.com"]}`, csrf, cookies)
	if code != 409 {
		t.Fatalf("dup secret: expected 409, got %d", code)
	}
	// validation errors
	code, _, _ = e.req("POST", "/api/v1/secrets",
		`{"name":"bad","value":"v","allowed_domains":["x.com"]}`, csrf, cookies)
	if code != 400 {
		t.Fatalf("bad name: expected 400, got %d", code)
	}
	code, _, _ = e.req("POST", "/api/v1/secrets",
		`{"name":"V","value":"v"}`, csrf, cookies)
	if code != 400 {
		t.Fatalf("no domains: expected 400, got %d", code)
	}
	// list: never contains value
	code, m, _ = e.req("GET", "/api/v1/secrets", "", nil, cookies)
	if code != 200 {
		t.Fatalf("list: %d", code)
	}
	lstJSON, _ := json.Marshal(m)
	if strings.Contains(string(lstJSON), "s3cr3t") {
		t.Fatalf("list leaked value: %s", lstJSON)
	}
	// rotate value
	code, m, _ = e.req("PUT", "/api/v1/secrets/"+secID, `{"value":"rotated-value"}`, csrf, cookies)
	if code != 200 {
		t.Fatalf("rotate: %d %v", code, m)
	}
	// bearer key gets 403
	_, m2, _ := e.req("POST", "/api/v1/keys", `{"name":"k"}`, csrf, cookies)
	bearer := map[string]string{"Authorization": "Bearer " + m2["secret"].(string)}
	code, _, _ = e.req("GET", "/api/v1/secrets", "", bearer, nil)
	if code != 403 {
		t.Fatalf("bearer secrets list: expected 403, got %d", code)
	}
	// other user's secret -> 404
	cookiesB := login(e, "b@x.com")
	code, _, _ = e.req("PUT", "/api/v1/secrets/"+secID, `{"value":"x"}`, csrf, cookiesB)
	if code != 404 {
		t.Fatalf("other user secret: expected 404, got %d", code)
	}
	code, _, _ = e.req("DELETE", "/api/v1/secrets/"+secID, "", csrf, cookies)
	if code != 200 {
		t.Fatalf("delete: %d", code)
	}
}

// TestEnvSecretsEndToEnd exercises env.get, secrets.names, placeholder
// literal printing, and a net round-trip through an echo server.
func TestEnvSecretsEndToEnd(t *testing.T) {
	e := newEnvWith(t, apiCipher(t))
	cookies := login(e, "a@x.com")
	csrf := map[string]string{"X-Requested-With": "calcside"}
	_, m, _ := e.req("POST", "/api/v1/keys", `{"name":"k"}`, csrf, cookies)
	bearer := map[string]string{"Authorization": "Bearer " + m["secret"].(string)}

	// echo server (plain http; SecretsAllowHTTP enabled in this env)
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Echo", r.Header.Get("X-Token"))
		w.Write([]byte("tok=" + r.Header.Get("X-Token")))
	}))
	defer echo.Close()
	eh := strings.TrimPrefix(echo.URL, "http://")

	_, m, _ = e.req("POST", "/api/v1/secrets",
		`{"name":"T","value":"SUPERSECRET","allowed_domains":["`+eh+`"]}`, csrf, cookies)
	if m["secret"] == nil {
		t.Fatalf("create vault secret: %v", m)
	}

	spec := `{"env":{"REGION":"us-east-1"},
		"capabilities":{"net":{"allow_hosts":["` + eh + `"]},"fs":{}},
		"secrets":{"T":{"ref":"T","allowed_domains":["` + eh + `"]}}}`
	code, m, _ := e.req("POST", "/api/v1/instances", spec, bearer, nil)
	if code != 201 {
		t.Fatalf("create instance: %d %v", code, m)
	}
	instID := m["instance"].(map[string]any)["id"].(string)
	// GET JSON must not contain the value
	if strings.Contains(fmt.Sprintf("%v", m["instance"]), "SUPERSECRET") {
		t.Fatal("instance JSON leaked secret")
	}

	code, m, _ = e.req("POST", "/api/v1/instances/"+instID+"/exec",
		`{"code":"print(env.get(\"REGION\")); print(secrets.names()); print(\"{{secrets.T}}\")"}`, bearer, nil)
	if code != 200 || m["error"] != nil {
		t.Fatalf("exec: %d %v", code, m)
	}
	if m["output"] != "us-east-1\n[\"T\"]\n{{secrets.T}}\n" {
		t.Fatalf("output: %q", m["output"])
	}

	// leak sweep: net echo returns the value in body+header; fs write of
	// the body must be redacted on read-back; audit/executions clean.
	code, m, _ = e.req("POST", "/api/v1/instances/"+instID+"/exec",
		`{"code":"r = net.get(\"`+echo.URL+`/h\", headers={\"X-Token\":\"{{secrets.T}}\"})\nprint(r[\"body\"])\nfs.write(\"resp.txt\", r[\"body\"] + \"|\" + r[\"headers\"][\"X-Echo\"])"}`,
		bearer, nil)
	if code != 200 {
		t.Fatalf("net exec: %d %v", code, m)
	}
	out, _ := m["output"].(string)
	if strings.Contains(out, "SUPERSECRET") || !strings.Contains(out, "[REDACTED:T]") {
		t.Fatalf("exec output leaked/not redacted: %q", out)
	}
	if em, ok := m["error"].(map[string]any); ok && strings.Contains(fmt.Sprintf("%v", em), "SUPERSECRET") {
		t.Fatalf("error leaked: %v", em)
	}

	// files endpoint content redacted
	code, m, _ = e.req("GET", "/api/v1/instances/"+instID+"/files?path=/work/resp.txt", "", bearer, nil)
	if code != 200 || strings.Contains(m["content"].(string), "SUPERSECRET") || !strings.Contains(m["content"].(string), "[REDACTED:T]") {
		t.Fatalf("files content: %d %v", code, m)
	}

	// audit + executions rows
	e.rec.Close()
	e.rec = nil
	code, m, _ = e.req("GET", "/api/v1/audit?instance_id="+instID, "", bearer, nil)
	if code != 200 {
		t.Fatalf("audit: %d", code)
	}
	auditJSON, _ := json.Marshal(m)
	if strings.Contains(string(auditJSON), "SUPERSECRET") {
		t.Fatalf("audit leaked: %s", auditJSON)
	}
	code, m, _ = e.req("GET", "/api/v1/instances/"+instID+"/executions", "", bearer, nil)
	execJSON, _ := json.Marshal(m)
	if strings.Contains(string(execJSON), "SUPERSECRET") {
		t.Fatalf("executions leaked: %s", execJSON)
	}
	// net audit event shows template + names
	evs := m // reuse
	_ = evs
	_, m, _ = e.req("GET", "/api/v1/audit?instance_id="+instID, "", bearer, nil)
	found := false
	for _, ev := range m["events"].([]any) {
		evm := ev.(map[string]any)
		args := evm["args"].(string)
		if evm["capability"] == "net" && strings.Contains(args, `"secrets":["T"]`) {
			found = true
		}
	}
	if !found {
		t.Fatalf("no net audit event with template URL; events: %v", m["events"])
	}
}
