package instance

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"calcside/internal/capability"
	capext "calcside/internal/capability/ext"
	capfs "calcside/internal/capability/fs"
	capio "calcside/internal/capability/io"
	capnet "calcside/internal/capability/net"
	"calcside/internal/engine"
	"calcside/internal/store"
	_ "calcside/internal/store/gormstore"
	"calcside/internal/types"
)

type recObs struct {
	mu   sync.Mutex
	recs []capability.Record
}

func (o *recObs) Observe(r capability.Record) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.recs = append(o.recs, r)
}

func (o *recObs) all() []capability.Record {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]capability.Record(nil), o.recs...)
}

// examplesDir resolves the repo's examples/capabilities dir.
func examplesDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.Abs(filepath.Join("..", "..", "examples", "capabilities"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(d, "tavily", "capability.yaml")); err != nil {
		t.Fatalf("examples dir: %v", err)
	}
	return d
}

func extMgr(t *testing.T, localRoots []string) (*Manager, store.Store, *store.User, *recObs) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, "sqlite", filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	reg := capability.NewRegistry()
	reg.Register(capfs.Factory())
	reg.Register(capio.Factory())
	reg.Register(capnet.Factory())
	reg.Register(capext.Factory(capext.Options{
		LocalRoots: localRoots,
		CacheDir:   t.TempDir(),
	}))
	limits := defaultLimits()
	limits.NetAllowPrivate = true
	limits.SecretsAllowHTTP = true
	obs := &recObs{}
	m := New(st, engine.New(8), reg, obs, "", time.Second, limits, nil, nil, nil, time.Hour)
	u, _ := st.UpsertUserByEmail(ctx, "u@x.com", "", "")
	return m, st, u, obs
}

func TestExtEndToEnd(t *testing.T) {
	ctx := context.Background()

	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		var req map[string]any
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		gotBody = fmt.Sprint(req["query"])
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"results":[{"title":"t","url":"u","content":"c"}]}`)
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")

	extDir := examplesDir(t)
	m, _, u, obs := extMgr(t, []string{extDir})

	spec := fmt.Sprintf(`{"capabilities":{
		"net": {"allow_hosts": [%q]},
		"ext": {"tavily": {"source": %q, "config": {"base_url": %q}}}
	}, "secrets": {"TAVILY_API_KEY": {"value": "s3cr3t-key", "allowed_domains": [%q]}}}`,
		host, filepath.Join(extDir, "tavily"), srv.URL, host)
	meta, err := m.Create(ctx, u, []byte(spec))
	if err != nil {
		t.Fatal(err)
	}
	res, err := m.Exec(ctx, meta.ID, `r = ext.tavily.search("hi")
