package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"calcside/internal/audit"
	"calcside/internal/auth"
	"calcside/internal/capability"
	capfs "calcside/internal/capability/fs"
	capio "calcside/internal/capability/io"
	"calcside/internal/engine"
	"calcside/internal/instance"
	"calcside/internal/secrets"
	auditsvc "calcside/internal/service/audit"
	"calcside/internal/service/catalog"
	"calcside/internal/service/iam"
	policysvc "calcside/internal/service/policy"
	"calcside/internal/service/sandbox"
	"calcside/internal/service/vault"
	"calcside/internal/store"
	_ "calcside/internal/store/gormstore"
	"calcside/internal/store/storetest"
	"calcside/internal/types"
)

// newDevEnv builds a server in dev mode: no credentials required, the
// anonymous principal is implicit.
func newDevEnv(t *testing.T, cipher *secrets.Cipher) *env {
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
	t.Cleanup(func() { e.rec.Close() })
	limits := capability.ServerLimits{
		DefaultTTL: 15 * time.Minute,
		MaxTTL:     24 * time.Hour, MaxExecTimeout: 5 * time.Minute,
		MaxFSQuotaBytes: 256 << 20,
	}
	mgr := instance.New(instance.Options{
		Engine: engine.New(8), Registry: reg, Limits: limits,
		EvalTimeout: time.Second, ReapInterval: time.Hour,
	})
	e.mgr = mgr
	vaultSvc := vault.New(st, cipher)
	e.sbx = sandbox.New(sandbox.Options{
		Store: st, Runtime: mgr, Secrets: vaultSvc, Audit: rec,
		Registry: reg, Limits: limits, MaxInstancesPerUser: 10,
	})
	svc := auth.NewService(st, false)
	anon, err := st.UpsertUserByEmail(context.Background(), "anonymous@localhost", "anonymous", "")
	if err != nil {
		t.Fatal(err)
	}
	h := Handler(Deps{
		IAM: iam.New(st, nil), Vault: vaultSvc,
		Policy: policysvc.New(st), Audit: auditsvc.New(st),
		Catalog: catalog.New(reg), Sandbox: e.sbx,
		Auth: svc, Dev: true, Anonymous: anon,
	})
	e.srv = httptest.NewServer(h)
	t.Cleanup(e.srv.Close)
	return e
}

