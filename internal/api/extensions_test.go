package api

import (
	"context"
	"image/png"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"calcside/internal/auth"
	"calcside/internal/capability"
	capext "calcside/internal/capability/ext"
	capio "calcside/internal/capability/io"
	"calcside/internal/engine"
	"calcside/internal/instance"
	auditsvc "calcside/internal/service/audit"
	"calcside/internal/service/catalog"
	"calcside/internal/service/iam"
	policysvc "calcside/internal/service/policy"
	"calcside/internal/service/sandbox"
	"calcside/internal/service/vault"
	"calcside/internal/store"
	_ "calcside/internal/store/gormstore"
	"calcside/internal/store/storetest"
)

// TestListExtensions checks the catalog endpoint: unauth → 401, authed →
// the contrib tavily entry.
func TestListExtensions(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, "sqlite", storetest.SQLite(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	reg := capability.NewRegistry()
	reg.Register(capio.Factory())
	reg.Register(capext.Factory(capext.Options{
		LocalRoots: []string{filepath.Join("..", "..", "contrib")},
		CacheDir:   t.TempDir(),
	}))
	limits := capability.ServerLimits{
		DefaultTTL: 15 * time.Minute,
		MaxTTL:     24 * time.Hour, MaxExecTimeout: 5 * time.Minute,
	}
	mgr := instance.New(instance.Options{
		Engine: engine.New(8), Registry: reg, Limits: limits,
		EvalTimeout: time.Second, ReapInterval: time.Hour,
	})
	vaultSvc := vault.New(st, nil)
	svc := auth.NewService(st, false)
	h := Handler(Deps{
		IAM: iam.New(st, nil), Vault: vaultSvc,
		Policy: policysvc.New(st), Audit: auditsvc.New(st),
		Catalog: catalog.New(reg), Sandbox: sandbox.New(sandbox.Options{
			Store: st, Runtime: mgr, Secrets: vaultSvc,
			Registry: reg, Limits: limits, MaxInstancesPerUser: 10,
		}),
		Auth: svc,
	})
	srv := httptest.NewServer(h)
	defer srv.Close()
	e := &env{t: t, st: st, srv: srv}

	code, m, _ := e.req("GET", "/api/v1/extensions", "", nil, nil)
	if code != 401 {
		t.Fatalf("unauth: %d %v", code, m)
	}
	cookies := login(e, "a@x.com")
	code, m, _ = e.req("GET", "/api/v1/extensions", "", nil, cookies)
	if code != 200 {
		t.Fatalf("list: %d %v", code, m)
	}
	if m["local_enabled"] != true || m["remote_enabled"] != false {
		t.Fatalf("flags: %v", m)
	}
	exts, _ := m["extensions"].([]any)
	if len(exts) != 1 {
		t.Fatalf("extensions: %v", m["extensions"])
	}
	ex := exts[0].(map[string]any)
	if ex["source"] != "contrib/tavily" || ex["name"] != "tavily" || ex["version"] != "0.1.0" {
		t.Fatalf("entry: %v", ex)
	}
	icon, _ := ex["icon_url"].(string)
	if !strings.HasPrefix(icon, "/api/v1/extensions/icons/") {
		t.Fatalf("missing packaged icon URL: %v", ex)
	}
	if code, _, _ := e.req("GET", icon, "", nil, nil); code != 401 {
		t.Fatalf("unauth icon: %d", code)
	}
	req, err := http.NewRequest("GET", srv.URL+icon, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/png" || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("icon response: %d %v", resp.StatusCode, resp.Header)
	}
	if _, err := png.Decode(resp.Body); err != nil {
		t.Fatal(err)
	}
	if deps, _ := ex["dependencies"].([]any); len(deps) != 1 || deps[0] != "net" {
		t.Fatalf("deps: %v", ex["dependencies"])
	}
	ops, _ := ex["ops"].([]any)
	if len(ops) != 1 || ops[0].(map[string]any)["name"] != "search" {
		t.Fatalf("ops: %v", ex["ops"])
	}
	var keyField map[string]any
	for _, cf := range ex["config"].([]any) {
		cfm := cf.(map[string]any)
		if cfm["name"] == "api_key" {
			keyField = cfm
		}
	}
	if keyField == nil || keyField["type"] != "secret" {
		t.Fatalf("config fields: %v", ex["config"])
	}
}
