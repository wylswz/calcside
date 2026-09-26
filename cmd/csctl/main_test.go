package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"calcside/internal/api"
	"calcside/internal/audit"
	"calcside/internal/auth"
	"calcside/internal/capability"
	capfs "calcside/internal/capability/fs"
	capio "calcside/internal/capability/io"
	"calcside/internal/client"
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
	"calcside/internal/types"
)

type testEnv struct {
	srv *httptest.Server
	st  store.Store
	mgr *instance.Manager
	key string
}

func newTestEnv(t *testing.T) *testEnv {
	st, err := store.Open(context.Background(), "sqlite", filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	reg := capability.NewRegistry()
	reg.Register(capfs.Factory())
	reg.Register(capio.Factory())
	rec := audit.NewRecorder(st)
	t.Cleanup(rec.Close)
	limits := capability.ServerLimits{
		DefaultTTL: 15 * time.Minute,
		MaxTTL:     24 * time.Hour, MaxExecTimeout: 5 * time.Minute,
		MaxFSQuotaBytes: 256 << 20, NetAllowPrivate: true,
	}
	mgr := instance.New(instance.Options{
		Engine: engine.New(8), Registry: reg, Limits: limits,
		EvalTimeout: time.Second, ReapInterval: time.Hour,
	})
	vaultSvc := vault.New(st, nil)
	svc := auth.NewService(st, false)
	h := api.Handler(api.Deps{
		IAM: iam.New(st, nil), Vault: vaultSvc,
		Policy: policysvc.New(st), Audit: auditsvc.New(st),
		Catalog: catalog.New(reg), Sandbox: sandbox.New(sandbox.Options{
			Store: st, Runtime: mgr, Secrets: vaultSvc, Audit: rec,
			Registry: reg, Limits: limits, MaxInstancesPerUser: 10,
		}),
		Auth: svc,
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	u, err := st.UpsertUserByEmail(context.Background(), "cli@x.com", "", "")
	if err != nil {
		t.Fatal(err)
	}
	secret, k := auth.NewAPIKey(u.ID, "test", nil)
	if err := st.CreateAPIKey(context.Background(), k); err != nil {
		t.Fatal(err)
	}
	return &testEnv{srv: srv, st: st, mgr: mgr, key: secret}
}

// runCLI executes run() with captured output and the env pointing at the
// test server. It isolates HOME so no real config file is read.
func runCLI(t *testing.T, env *testEnv, args ...string) (int, string, string) {
	t.Helper()
	t.Setenv("CALCSIDE_SERVER", env.srv.URL)
	t.Setenv("CALCSIDE_API_KEY", env.key)
	t.Setenv("HOME", t.TempDir())
	var outBuf, errBuf bytes.Buffer
	oldOut, oldErr := stdout, stderr
	stdout, stderr = &outBuf, &errBuf
	defer func() { stdout, stderr = oldOut, oldErr }()
	code := run(args)
	return code, outBuf.String(), errBuf.String()
}

func TestWhoami(t *testing.T) {
	env := newTestEnv(t)
	code, out, errOut := runCLI(t, env, "whoami")
	if code != 0 || out == "" {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
}

func TestCreateExecRm(t *testing.T) {
	env := newTestEnv(t)
	code, out, errOut := runCLI(t, env, "instances", "create", "--fs")
	if code != 0 {
		t.Fatalf("create: %d %q", code, errOut)
	}
	id := bytes.TrimSpace([]byte(out))
	code, out, errOut = runCLI(t, env, "exec", string(id), "-c", "fs.write('a','x')\nprint(fs.read('a'))")
	if code != 0 || out != "x\n" {
		t.Fatalf("exec: %d out=%q err=%q", code, out, errOut)
	}
	code, out, _ = runCLI(t, env, "files", string(id), "/work")
	if code != 0 || !bytes.Contains([]byte(out), []byte("a")) {
		t.Fatalf("files: %d %q", code, out)
	}
	code, _, errOut = runCLI(t, env, "instances", "rm", string(id))
	if code != 0 {
		t.Fatalf("rm: %d %q", code, errOut)
	}
}

func TestExecScriptErrorExit2(t *testing.T) {
	env := newTestEnv(t)
	code, out, _ := runCLI(t, env, "instances", "create")
	if code != 0 {
		t.Fatal("create failed")
	}
	id := string(bytes.TrimSpace([]byte(out)))
	var errOut string
	code, _, errOut = runCLI(t, env, "exec", id, "-c", "undefined_func()")
	if code != 2 {
		t.Fatalf("expected exit 2, got %d", code)
	}
	if !bytes.Contains([]byte(errOut), []byte("error[")) {
		t.Fatalf("stderr %q", errOut)
	}
	runCLI(t, env, "instances", "rm", id)
}

func TestRunDeletesInstance(t *testing.T) {
	env := newTestEnv(t)
	// Even on script error the ephemeral instance must be deleted.
	code, _, _ := runCLI(t, env, "run", "-c", "boom(")
	if code != 2 {
		t.Fatalf("expected exit 2 on syntax error, got %d", code)
	}
	lst, err := env.st.ListInstances(context.Background(), "", types.InstanceRunning)
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range lst {
		if in.Status == types.InstanceRunning {
			t.Fatalf("ephemeral instance leaked: %s", in.ID)
		}
	}
	// And on success.
	var out string
	code, out, _ = runCLI(t, env, "run", "--fs", "-c", "print('ok')")
	if code != 0 || out != "ok\n" {
		t.Fatalf("run: %d %q", code, out)
	}
	lst, _ = env.st.ListInstances(context.Background(), "", types.InstanceRunning)
	for _, in := range lst {
		if in.Status == types.InstanceRunning {
			t.Fatalf("ephemeral instance leaked: %s", in.ID)
		}
	}
}

func TestConfigPrecedence(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, ".config", "calcside", "config.json")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(config{Server: "http://file-server", APIKey: "filekey"})
	if err := os.WriteFile(cfgPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", dir)
	t.Setenv("CALCSIDE_SERVER", "http://env-server")
	t.Setenv("CALCSIDE_API_KEY", "")
	c := loadConfig()
	if c.Server != "http://env-server" || c.APIKey != "filekey" {
		t.Fatalf("env should override file: %+v", c)
	}
	t.Setenv("CALCSIDE_SERVER", "")
	c = loadConfig()
	if c.Server != "http://file-server" {
		t.Fatalf("file fallback broken: %+v", c)
	}
}

func TestKeysMessage(t *testing.T) {
	env := newTestEnv(t)
	code, _, errOut := runCLI(t, env, "keys")
	if code != 2 || !bytes.Contains([]byte(errOut), []byte("web console")) {
		t.Fatalf("expected helpful keys message, got %d %q", code, errOut)
	}
}

// client package smoke: typed methods against the real API.
func TestClientTyped(t *testing.T) {
	env := newTestEnv(t)
	c := client.New(env.srv.URL, env.key)
	ctx := context.Background()
	u, err := c.Me(ctx)
	if err != nil || u.Email != "cli@x.com" {
		t.Fatalf("Me: %v %+v", err, u)
	}
	in, err := c.CreateInstance(ctx, client.InstanceSpec{"capabilities": map[string]any{"fs": map[string]any{}}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.Exec(ctx, in.ID, "print('hi')", 0)
	if err != nil || res.Output != "hi\n" {
		t.Fatalf("Exec: %v %+v", err, res)
	}
	if err := c.DeleteInstance(ctx, in.ID); err != nil {
		t.Fatal(err)
	}
}
