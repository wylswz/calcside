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

	"calcside/cmd/calcside/internal/auth"
	"calcside/cmd/calcside/internal/extensions"
	_ "calcside/cmd/calcside/internal/gormstore"
	"calcside/cmd/calcside/internal/service/access"
	auditsvc "calcside/cmd/calcside/internal/service/audit"
	"calcside/cmd/calcside/internal/service/catalog"
	"calcside/cmd/calcside/internal/service/iam"
	llmprovidersvc "calcside/cmd/calcside/internal/service/llmprovider"
	policysvc "calcside/cmd/calcside/internal/service/policy"
	"calcside/cmd/calcside/internal/service/sandbox"
	"calcside/cmd/calcside/internal/service/vault"
	workspacesvc "calcside/cmd/calcside/internal/service/workspace"
	"calcside/internal/capability"
	capext "calcside/internal/capability/ext"
	capio "calcside/internal/capability/io"
	"calcside/internal/engine"
	"calcside/internal/runtime/instance"
	"calcside/internal/store"
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
	extOptions := capext.Options{
		LocalRoots: []string{filepath.Join("..", "..", "..", "..", "contrib")},
		CacheDir:   t.TempDir(),
	}
	reg.Register(capext.Factory(extOptions))
	limits := capability.ServerLimits{
		DefaultTTL: 15 * time.Minute,
		MaxTTL:     24 * time.Hour, MaxExecTimeout: 5 * time.Minute,
	}
	mgr := instance.New(instance.Options{
		Engine: engine.New(8), Registry: reg, Limits: limits,
		EvalTimeout: time.Second, ReapInterval: time.Hour,
	})
	ac := access.New(st)
	vaultSvc := vault.New(st, nil, ac)
	svc := auth.NewService(st, false)
	sbx := sandbox.New(sandbox.Options{
		Store: st, Runtime: mgr, Secrets: vaultSvc,
		Registry: reg, Limits: limits, Access: ac, MaxInstancesPerUser: 10,
	})
	h := Handler(Deps{
		IAM: iam.New(st, ac, sbx, 10, nil), Vault: vaultSvc,
		Policy: policysvc.New(st, ac), LLMProvider: llmprovidersvc.New(st, ac, &extOptions),
		Audit:   auditsvc.New(st, ac),
		Catalog: catalog.New(reg, extensions.NewCatalog(extOptions)), Sandbox: sbx, Access: ac, Workspace: workspacesvc.New(st, ac),
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
	var ex map[string]any
	for _, e := range exts {
		if m, ok := e.(map[string]any); ok && m["source"] == "contrib/tavily" {
			ex = m
		}
	}
	if ex == nil {
		t.Fatalf("contrib/tavily not in catalog: %v", m["extensions"])
	}
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