print(r[0]["title"])`, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Error != nil {
		t.Fatalf("exec error: %+v", res.Error)
	}
	if res.Output != "t\n" {
		t.Fatalf("output %q", res.Output)
	}
	if gotAuth != "Bearer s3cr3t-key" {
		t.Fatalf("Authorization %q", gotAuth)
	}
	if gotBody != "hi" {
		t.Fatalf("query %q", gotBody)
	}

	// Audit: ext.tavily.search allow then net.post allow; secret absent.
	var extRec, netRec *capability.Record
	for i, r := range obs.all() {
		b, _ := json.Marshal(r)
		if strings.Contains(string(b), "s3cr3t-key") {
			t.Fatalf("secret leaked in audit record: %s", b)
		}
		if r.Call.Capability == types.CapExt && r.Call.Op == "tavily.search" {
			extRec = &obs.all()[i]
		}
		if r.Call.Capability == types.CapNet && r.Call.Op == "post" {
			netRec = &obs.all()[i]
		}
	}
	if extRec == nil || extRec.Decision != types.DecisionAllow {
		t.Fatalf("missing/!allow ext record: %+v", extRec)
	}
	if netRec == nil || netRec.Decision != types.DecisionAllow {
		t.Fatalf("missing/!allow net record: %+v", netRec)
	}
	if extRec.Call.Args["query"] != "hi" {
		t.Fatalf("ext args: %v", extRec.Call.Args)
	}

	// Prompt documents the op.
	pd, err := m.PromptData(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range pd.Parts {
		if p.Factory.Name() == types.CapExt {
			s := p.Factory.Prompt(p.Config)
			if !strings.Contains(s, "ext.tavily.search(query, max_results)") {
				t.Fatalf("prompt missing op line:\n%s", s)
			}
			if !strings.Contains(s, "Secrets used: api_key ← TAVILY_API_KEY") {
				t.Fatalf("prompt missing secrets line:\n%s", s)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("ext not in prompt parts")
	}
}

// TestExtSecretOverride overrides the api_key config with a different
// instance secret.
func TestExtSecretOverride(t *testing.T) {
	ctx := context.Background()
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		fmt.Fprint(w, `{"results":[{"title":"t","url":"u","content":"c"}]}`)
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")

	extDir := examplesDir(t)
	m, _, u, obs := extMgr(t, []string{extDir})
	spec := fmt.Sprintf(`{"capabilities":{
		"net": {"allow_hosts": [%q]},
		"ext": {"tavily": {"source": %q, "config": {"base_url": %q, "api_key": "{{secrets.MY_TAVILY}}"}}}
	}, "secrets": {"MY_TAVILY": {"value": "other-key-9", "allowed_domains": [%q]}}}`,
		host, filepath.Join(extDir, "tavily"), srv.URL, host)
	meta, err := m.Create(ctx, u, []byte(spec))
	if err != nil {
		t.Fatal(err)
	}
	res, err := m.Exec(ctx, meta.ID, `ext.tavily.search("q")`, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Error != nil {
		t.Fatalf("exec error: %+v", res.Error)
	}
	if gotAuth != "Bearer other-key-9" {
		t.Fatalf("Authorization %q", gotAuth)
	}
	for _, r := range obs.all() {
		b, _ := json.Marshal(r)
		if strings.Contains(string(b), "other-key-9") {
			t.Fatalf("secret leaked in audit record: %s", b)
		}
	}
}

func TestExtMissingSecretRef(t *testing.T) {
	ctx := context.Background()
	extDir := examplesDir(t)
	m, _, u, _ := extMgr(t, []string{extDir})
	spec := fmt.Sprintf(`{"capabilities":{
		"net": {"allow_hosts": ["x.example"]},
		"ext": {"tavily": {"source": %q}}}}`,
		filepath.Join(extDir, "tavily"))
	_, err := m.Create(ctx, u, []byte(spec))
	if err == nil || !strings.Contains(err.Error(), `references secret "TAVILY_API_KEY"`) {
		t.Fatalf("want missing-secret error, got %v", err)
	}
}

func TestExtMissingBaseDep(t *testing.T) {
	ctx := context.Background()
	extDir := examplesDir(t)
	m, _, u, _ := extMgr(t, []string{extDir})
	spec := fmt.Sprintf(`{"capabilities":{"ext":{"tavily":{"source":%q}}}}`,
		filepath.Join(extDir, "tavily"))
	_, err := m.Create(ctx, u, []byte(spec))
	if err == nil || !strings.Contains(err.Error(), `requires capability "net"`) {
		t.Fatalf("want requires-capability error, got %v", err)
	}
}

func TestExtNetAllowlistEnforced(t *testing.T) {
	ctx := context.Background()
	extDir := examplesDir(t)
	m, _, u, _ := extMgr(t, []string{extDir})
	spec := fmt.Sprintf(`{"capabilities":{
		"net": {"allow_hosts": ["nowhere.example"]},
		"ext": {"tavily": {"source": %q, "config": {"base_url": "http://127.0.0.1:1"}}}},
		"secrets": {"TAVILY_API_KEY": {"value": "x", "allowed_domains": ["127.0.0.1:1"]}}}`,
		filepath.Join(extDir, "tavily"))
	meta, err := m.Create(ctx, u, []byte(spec))
	if err != nil {
		t.Fatal(err)
	}
	res, err := m.Exec(ctx, meta.ID, `ext.tavily.search("hi")`, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Error == nil || !strings.Contains(res.Error.Message, "not in allow_hosts") {
		t.Fatalf("want allow_hosts error, got %+v", res.Error)
	}
}

// writeTmpExt writes an extension into a dir under root.
func writeTmpExt(t *testing.T, root, name string, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	for rel, src := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestExtInitOutOfScope(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dir := writeTmpExt(t, root, "bad", map[string]string{
		"capability.yaml": "name: bad\nops: [{name: go}]\ndependencies: [net]\n",
		"main.star":       "net.get(url=\"http://x.example\")\ndef go():\n    return 1\n",
	})
	m, _, u, _ := extMgr(t, []string{root})
	spec := fmt.Sprintf(`{"capabilities":{"net":{"allow_hosts":["x.example"]},"ext":{"bad":{"source":%q}}}}`, dir)
	_, err := m.Create(ctx, u, []byte(spec))
	if err == nil || !strings.Contains(err.Error(), "outside its scope") {
		t.Fatalf("want out-of-scope error, got %v", err)
	}
}

func TestExtRemoteDisabled(t *testing.T) {
	ctx := context.Background()
	m, _, u, _ := extMgr(t, nil)
	_, err := m.Create(ctx, u, []byte(
		`{"capabilities":{"ext":{"x":{"source":"github.com/a/b@v1","sum":"h1:z"}}}}`))
	if err == nil {
		t.Fatal("remote source without allow-sources should fail")
	}
}
