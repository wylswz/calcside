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
	"sync/atomic"
	"testing"
	"time"

	"calcside/cmd/calcside-worker/internal/config"
	capext "calcside/internal/capability/ext"
	common "calcside/internal/config"
	"calcside/internal/runtime"
	"calcside/internal/runtime/remote"
)

func TestLocalExtResolverRoundTrip(t *testing.T) {
	// API side: a local root holding one extension tree.
	root := filepath.Join(t.TempDir(), "contrib")
	extDir := filepath.Join(root, "myext")
	mustWrite := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(filepath.Join(extDir, "capability.yaml"), "name: myext\nversion: 0.1.0\n")
	mustWrite(filepath.Join(extDir, "main.star"), "def hello():\n    return 1\n")
	mustWrite(filepath.Join(extDir, "sub", "helpers.star"), "def h():\n    return 2\n")

	srv := httptest.NewServer(extTreeFixture([]string{root}, testKey))
	defer srv.Close()

	cache := t.TempDir()
	resolve := LocalExtResolver(srv.URL, "w1", testKey, cache)
	p := capext.ParsedIdentifier{Local: "contrib/myext"}

	dir, err := resolve(context.Background(), p)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	for _, rel := range []string{"capability.yaml", "main.star", "sub/helpers.star"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); err != nil {
			t.Errorf("missing cached file %s: %v", rel, err)
		}
	}
	got, _ := os.ReadFile(filepath.Join(dir, "main.star"))
	if string(got) != "def hello():\n    return 1\n" {
		t.Fatalf("bad content: %q", got)
	}
	// Second resolve hits the cache (sum unchanged) but must not fail.
	if _, err := resolve(context.Background(), p); err != nil {
		t.Fatalf("cached resolve: %v", err)
	}
	// Outside the roots is refused by the API, not just locally.
	if _, err := resolve(context.Background(), capext.ParsedIdentifier{Local: t.TempDir()}); err == nil {
		t.Fatal("expected containment failure")
	}
	for _, source := range []string{"../myext", "contrib/../myext", "other/myext", extDir} {
		if _, err := resolve(context.Background(), capext.ParsedIdentifier{Local: source}); err == nil {
			t.Errorf("accepted invalid source %q", source)
		}
	}
	mustWrite(filepath.Join(extDir, "main.star"), "def hello():\n    return 3\n")
	mod, err := (&capext.CapabilityLoader{
		Identifier: "contrib/myext",
		Options:    &capext.Options{LocalResolver: resolve},
	}).Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if string(mod.Sources["main.star"]) != "def hello():\n    return 3\n" {
		t.Fatal("worker used a stale extension tree")
	}
}

var testKey = []byte("test-shared-key-0123456789abcdef")

func extTreeFixture(roots []string, key []byte) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := remote.VerifyRequest(key, r, nil, time.Now()); err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet || r.URL.Path != remote.ExtTreePath {
			http.NotFound(w, r)
			return
		}
		opts := capext.Options{LocalRoots: roots}
		dir, err := opts.ResolveLocal(capext.ParsedIdentifier{Local: r.URL.Query().Get("path")}, "")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		files, err := capext.ReadSources(dir)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		sum, err := capext.HashTree(dir)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"files": files, "sum": sum})
	})
}

func TestWorkerLocalExtensionThroughAPI(t *testing.T) {
	for _, mode := range []string{common.IsolationInproc, common.IsolationProcess} {
		t.Run(mode, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "contrib")
			dir := filepath.Join(root, "myext")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			for name, data := range map[string]string{"capability.yaml": "name: myext\nops: [{name: hello}]\n", "main.star": "def hello():\n    return 42\n"} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			var calls atomic.Int64
			fixture := extTreeFixture([]string{root}, testKey)
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Calcside-Node") != "worker-ext" {
					http.Error(w, "wrong node", http.StatusUnauthorized)
					return
				}
				calls.Add(1)
				fixture.ServeHTTP(w, r)
			}))
			t.Cleanup(api.Close)
			cfg := config.WorkerConfig{Addr: "127.0.0.1:0", SharedKey: string(testKey), NodeID: "worker-ext", APIAddr: api.URL, ExtCacheDir: t.TempDir(), DefaultTTL: time.Minute, MaxTTL: time.Hour, MaxExecTimeout: time.Minute, ReaperInterval: time.Hour, MaxConcurrentExecs: 2, InstanceIsolation: mode}
			app, cleanup, err := initializeWorker(cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(cleanup)
			worker := httptest.NewServer(app.Server.Handler)
			t.Cleanup(worker.Close)
			rt, err := remote.NewDirect(worker.URL, worker.Client(), testKey, "api-test")
			if err != nil {
				t.Fatal(err)
			}
			owner := runtime.Owner{UserID: "usr_ext"}
			for i, value := range []int{42, 43} {
				if err := os.WriteFile(filepath.Join(dir, "main.star"), []byte(fmt.Sprintf("def hello():\n    return %d\n", value)), 0o644); err != nil {
					t.Fatal(err)
				}
				sum, err := capext.HashTree(dir)
				if err != nil {
					t.Fatal(err)
				}
				id := fmt.Sprintf("ins_ext_%d", i)
				created, err := rt.Create(t.Context(), &runtime.CreateRequest{InstanceID: id, Owner: owner, Spec: []byte(fmt.Sprintf(`{"capabilities":{"ext":{"myext":{"source":"contrib/myext","sum":%q}}}}`, sum)), ExpiresAt: time.Now().Add(time.Minute)})
				if err != nil {
					t.Fatal(err)
				}
				out, err := rt.Exec(t.Context(), &runtime.ExecRequest{InstanceID: id, Owner: owner, Epoch: created.Epoch, ExecID: "exe_ext", Code: "print(ext.myext.hello())"})
				if err != nil || out.Result.Error != nil || out.Result.Output != fmt.Sprintf("%d\n", value) {
					t.Fatalf("extension exec: %v %+v", err, out)
				}
				if _, err := rt.Delete(t.Context(), &runtime.DeleteRequest{InstanceID: id, Owner: owner, Epoch: created.Epoch}); err != nil {
					t.Fatal(err)
				}
			}
			if calls.Load() < 2 {
				t.Fatalf("worker did not resolve via API: %d", calls.Load())
			}
			_, err = rt.Create(t.Context(), &runtime.CreateRequest{InstanceID: "ins_bad_sum", Owner: owner, Spec: []byte(`{"capabilities":{"ext":{"myext":{"source":"contrib/myext","sum":"h1:wrong"}}}}`), ExpiresAt: time.Now().Add(time.Minute)})
			if err == nil || !strings.Contains(err.Error(), "sum mismatch") {
				t.Fatalf("expected sum validation: %v", err)
			}
		})
	}
}

func TestLocalExtResolverFailures(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		body, want string
	}{
		{"api-error", 400, `{"error":{"kind":"bad_spec","message":"source refused"}}`, "source refused"},
		{"http-error", 503, `unavailable`, "503"},
		{"path-escape", 200, `{"sum":"h1:test","files":{"../escape":"eA=="}}`, "escapes cache dir"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if strings.HasPrefix(tc.body, "{") {
					w.Header().Set("Content-Type", "application/json")
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			_, err := LocalExtResolver(server.URL, "worker", testKey, t.TempDir())(t.Context(), capext.ParsedIdentifier{Local: "contrib/myext"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %s: %v", tc.want, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := LocalExtResolver("http://127.0.0.1:1", "worker", testKey, t.TempDir())(ctx, capext.ParsedIdentifier{Local: "contrib/myext"})
	if err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("ignored cancellation: %v", err)
	}
}
