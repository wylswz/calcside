package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"calcside/cmd/calcside-worker/internal/config"
	"calcside/internal/runtime"
	"calcside/internal/runtime/remote"
	"calcside/internal/runtime/subproc"
)

func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == subproc.ChildArg {
		if err := subproc.RunChild(os.Stdin, os.Stdout, configureChild); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestInitializeWorker(t *testing.T) {
	{
		t.Run("process", func(t *testing.T) {
			cfg := config.WorkerConfig{
				Addr: "127.0.0.1:0", SharedKey: "test-worker-key", NodeID: "worker-test",
				APIAddr:    "http://127.0.0.1:1",
				DefaultTTL: time.Minute, MaxTTL: time.Hour, MaxExecTimeout: time.Minute,
				ReaperInterval: time.Hour, MaxConcurrentExecs: 2, ExtCacheDir: t.TempDir(),
			}
			app, cleanup, err := initializeWorker(cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(cleanup)
			if app.NodeID != cfg.NodeID || app.Server.Addr != cfg.Addr || app.Server.ReadTimeout != 30*time.Second || app.Server.WriteTimeout != cfg.MaxExecTimeout+30*time.Second {
				t.Fatalf("worker config not preserved: %+v", app)
			}
			w := httptest.NewRecorder()
			app.Server.Handler.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
			if w.Code != http.StatusOK {
				t.Fatalf("health: %d", w.Code)
			}
			w = httptest.NewRecorder()
			app.Server.Handler.ServeHTTP(w, httptest.NewRequest("POST", "/runtime/v1/create", strings.NewReader(`{}`)))
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("unsigned runtime request: %d", w.Code)
			}
			srv := httptest.NewServer(app.Server.Handler)
			t.Cleanup(srv.Close)
			rt, err := remote.NewDirect(srv.URL, srv.Client(), []byte(cfg.SharedKey), "api-test")
			if err != nil {
				t.Fatal(err)
			}
			owner := runtime.Owner{UserID: "usr_test"}
			created, err := rt.Create(t.Context(), &runtime.CreateRequest{
				InstanceID: "ins_test", Owner: owner, Spec: []byte(`{"capabilities":{}}`), ExpiresAt: time.Now().Add(time.Minute),
			})
			if err != nil {
				t.Fatal(err)
			}
			out, err := rt.Exec(t.Context(), &runtime.ExecRequest{
				InstanceID: "ins_test", Owner: owner, Epoch: created.Epoch, ExecID: "exe_test", Code: "print(42)",
			})
			if err != nil || out.Result.Output != "42\n" {
				t.Fatalf("exec: %v, %+v", err, out)
			}
		})
	}
}

func TestInitializeWorkerRequiresKey(t *testing.T) {
	app, cleanup, err := initializeWorker(config.WorkerConfig{NodeID: "worker-test"})
	if cleanup != nil {
		t.Cleanup(cleanup)
	}
	if err == nil || !strings.Contains(err.Error(), "--shared-key is required") || app != nil || cleanup != nil {
		t.Fatalf("app=%v cleanup=%t err=%v", app, cleanup != nil, err)
	}
}

func TestInitializeWorkerRequiresAPIAddr(t *testing.T) {
	app, cleanup, err := initializeWorker(config.WorkerConfig{
		NodeID: "worker-test", SharedKey: "test-key", ExtCacheDir: t.TempDir(),
	})
	if cleanup != nil {
		t.Cleanup(cleanup)
	}
	if err == nil || !strings.Contains(err.Error(), "--api-addr is required") || app != nil || cleanup != nil {
		t.Fatalf("app=%v cleanup=%t err=%v", app, cleanup != nil, err)
	}
}

func TestWorkerAgentWiring(t *testing.T) {
	base := config.WorkerConfig{
		Addr: "127.0.0.1:0", SharedKey: "test-worker-key", NodeID: "worker-test",
		APIAddr:    "http://127.0.0.1:1",
		DefaultTTL: time.Minute, MaxTTL: time.Hour, MaxExecTimeout: time.Minute,
		ReaperInterval: time.Hour, MaxConcurrentExecs: 2, ExtCacheDir: t.TempDir(),
	}
	// Configured addr: agent constructs without touching the network
	// (all contact happens inside Run).
	cfg := base
	app, cleanup, err := initializeWorker(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	if app.Agent == nil {
		t.Fatal("missing agent with api addr")
	}
	// A malformed addr fails construction.
	cfg = base
	cfg.APIAddr = "not a url"
	if _, _, err = initializeWorker(cfg); err == nil {
		t.Fatal("bad api addr accepted")
	}
}
