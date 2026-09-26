package api

import (
	"context"
	"net/http/httptest"
	"path/filepath"
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
)

// TestListExtensions checks the catalog endpoint: unauth → 401, authed →
// the examples/capabilities tavily entry.
func TestListExtensions(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, "sqlite", filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	reg := capability.NewRegistry()
	reg.Register(capio.Factory())
	reg.Register(capext.Factory(capext.Options{
		LocalRoots: []string{filepath.Join("..", "..", "examples", "capabilities")},
		CacheDir:   t.TempDir(),
	}))
	mgr := instance.New(st, engine.New(8), reg, nil, "", time.Second, instance.ServerLimits{
		MaxInstancesPerUser: 10, DefaultTTL: 15 * time.Minute,
		MaxTTL: 24 * time.Hour, MaxExecTimeout: 5 * time.Minute,
	}, nil, nil, nil, time.Hour)
	svc := auth.NewService(st, false)
	h := Handler(Deps{
		IAM: iam.New(st, nil), Vault: vault.New(st, nil),
		Policy: policysvc.New(st), Audit: auditsvc.New(st),
		Catalog: catalog.New(reg), Sandbox: sandbox.New(st, mgr),
		Auth: svc,
	})
	srv := httptest.NewServer(h)
	defer srv.Close()
	e := &env{t: t, st: st, srv: srv}

	code, m, _ := e.req("GET", "/api/v1/extensions", "", nil, nil)
	if code != 401 {
		t.Fatalf("unauth: %d %v", code, m)
	}
	code, m, _ = e.req("GET", "/api/v1/extensions", "", nil, login(e, "a@x.com"))
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
	if ex["name"] != "tavily" || ex["version"] != "0.1.0" {
		t.Fatalf("entry: %v", ex)
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
