package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"calcside/cmd/calcside/internal/artifact"
	"calcside/cmd/calcside/internal/audit"
	"calcside/cmd/calcside/internal/auth"
	_ "calcside/cmd/calcside/internal/gormstore"
	auditsvc "calcside/cmd/calcside/internal/service/audit"
	"calcside/cmd/calcside/internal/service/catalog"
	"calcside/cmd/calcside/internal/service/iam"
	policysvc "calcside/cmd/calcside/internal/service/policy"
	"calcside/cmd/calcside/internal/service/sandbox"
	"calcside/cmd/calcside/internal/service/vault"
	"calcside/cmd/calcside/internal/vaultcipher"
	"calcside/internal/capability"
	capfs "calcside/internal/capability/fs"
	capio "calcside/internal/capability/io"
	capnet "calcside/internal/capability/net"
	"calcside/internal/engine"
	"calcside/internal/instance"
	"calcside/internal/runtime"
	"calcside/internal/store"
	"calcside/internal/store/storetest"
)

type env struct {
	t   *testing.T
	srv *httptest.Server
	st  store.Store
	mgr *instance.Manager
	sbx *sandbox.Service
	rec *audit.Recorder
}

// expire ages an instance out the way the API tier's reaper does:
// backdate the stored deadline, then run the sweep. Expiry is a status
// transition, so it is driven from the store, not from the node.
func (e *env) expire(id string) {
	e.t.Helper()
	ctx := context.Background()
	in, err := e.st.GetInstance(ctx, id)
	if err != nil {
		e.t.Fatal(err)
	}
	in.ExpiresAt = time.Now().Add(-time.Second)
	if err := e.st.UpdateInstance(ctx, in); err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.sbx.ExpireDue(ctx, 10); err != nil {
		e.t.Fatal(err)
	}
}

func newEnv(t *testing.T) *env { return newEnvWith(t, nil) }

// newEnvWith builds the test server; with cipher non-nil the secrets
// vault and net capability (private + http, for httptest) are enabled.
func newEnvWith(t *testing.T, cipher *vaultcipher.Cipher) *env {
	return newEnvWithPolicies(t, cipher, nil)
}

func newEnvWithPolicies(t *testing.T, cipher *vaultcipher.Cipher, global map[string]string) *env {
	return newArtifactEnv(t, cipher, global, artifact.Config{})
}

