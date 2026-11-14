package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"calcside/cmd/calcside-worker/internal/config"
	"calcside/internal/capability"
	"calcside/internal/engine"
	"calcside/internal/nodeagent"
	"calcside/internal/runtime"
	"calcside/internal/runtime/instance"
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
				APIAddr: "http://127.0.0.1:1", AdvertiseAddr: "127.0.0.1:8090",
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

// fakeNodeControl answers register/sync successfully and captures the
// registration payload — enough for the agent to reach Ready without
// any replica work.
type fakeNodeControl struct {
	mu      sync.Mutex
	lastReg *runtime.NodeRegisterRequest
}

func (f *fakeNodeControl) RegisterNode(_ context.Context, r *runtime.NodeRegisterRequest) (*runtime.NodeRegisterResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *r
	f.lastReg = &cp
	return &runtime.NodeRegisterResponse{
		Node:      runtime.NodeIdentity{NodeID: r.NodeID, BootID: r.BootID, Incarnation: 1},
		ServerNow: time.Now(),
	}, nil
}

func (f *fakeNodeControl) SyncNode(_ context.Context, _ *runtime.NodeSyncRequest) (*runtime.NodeSyncResponse, error) {
	return &runtime.NodeSyncResponse{ServerNow: time.Now()}, nil
}

func (f *fakeNodeControl) PrepareReplica(_ context.Context, _ *runtime.NodePrepareRequest) (*runtime.NodePrepareResponse, error) {
	return nil, errors.New("unused")
}

func readyzCode(t *testing.T, h http.Handler) int {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/readyz", nil))
	return w.Code
}

// TestReadyzLifecycle proves the startup race fix: /healthz only says
// the process is alive, /readyz stays 503 until the agent confirms
// registration, flips 200 once the heartbeat succeeds, and drops back
// to 503 on shutdown.
func TestReadyzLifecycle(t *testing.T) {
	key := []byte("readyz-test-key")
	reg := capability.NewRegistry()
	mgr := instance.New(instance.Options{
		Engine: engine.New(8), Registry: reg,
		Limits: capability.ServerLimits{MaxExecTimeout: time.Minute},
	})
	t.Cleanup(mgr.StopReaper)
	workerAPI := httptest.NewServer(remote.NewHandler(mgr, key))
	t.Cleanup(workerAPI.Close)
	rt, err := remote.NewDirect(workerAPI.URL, workerAPI.Client(), key, "readyz-test")
	if err != nil {
		t.Fatal(err)
	}

	ctl := &fakeNodeControl{}
	agent, err := nodeagent.New(nodeagent.Options{
		Control: ctl, Runtime: rt, NodeID: "w1", BootID: "b1",
		AdvertiseAddr: "127.0.0.1:8090", Capacity: 1,
		PollInterval: 20 * time.Millisecond, CallTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := provideServer(config.WorkerConfig{SharedKey: "k"}, rt, agent)
	h := srv.Handler

	if got := readyzCode(t, h); got != http.StatusServiceUnavailable {
		t.Fatalf("readyz before run: %d", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- agent.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("agent run: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("agent.Run did not return after cancel")
		}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !agent.Ready() {
		time.Sleep(10 * time.Millisecond)
	}
	if !agent.Ready() {
		t.Fatal("agent never became ready")
	}
	if got := readyzCode(t, h); got != http.StatusOK {
		t.Fatalf("readyz after registration: %d", got)
	}
	ctl.mu.Lock()
	regAddr, hasReg := "", false
	if ctl.lastReg != nil {
		regAddr, hasReg = ctl.lastReg.Addr, true
	}
	ctl.mu.Unlock()
	if !hasReg || regAddr != "127.0.0.1:8090" {
		t.Fatalf("registration did not carry advertise addr: %q (present=%v)", regAddr, hasReg)
	}
	cancel()
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && agent.Ready() {
		time.Sleep(10 * time.Millisecond)
	}
	if agent.Ready() {
		t.Fatal("ready stuck after shutdown")
	}
	if got := readyzCode(t, h); got != http.StatusServiceUnavailable {
		t.Fatalf("readyz after shutdown: %d", got)
	}
}

func TestWorkerAgentWiring(t *testing.T) {
	base := config.WorkerConfig{
		Addr: "127.0.0.1:0", SharedKey: "test-worker-key", NodeID: "worker-test",
		APIAddr: "http://127.0.0.1:1", AdvertiseAddr: "127.0.0.1:8090",
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
