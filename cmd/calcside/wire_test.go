package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"calcside/internal/config"
	"calcside/internal/node"
	"calcside/internal/node/subproc"
	"calcside/internal/runtime/remote"
	"calcside/internal/store"
	"calcside/internal/store/storetest"
	"calcside/internal/types"
)

func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == subproc.ChildArg {
		if err := subproc.RunChild(os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func testConfig(t *testing.T) config.Config {
	t.Helper()
	return config.Config{
		Addr: "127.0.0.1:0", AddrExplicit: true, Dev: true,
		Store: types.DriverSQLite, DSN: storetest.SQLite(t), NodeID: "api-test",
		DefaultTTL: time.Minute, MaxTTL: time.Hour, MaxExecTimeout: time.Minute,
		MaxConcurrentExecs: 2, ReaperInterval: time.Hour, PolicyEvalTimeout: time.Second,
		ExtCacheDir: t.TempDir(), InstanceIsolation: config.IsolationInproc,
	}
}

func request(t *testing.T, h http.Handler, method, path, body string, want int) map[string]any {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Requested-With", "calcside")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("%s %s: status=%d body=%s", method, path, w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestInitializeApp(t *testing.T) {
	for _, mode := range []string{config.IsolationInproc, config.IsolationProcess, "remote"} {
		t.Run(mode, func(t *testing.T) {
			cfg := testConfig(t)
			if mode == "remote" {
				nd := node.Build(node.Config{Limits: cfg.ExecLimits()})
				t.Cleanup(nd.Close)
				worker := httptest.NewServer(remote.NewHandler(nd.Manager, []byte("test-worker-key")))
				t.Cleanup(worker.Close)
				cfg.Workers = "worker-test=" + strings.TrimPrefix(worker.URL, "http://")
				cfg.WorkerKey = "test-worker-key"
			} else {
				cfg.InstanceIsolation = mode
			}
			app, cleanup, err := initializeApp(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(cleanup)
			if app.Server.Addr != cfg.Addr || app.Sandbox == nil {
				t.Fatalf("incomplete app: %+v", app)
			}
			h := app.Server.Handler
			request(t, h, "GET", "/healthz", "", http.StatusOK)
			if got := request(t, h, "GET", "/api/v1/auth/config", "", http.StatusOK); got["dev_mode"] != true || got["google"] != false {
				t.Fatalf("auth config: %v", got)
			}
			created := request(t, h, "POST", "/api/v1/instances", `{"capabilities":{}}`, http.StatusCreated)
			id := created["instance"].(map[string]any)["id"].(string)
			out := request(t, h, "POST", "/api/v1/instances/"+id+"/exec", `{"code":"x = 41\nprint(x + 1)"}`, http.StatusOK)
			if out["output"] != "42\n" {
				t.Fatalf("exec: %v", out)
			}
			out = request(t, h, "POST", "/api/v1/instances/"+id+"/exec", `{"code":"print(x)"}`, http.StatusOK)
			if out["output"] != "41\n" {
				t.Fatalf("globals not preserved: %v", out)
			}
			if mode == "remote" {
				r := httptest.NewRequest("GET", remote.ExtTreePath, nil)
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != http.StatusUnauthorized {
					t.Fatalf("unsigned extension callback: %d", w.Code)
				}
			}
		})
	}
}

type trackedStore struct {
	store.Store
	closes int
}

func (s *trackedStore) Close() error {
	s.closes++
	return s.Store.Close()
}

func trackStore(t *testing.T, cfg *config.Config) *trackedStore {
	t.Helper()
	st, err := store.Open(t.Context(), cfg.Store, cfg.DSN)
	if err != nil {
		t.Fatal(err)
	}
	tracked := &trackedStore{Store: st}
	t.Cleanup(func() { _ = st.Close() })
	cfg.Store = types.StoreDriver(t.Name())
	store.RegisterDriver(cfg.Store, func(context.Context, string) (store.Store, error) { return tracked, nil })
	return tracked
}

func TestInitializeAppFailureCleanup(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*config.Config)
		want   string
	}{
		{"cipher", func(c *config.Config) { c.SecretKey = "invalid" }, "--secret-key:"},
		{"policy", func(c *config.Config) { c.PolicyDir = filepath.Join(t.TempDir(), "[") }, "--policy-dir:"},
		{"worker-key", func(c *config.Config) { c.Workers = "w=127.0.0.1:1" }, "--workers requires --worker-key"},
		{"workers", func(c *config.Config) { c.Workers, c.WorkerKey = "invalid", "test-key" }, "--workers: bad entry"},
		{"dev-address", func(c *config.Config) { c.Addr = "0.0.0.0:8080" }, "dev mode refuses non-loopback"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig(t)
			st := trackStore(t, &cfg)
			tc.mutate(&cfg)
			app, cleanup, err := initializeApp(t.Context(), cfg)
			if cleanup != nil {
				t.Cleanup(cleanup)
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) || app != nil || cleanup != nil {
				t.Fatalf("app=%v cleanup=%t err=%v", app, cleanup != nil, err)
			}
			if st.closes != 1 {
				t.Fatalf("store closed %d times, want once", st.closes)
			}
		})
	}
}