func newArtifactEnv(t *testing.T, cipher *vaultcipher.Cipher, global map[string]string, cfg artifact.Config) *env {
	t.Helper()
	st, err := store.Open(context.Background(), "sqlite", storetest.SQLite(t))
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
	limits := capability.ServerLimits{
		DefaultTTL: 15 * time.Minute,
		MaxTTL:     24 * time.Hour, MaxExecTimeout: 5 * time.Minute,
		MaxFSQuotaBytes: 256 << 20,
	}
	if cipher != nil {
		reg.Register(capnet.Factory())
		limits.SecretsAllowHTTP = true
	}
	mgr := instance.New(instance.Options{
		Engine: engine.New(8), Registry: reg, Limits: limits,
		EvalTimeout: time.Second, ReapInterval: time.Hour,
	})
	e.mgr = mgr
	t.Cleanup(mgr.StopReaper)
	vaultSvc := vault.New(st, cipher)
	e.sbx = sandbox.New(sandbox.Options{
		Store: st, Runtime: mgr, Secrets: vaultSvc, Audit: rec,
		Registry: reg, Limits: limits, MaxInstancesPerUser: 10, GlobalPolicies: global,
	})
	previews, err := NewArtifactPreviews(cfg, e.sbx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(previews.Close)
	svc := auth.NewService(st, false)
	h := Handler(Deps{
		IAM: iam.New(st, nil), Vault: vaultSvc,
		Policy: policysvc.New(st), Audit: auditsvc.New(st),
		Catalog: catalog.New(reg, nil), Sandbox: e.sbx,
		Auth: svc, Previews: previews,
	})
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

	// inspect: globals snapshot shows variables set by earlier execs
	code, m, _ = e.req("POST", "/api/v1/instances/"+instID+"/exec", `{"code":"answer = 42"}`, bearer, nil)
	if code != 200 {
		t.Fatalf("exec2: %d %v", code, m)
	}
	code, m, _ = e.req("GET", "/api/v1/instances/"+instID+"/inspect", "", bearer, nil)
	if code != 200 || m["variables"].(map[string]any)["answer"] != "42" {
		t.Fatalf("inspect: %d %v", code, m)
	}
	if _, ok := m["resources"]; ok {
		t.Fatalf("inspect: inproc instance has no cgroup, want resources omitted: %v", m)
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
	if code != 200 || len(m["executions"].([]any)) != 2 {
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
	e.expire(instID)
	code, _, _ := e.req("POST", "/api/v1/instances/"+instID+"/exec", `{"code":"print(1)"}`, bearer, nil)
	if code != 409 {
		t.Fatalf("expected 409, got %d", code)
	}
}

func TestGetExecution(t *testing.T) {
	e := newEnv(t)
	cookies := login(e, "a@x.com")
	csrf := map[string]string{"X-Requested-With": "calcside"}
	_, m, _ := e.req("POST", "/api/v1/keys", `{"name":"k"}`, csrf, cookies)
	bearer := map[string]string{"Authorization": "Bearer " + m["secret"].(string)}
	_, m, _ = e.req("POST", "/api/v1/instances", `{}`, bearer, nil)
	instID := m["instance"].(map[string]any)["id"].(string)
	code, m, _ := e.req("POST", "/api/v1/instances/"+instID+"/exec", `{"code":"print('hi')"}`, bearer, nil)
	if code != 200 {
		t.Fatalf("exec: %d %v", code, m)
	}
	execID := m["exec_id"].(string)

	code, m, _ = e.req("GET", "/api/v1/executions/"+execID, "", bearer, nil)
	if code != 200 || m["code"] != "print('hi')" {
		t.Fatalf("get execution: %d %v", code, m)
	}
	if m["execution"].(map[string]any)["code"] != nil {
		t.Fatal("execution object should not embed code")
	}

	cookiesB := login(e, "b@x.com")
	_, m, _ = e.req("POST", "/api/v1/keys", `{"name":"k"}`, csrf, cookiesB)
	bearerB := map[string]string{"Authorization": "Bearer " + m["secret"].(string)}
	code, _, _ = e.req("GET", "/api/v1/executions/"+execID, "", bearerB, nil)
	if code != 404 {
		t.Fatalf("other user's execution: expected 404, got %d", code)
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
	code, m, _ = e.req("POST", "/api/v1/policies", `{"name":"p","rego":`+jsonStr(valid)+`}`, csrf, cookies)
	if code != 409 {
		t.Fatalf("duplicate name: want 409, got %d %v", code, m)
	}
	code, m, _ = e.req("POST", "/api/v1/policies", `{"name":"has space","rego":`+jsonStr(valid)+`}`, csrf, cookies)
	if code != 400 {
		t.Fatalf("bad name: want 400, got %d %v", code, m)
	}
	code, m, _ = e.req("PUT", "/api/v1/policies/"+polID, `{"name":"no_read"}`, csrf, cookies)
	if code != 200 || m["policy"].(map[string]any)["name"] != "no_read" {
		t.Fatalf("update: %d %v", code, m)
	}
	code, _, _ = e.req("DELETE", "/api/v1/policies/"+polID, "", csrf, cookies)
	if code != 200 {
		t.Fatalf("delete policy: %d", code)
	}
}

// Library policies apply only to the instances that select them, and the
// rego is snapshotted at creation.
func TestInstancePolicySelection(t *testing.T) {
	e := newEnv(t)
	cookies := login(e, "a@x.com")
	csrf := map[string]string{"X-Requested-With": "calcside"}
	noRead := `package calcside.hooks
deny contains "no reads" if { input.op == "read" }`
	code, m, _ := e.req("POST", "/api/v1/policies", `{"name":"no_read","rego":`+jsonStr(noRead)+`}`, csrf, cookies)
	if code != 201 {
		t.Fatalf("create policy: %d %v", code, m)
	}
	polID := m["policy"].(map[string]any)["id"].(string)

	create := func(spec string) string {
		t.Helper()
		code, m, _ := e.req("POST", "/api/v1/instances", spec, csrf, cookies)
		if code != 201 {
			t.Fatalf("create instance %s: %d %v", spec, code, m)
		}
		return m["instance"].(map[string]any)["id"].(string)
	}
	readErr := func(id string) any {
		t.Helper()
		code, m, _ := e.req("POST", "/api/v1/instances/"+id+"/exec",
			`{"code":"fs.write('a','x')\nprint(fs.read('a'))"}`, csrf, cookies)
		if code != 200 {
			t.Fatalf("exec: %d %v", code, m)
		}
		if m["error"] == nil {
			return nil
		}
		return m["error"].(map[string]any)["type"]
	}

	with := create(`{"capabilities":{"fs":{}},"policies":["no_read"]}`)
	without := create(`{"capabilities":{"fs":{}}}`)
	if got := readErr(with); got != "policy_denied" {
		t.Fatalf("selected policy: want policy_denied, got %v", got)
	}
	if got := readErr(without); got != nil {
		t.Fatalf("unselected policy must not apply, got %v", got)
	}
	code, m, _ = e.req("GET", "/api/v1/instances/"+with, "", csrf, cookies)
	pols, _ := m["instance"].(map[string]any)["spec"].(map[string]any)["policies"].([]any)
	if code != 200 || len(pols) != 1 || pols[0] != "no_read" {
		t.Fatalf("stored spec policies: %d %v", code, m)
	}

	// snapshot: editing or deleting the library policy leaves a live
	// instance's enforcement unchanged
	if code, m, _ := e.req("DELETE", "/api/v1/policies/"+polID, "", csrf, cookies); code != 200 {
		t.Fatalf("delete policy: %d %v", code, m)
	}
	if got := readErr(with); got != "policy_denied" {
		t.Fatalf("snapshot lost after delete: got %v", got)
	}

	for _, spec := range []string{`{"policies":["no_read"]}`, `{"policies":["bad name"]}`, `{"policies":["a","a"]}`} {
		code, m, _ = e.req("POST", "/api/v1/instances", spec, csrf, cookies)
		if code != 400 || m["error"].(map[string]any)["code"] != "bad_spec" {
			t.Fatalf("%s: want 400 bad_spec, got %d %v", spec, code, m)
		}
	}
}

func jsonStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// --- secrets vault API ---

func apiCipher(t *testing.T) *vaultcipher.Cipher {
	t.Helper()
	c, err := vaultcipher.NewCipher("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
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
	// no domains = unrestricted (net allow_hosts still bounds requests)
	code, m, _ = e.req("POST", "/api/v1/secrets",
		`{"name":"V","value":"v"}`, csrf, cookies)
	if code != 201 {
		t.Fatalf("no domains: expected 201, got %d", code)
	}
	if ad, ok := m["secret"].(map[string]any)["allowed_domains"].([]any); !ok || len(ad) != 0 {
		t.Fatalf("no domains: want allowed_domains [], got %v", m["secret"])
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

	spec := `{"policies":[],"env":{"REGION":"us-east-1"},
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

func TestBuiltinPolicyCatalog(t *testing.T) {
	e := newEnv(t)
	cookies := login(e, "builtin@x.com")
	csrf := map[string]string{"X-Requested-With": "calcside"}
	code, m, _ := e.req("GET", "/api/v1/policies", "", nil, cookies)
	if code != 200 {
		t.Fatalf("list: %d %v", code, m)
	}
	pols := m["policies"].([]any)
	if len(pols) != 1 {
		t.Fatalf("built-in catalog: %v", pols)
	}
	p := pols[0].(map[string]any)
	if p["id"] != runtime.BlockPrivateNetworkPolicy || p["kind"] != "builtin" || p["default"] != true || p["rego"] != nil || p["description"] == "" {
		t.Fatalf("built-in descriptor: %v", p)
	}
	path := "/api/v1/policies/" + runtime.BlockPrivateNetworkPolicy
	code, m, _ = e.req("GET", path, "", nil, cookies)
	if code != 200 || m["policy"].(map[string]any)["name"] != runtime.BlockPrivateNetworkPolicy {
		t.Fatalf("get: %d %v", code, m)
	}
	for _, method := range []string{"PUT", "DELETE"} {
		code, m, _ = e.req(method, path, `{}`, csrf, cookies)
		if code != 403 {
			t.Fatalf("read-only %s: %d %v", method, code, m)
		}
	}
	for _, name := range []string{runtime.BlockPrivateNetworkPolicy, "builtin.future"} {
		code, m, _ = e.req("POST", "/api/v1/policies", `{"name":`+jsonStr(name)+`,"rego":"package calcside.hooks"}`, csrf, cookies)
		if code != 400 {
			t.Fatalf("reserved name %s: %d %v", name, code, m)
		}
	}
	code, m, _ = e.req("POST", "/api/v1/policies", `{"name":"custom","rego":"package calcside.hooks"}`, csrf, cookies)
	if code != 201 {
		t.Fatalf("create: %d %v", code, m)
	}
	id := m["policy"].(map[string]any)["id"].(string)
	code, m, _ = e.req("PUT", "/api/v1/policies/"+id, `{"name":`+jsonStr(runtime.BlockPrivateNetworkPolicy)+`}`, csrf, cookies)
	if code != 400 {
		t.Fatalf("reserved rename: %d %v", code, m)
	}
	code, m, _ = e.req("POST", "/api/v1/instances", `{"policies":["builtin.unknown"]}`, csrf, cookies)
	if code != 400 {
		t.Fatalf("unknown built-in: %d %v", code, m)
	}
}

func TestBuiltinNetworkPolicySelection(t *testing.T) {
	e := newEnvWith(t, apiCipher(t))
	cookies := login(e, "network@x.com")
	csrf := map[string]string{"X-Requested-With": "calcside"}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") }))
	defer target.Close()
	localhost := strings.Replace(target.URL, "127.0.0.1", "localhost", 1)
	code, m, _ := e.req("POST", "/api/v1/policies", `{"name":"custom","rego":"package calcside.hooks"}`, csrf, cookies)
	if code != 201 {
		t.Fatalf("policy: %d %v", code, m)
	}
	for _, tc := range []struct {
		name, field string
		blocked     bool
	}{
		{"default", "", true},
		{"selected", `,"policies":["builtin.block_private_network"]`, true},
		{"none", `,"policies":[]`, false},
		{"custom-only", `,"policies":["custom"]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, m, _ := e.req("POST", "/api/v1/instances", `{"capabilities":{"net":{}}`+tc.field+`}`, csrf, cookies)
			if code != 201 {
				t.Fatalf("create: %d %v", code, m)
			}
			in := m["instance"].(map[string]any)
			id := in["id"].(string)
			selected, ok := in["spec"].(map[string]any)["policies"].([]any)
			if !ok || (tc.blocked && (len(selected) != 1 || selected[0] != runtime.BlockPrivateNetworkPolicy)) || (tc.name == "none" && len(selected) != 0) {
				t.Fatalf("stored selection: %v", in)
			}
			for _, url := range []string{target.URL, localhost} {
				code, m, _ = e.req("POST", "/api/v1/instances/"+id+"/exec", `{"code":`+jsonStr(fmt.Sprintf(`print(net.get(%q)["body"])`, url))+`}`, csrf, cookies)
				if code != 200 {
					t.Fatalf("exec: %d %v", code, m)
				}
				if tc.blocked {
					err, ok := m["error"].(map[string]any)
					if !ok || err["type"] != "policy_denied" || !strings.Contains(err["message"].(string), runtime.BlockPrivateNetworkPolicy) {
						t.Fatalf("expected policy denial: %v", m)
					}
				} else if m["error"] != nil || m["output"] != "ok\n" {
					t.Fatalf("unselected: %v", m)
				}
			}
		})
	}
	e.rec.Close()
	e.rec = nil
	code, m, _ = e.req("GET", "/api/v1/audit", "", nil, cookies)
	if code != 200 {
		t.Fatalf("audit: %d %v", code, m)
	}
	denied := 0
	for _, item := range m["events"].([]any) {
		event := item.(map[string]any)
		if event["decision"] == "deny" {
			denied++
			if event["phase"] != "before" || !strings.Contains(event["reason"].(string), runtime.BlockPrivateNetworkPolicy) {
				t.Fatalf("audit denial: %v", event)
			}
		}
	}
	if denied != 4 {
		t.Fatalf("want 4 network policy audit denials, got %d", denied)
	}
}

func TestEmptySelectionKeepsServerPolicies(t *testing.T) {
	e := newEnvWithPolicies(t, nil, map[string]string{"server.rego": `package calcside.hooks
deny contains "server guardrail" if { input.capability == "io" }`})
	cookies := login(e, "server@x.com")
	csrf := map[string]string{"X-Requested-With": "calcside"}
	code, m, _ := e.req("POST", "/api/v1/instances", `{"policies":[]}`, csrf, cookies)
	if code != 201 {
		t.Fatalf("create: %d %v", code, m)
	}
	id := m["instance"].(map[string]any)["id"].(string)
	code, m, _ = e.req("POST", "/api/v1/instances/"+id+"/exec", `{"code":"print(1)"}`, csrf, cookies)
	if code != 200 || m["error"] == nil || !strings.Contains(fmt.Sprint(m["error"]), "server guardrail") {
		t.Fatalf("server policy bypassed: %d %v", code, m)
	}
}

func TestPasswordChangeRequiresLocalSession(t *testing.T) {
	e := newEnv(t)
	cookies := login(e, "google@example.com")
	csrf := map[string]string{"X-Requested-With": "calcside"}
	body := `{"current_password":"current-password","new_password":"changed-password"}`
	code, me, _ := e.req("GET", "/api/v1/me", "", nil, cookies)
	if code != 200 || me["user"].(map[string]any)["has_password"] != false {
		t.Fatalf("passwordless profile: %d %v", code, me)
	}
	code, _, _ = e.req("POST", "/api/v1/me/password", body, csrf, cookies)
	if code != 403 {
		t.Fatalf("passwordless account changed password: %d", code)
	}
	for _, body := range []string{`{}`, `{"new_password":"changed-password"}`, `{"current_password":null,"new_password":"changed-password"}`, `not-json`} {
		code, _, _ = e.req("POST", "/api/v1/me/password", body, csrf, cookies)
		if code != 400 {
			t.Fatalf("invalid password change body: %d", code)
		}
	}
	dev := newDevEnv(t, nil)
	code, _, _ = dev.req("POST", "/api/v1/me/password", body, csrf, nil)
	if code != 403 {
		t.Fatalf("anonymous account changed password: %d", code)
	}
}