func TestDevModeAnonymous(t *testing.T) {
	cipher, err := secrets.NewCipher(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	e := newDevEnv(t, cipher)
	csrf := map[string]string{"X-Requested-With": "calcside"}

	// auth config reports dev mode
	code, m, _ := e.req("GET", "/api/v1/auth/config", "", nil, nil)
	if code != 200 || m["dev_mode"] != true {
		t.Fatalf("auth config: %d %v", code, m)
	}

	// me returns the anonymous user without credentials
	code, m, _ = e.req("GET", "/api/v1/me", "", nil, nil)
	if code != 200 {
		t.Fatalf("me: %d %v", code, m)
	}
	if m["kind"] != string(types.AuthAnonymous) {
		t.Fatalf("kind = %v", m["kind"])
	}
	if u := m["user"].(map[string]any); u["email"] != "anonymous@localhost" || u["is_admin"] != false {
		t.Fatalf("user = %v", u)
	}

	// anonymous can create instances
	code, m, _ = e.req("POST", "/api/v1/instances",
		`{"capabilities":{"io":{}}}`, csrf, nil)
	if code != 201 {
		t.Fatalf("create: %d %v", code, m)
	}
	instID := m["instance"].(map[string]any)["id"].(string)
	code, m, _ = e.req("POST", "/api/v1/instances/"+instID+"/exec",
		`{"code":"print(1+1)"}`, csrf, nil)
	if code != 200 || m["output"] != "2\n" {
		t.Fatalf("exec: %d %v", code, m)
	}

	// anonymous can manage API keys (session-class endpoints)
	code, m, _ = e.req("POST", "/api/v1/keys", `{"name":"k"}`, csrf, nil)
	if code != 201 {
		t.Fatalf("create key: %d %v", code, m)
	}

	// anonymous can manage secrets
	code, m, _ = e.req("POST", "/api/v1/secrets",
		`{"name":"T","value":"v","allowed_domains":["x.com"]}`, csrf, nil)
	if code != 201 {
		t.Fatalf("create secret: %d %v", code, m)
	}
}

func TestDevModeCSRFForAnonymous(t *testing.T) {
	e := newDevEnv(t, nil)
	// mutation without the CSRF header is rejected for anonymous too
	code, m, _ := e.req("POST", "/api/v1/instances",
		`{"capabilities":{"io":{}}}`, nil, nil)
	if code != 403 {
		t.Fatalf("expected csrf 403, got %d %v", code, m)
	}
	if m["error"].(map[string]any)["code"] != string(types.ErrCodeCSRF) {
		t.Fatalf("code = %v", m["error"])
	}
}

func TestNonDevUnauthenticated401(t *testing.T) {
	e := newEnv(t)
	code, m, _ := e.req("GET", "/api/v1/me", "", nil, nil)
	if code != 401 || m["error"].(map[string]any)["code"] != string(types.ErrCodeUnauthorized) {
		t.Fatalf("me: %d %v", code, m)
	}
	// non-dev keeps rejecting stale cookies
	stale := []*http.Cookie{{Name: "cs_session", Value: "stale"}}
	code, m, _ = e.req("GET", "/api/v1/me", "", nil, stale)
	if code != 401 {
		t.Fatalf("non-dev stale cookie: %d %v", code, m)
	}
}

func TestDevModeStaleCookieFallsBackAnonymous(t *testing.T) {
	e := newDevEnv(t, nil)
	stale := []*http.Cookie{{Name: "cs_session", Value: "stale"}}
	code, m, setCookies := e.req("GET", "/api/v1/me", "", nil, stale)
	if code != 200 || m["kind"] != string(types.AuthAnonymous) {
		t.Fatalf("me: %d %v", code, m)
	}
	var cleared bool
	for _, c := range setCookies {
		if c.Name == "cs_session" && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatalf("expected cs_session clearing Set-Cookie, got %v", setCookies)
	}
}

func TestDevModeBogusBearerStill401(t *testing.T) {
	e := newDevEnv(t, nil)
	bad := map[string]string{"Authorization": "Bearer cs_bogus"}
	code, m, _ := e.req("GET", "/api/v1/me", "", bad, nil)
	if code != 401 {
		t.Fatalf("bogus bearer: %d %v", code, m)
	}
}

func TestBearerBeatsAnonymous(t *testing.T) {
	e := newDevEnv(t, nil)
	// create a real user + API key; bearer auth must win over anonymous
	u, err := e.st.UpsertUserByEmail(context.Background(), "real@x.com", "real", "")
	if err != nil {
		t.Fatal(err)
	}
	secret, k := auth.NewAPIKey(u.ID, "k", nil)
	if err := e.st.CreateAPIKey(context.Background(), k); err != nil {
		t.Fatal(err)
	}
	bearer := map[string]string{"Authorization": "Bearer " + secret}
	code, m, _ := e.req("GET", "/api/v1/me", "", bearer, nil)
	if code != 200 {
		t.Fatalf("me: %d %v", code, m)
	}
	if usr := m["user"].(map[string]any); usr["email"] != "real@x.com" {
		t.Fatalf("user = %v", usr)
	}
	if m["kind"] != string(types.AuthAPIKey) {
		t.Fatalf("kind = %v", m["kind"])
	}
	// bearer mutations need no CSRF header
	code, m, _ = e.req("POST", "/api/v1/instances",
		`{"capabilities":{"io":{}}}`, bearer, nil)
	if code != 201 {
		t.Fatalf("create with bearer: %d %v", code, m)
	}
}
