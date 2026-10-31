package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"calcside/cmd/calcside-worker/internal/config"
	"calcside/cmd/calcside-worker/internal/extcache"
	"calcside/internal/capability"
	"calcside/internal/engine"
	"calcside/internal/extartifact"
	"calcside/internal/runtime"
	"calcside/internal/runtime/instance"
	"calcside/internal/runtime/remote"
	"calcside/internal/runtime/subproc"
)

var extTestKey = []byte("test-shared-key-0123456789abcdef")

func extSpec(source, sum string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"capabilities": map[string]any{
		"ext": map[string]any{"x": map[string]any{"source": source, "sum": sum}},
	}})
	return b
}

// A child carries only a cache resolver — never API credentials.
func TestConfigureChildCacheOnly(t *testing.T) {
	dir := t.TempDir()
	cfg := &subproc.ChildConfig{}
	cfg.Node.ExtCacheDir = dir
	if err := configureChild(cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Node.ExtSourceResolver == nil {
		t.Fatal("configureChild set no source resolver")
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"api_addr", "api_key", string(extTestKey), "shared-key", "node_id"} {
		if strings.Contains(string(raw), banned) {
			t.Fatalf("child config leaks %q", banned)
		}
	}
}

// CacheResolver is read-only: a miss is a fixed error, never a fetch.
func TestCacheResolverMissNoFetch(t *testing.T) {
	dir := t.TempDir()
	resolve := extcache.CacheResolver(dir)
	files := map[string][]byte{"capability.yaml": []byte("name: x\n")}
	sum, err := extartifact.TreeHash(files)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolve(context.Background(), "contrib/x", sum); err == nil {
		t.Fatal("missing artifact resolved")
	}
	if _, err := extartifact.Install(dir, "contrib/x", sum, files); err != nil {
		t.Fatal(err)
	}
	if got, err := resolve(context.Background(), "contrib/x", sum); err != nil || got == "" {
		t.Fatalf("installed: %v %q", err, got)
	}
	if _, err := resolve(context.Background(), "../x", sum); !errors.Is(err, runtime.ErrBadSpec) {
		t.Fatalf("bad source: %v", err)
	}
}

// Ensure blocks the caller until the synchronous fetch lands — the
// wrapped runtime never sees Create/Prepare before the artifact is
// installed.
func TestEnsureBlocksUntilInstalled(t *testing.T) {
	files := map[string][]byte{"capability.yaml": []byte("name: x\n")}
	sum, _ := extartifact.TreeHash(files)
	var fetches atomic.Int32
	release := make(chan struct{})
	fetch := func(ctx context.Context, source, sum string) (map[string][]byte, string, error) {
		fetches.Add(1)
		<-release // hold until the test lets it through
		return files, sum, nil
	}
	dir := t.TempDir()
	c, err := extcache.New(dir, fetch, 0)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- c.Ensure(context.Background(), extSpec("contrib/x", sum)) }()
	time.Sleep(200 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("Ensure returned before the fetch landed: %v", err)
	default:
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	// second Ensure: verified cache hit — zero additional fetches
	if err := c.Ensure(context.Background(), extSpec("contrib/x", sum)); err != nil {
		t.Fatal(err)
	}
	if n := fetches.Load(); n != 1 {
		t.Fatalf("cache hit refetched: %d", n)
	}
}

// A canceled caller waiting inside the fetch must not register an
// instance — WithRuntime rechecks ctx before delegating.
func TestCanceledEnsureNeverCreates(t *testing.T) {
	fetch := func(ctx context.Context, source, sum string) (map[string][]byte, string, error) {
		<-ctx.Done()
		return nil, "", ctx.Err()
	}
	c, _ := extcache.New(t.TempDir(), fetch, 0)
	m := newManager(t)
	rt := extcache.WithRuntime(m, c)
	sum, _ := extartifact.TreeHash(map[string][]byte{"a": []byte("b")})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	resp, err := rt.Create(ctx, &runtime.CreateRequest{Spec: extSpec("contrib/x", sum)})
	if err == nil || resp != nil {
		t.Fatal("canceled create ran")
	}
	if m.Count() != 0 {
		t.Fatal("canceled create registered an instance")
	}
}

// Mismatched sums, malformed refs, oversized bodies and bad HMAC all
// fail closed — no instance is registered.
func TestFetchFailuresFailClosed(t *testing.T) {
	files := map[string][]byte{"a": []byte("b")}
	sum, _ := extartifact.TreeHash(files)
	for _, tc := range []struct {
		name     string
		spec     json.RawMessage
		fetch    extcache.Fetch
		wantSpec bool
	}{
		{"sum mismatch", extSpec("contrib/x", sum), func(ctx context.Context, s, su string) (map[string][]byte, string, error) {
			return files, "h1:" + strings.Repeat("0", 43) + "=", nil
		}, false},
		{"bad source", extSpec("../x", sum), func(ctx context.Context, s, su string) (map[string][]byte, string, error) {
			return files, su, nil
		}, true},
		{"fetch error", extSpec("contrib/x", sum), func(ctx context.Context, s, su string) (map[string][]byte, string, error) {
			return nil, "", errors.New("boom")
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := extcache.New(t.TempDir(), tc.fetch, 0)
			m := newManager(t)
			rt := extcache.WithRuntime(m, c)
			if _, err := rt.Prepare(context.Background(), &runtime.PrepareRequest{CreateRequest: runtime.CreateRequest{Spec: tc.spec}}); err == nil {
				t.Fatal("expected failure")
			}
			if m.Count() != 0 {
				t.Fatal("failed fetch registered an instance")
			}
		})
	}
}

// The authenticated fetch: exact source/sum/node + real HMAC verify,
// and a >2MiB VALID JSON body fails closed.
func TestAuthenticatedFetchAndOversize(t *testing.T) {
	files := map[string][]byte{"a": []byte("b")}
	sum, _ := extartifact.TreeHash(files)
	var gotSrc, gotSum, gotNode string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := remote.VerifyRequest(extTestKey, r, body, time.Now()); err != nil {
			w.WriteHeader(401)
			return
		}
		gotSrc, gotSum = r.URL.Query().Get("path"), r.URL.Query().Get("sum")
		gotNode = r.Header.Get("X-Calcside-Node")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"sum": sum, "files": files})
	}))
	defer srv.Close()

	c, err := provideExtCache(config.WorkerConfig{APIAddr: srv.URL, SharedKey: string(extTestKey), ExtCacheDir: t.TempDir()}, "w1")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Ensure(context.Background(), extSpec("contrib/x", sum)); err != nil {
		t.Fatal(err)
	}
	if gotSrc != "contrib/x" || gotSum != sum || gotNode != "w1" {
		t.Fatalf("fetch identity: %q %q %q", gotSrc, gotSum, gotNode)
	}

	// oversize VALID JSON — without the 2MiB bound this installs
	big := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"sum": sum, "files": files, "padding": strings.Repeat("x", 3<<20),
		})
	}))
	defer big.Close()
	c2, _ := extcache.New(t.TempDir(), func(ctx context.Context, s, su string) (map[string][]byte, string, error) {
		req, _ := http.NewRequestWithContext(ctx, "GET", big.URL+"/internal/v1/ext/tree?path="+s+"&sum="+su, nil)
		resp, err := (&boundedClient{}).Do(req)
		if err != nil {
			return nil, "", err
		}
		defer resp.Body.Close()
		var body struct {
			Sum   string            `json:"sum"`
			Files map[string][]byte `json:"files"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			return nil, "", err
		}
		return body.Files, body.Sum, nil
	}, 0)
	if err := c2.Ensure(context.Background(), extSpec("contrib/x", sum)); err == nil {
		t.Fatal("oversize artifact installed — bound missing")
	}
}

// newManager is the real execution fixture: a node that registers any
// request reaching it, so tests can assert a rejected fetch left no
// instance behind.
func newManager(t *testing.T) *instance.Manager {
	t.Helper()
	m := instance.New(instance.Options{
		Engine: engine.New(8), Registry: capability.NewRegistry(),
	})
	t.Cleanup(m.StopReaper)
	return m
}

// The configured timeout bounds the fetch even when the caller's
// deadline is far later; a post-deadline valid response still fails.
func TestEnsureDeadlineAlwaysBounded(t *testing.T) {
	files := map[string][]byte{"a": []byte("b")}
	sum, _ := extartifact.TreeHash(files)
	for _, tc := range []struct {
		name          string
		parentTimeout time.Duration
		validLate     bool
	}{
		{"no parent deadline", 0, false},
		{"10s parent deadline", 10 * time.Second, false},
		{"misbehaving fetcher returns valid late", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := extcache.New(t.TempDir(), func(ctx context.Context, s, su string) (map[string][]byte, string, error) {
				<-ctx.Done()
				if tc.validLate {
					return files, su, nil // ignores cancel — must still fail
				}
				return nil, "", ctx.Err()
			}, 20*time.Millisecond)
			if err != nil {
				t.Fatal(err)
			}
			m := newManager(t)
			rt := extcache.WithRuntime(m, c)
			pctx := context.Background()
			if tc.parentTimeout > 0 {
				var cancel context.CancelFunc
				pctx, cancel = context.WithTimeout(pctx, tc.parentTimeout)
				defer cancel()
			}
			done := make(chan error, 1)
			go func() {
				_, err := rt.Create(pctx, &runtime.CreateRequest{Spec: extSpec("contrib/x", sum)})
				done <- err
			}()
			select {
			case err := <-done:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("expected DeadlineExceeded, got %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("Ensure did not honor the configured timeout")
			}
			if m.Count() != 0 {
				t.Fatal("deadline failure registered an instance")
			}
		})
	}
}

// The semaphore bounds concurrent fetches to 4; identical content
// installs atomically without extra fetches on a verified cache hit.
func TestEnsureBoundedConcurrencyAndCacheHit(t *testing.T) {
	var active, maxActive atomic.Int32
	barrier := make(chan struct{})
	var calls atomic.Int32
	c, err := extcache.New(t.TempDir(), func(ctx context.Context, s, su string) (map[string][]byte, string, error) {
		n := active.Add(1)
		for {
			m := maxActive.Load()
			if n <= m || maxActive.CompareAndSwap(m, n) {
				break
			}
		}
		calls.Add(1)
		<-barrier
		active.Add(-1)
		sum2, _ := extartifact.TreeHash(map[string][]byte{"a": []byte(s)})
		return map[string][]byte{"a": []byte(s)}, sum2, nil
	}, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		src := "contrib/x" + string(rune('0'+i))
		sum, _ := extartifact.TreeHash(map[string][]byte{"a": []byte(src)})
		spec := extSpec(src, sum)
		wg.Add(1)
		go func() { defer wg.Done(); errs <- c.Ensure(context.Background(), spec) }()
	}
	time.Sleep(200 * time.Millisecond)
	close(barrier)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if m := maxActive.Load(); m > 4 {
		t.Fatalf("concurrency bound violated: %d", m)
	}
	// verified cache hit → zero additional fetch calls
	before := calls.Load()
	for i := 0; i < 8; i++ {
		src := "contrib/x" + string(rune('0'+i))
		sum, _ := extartifact.TreeHash(map[string][]byte{"a": []byte(src)})
		if err := c.Ensure(context.Background(), extSpec(src, sum)); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != before {
		t.Fatalf("cache hit refetched: %d -> %d", before, calls.Load())
	}
}