func TestInitializeAppProduction(t *testing.T) {
	cfg := testConfig(t)
	cfg.Dev = false
	st := trackStore(t, &cfg)
	app, cleanup, err := initializeApp(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup()
		if st.closes != 1 {
			t.Errorf("store closed %d times, want once", st.closes)
		}
	}()
	request(t, app.Server.Handler, "GET", "/api/v1/me", "", http.StatusUnauthorized)
}

func TestInitializeAppFlushesAuditBeforeClosingStore(t *testing.T) {
	cfg := testConfig(t)
	st := trackStore(t, &cfg)
	var id string
	func() {
		app, cleanup, err := initializeApp(t.Context(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		h := app.Server.Handler
		created := request(t, h, "POST", "/api/v1/instances", `{"capabilities":{}}`, http.StatusCreated)
		id = created["instance"].(map[string]any)["id"].(string)
		request(t, h, "POST", "/api/v1/instances/"+id+"/exec", `{"code":"io.println(42)"}`, http.StatusOK)
	}()
	if st.closes != 1 {
		t.Fatalf("store closed %d times, want once", st.closes)
	}
	reopened, err := store.Open(t.Context(), types.DriverSQLite, cfg.DSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	events, err := reopened.ListAuditEvents(t.Context(), store.AuditFilter{InstanceID: id})
	if err != nil || len(events) == 0 {
		t.Fatalf("audit was not flushed: events=%v err=%v", events, err)
	}
}

func TestInitializeAppDoesNotMigrate(t *testing.T) {
	cfg := testConfig(t)
	cfg.DSN = filepath.Join(t.TempDir(), "unmigrated.db")
	st := trackStore(t, &cfg)
	app, cleanup, err := initializeApp(t.Context(), cfg)
	if cleanup != nil {
		t.Cleanup(cleanup)
	}
	if err == nil || !strings.Contains(err.Error(), "recover:") || app != nil || cleanup != nil {
		t.Fatalf("app=%v cleanup=%t err=%v", app, cleanup != nil, err)
	}
	if st.closes != 1 {
		t.Fatalf("store closed %d times, want once", st.closes)
	}
}

func TestServerAddr(t *testing.T) {
	for _, tc := range []struct {
		cfg  config.Config
		want string
	}{
		{config.Config{Addr: ":8080"}, ":8080"},
		{config.Config{Addr: ":8080", Dev: true}, "127.0.0.1:8080"},
		{config.Config{Addr: "127.0.0.1:8787", Dev: true, AddrExplicit: true}, "127.0.0.1:8787"},
	} {
		if got := serverAddr(tc.cfg); got != tc.want {
			t.Errorf("serverAddr(%+v) = %q, want %q", tc.cfg, got, tc.want)
		}
	}
}
