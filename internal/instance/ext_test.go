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
	"testing"

	"calcside/internal/capability"
	capext "calcside/internal/capability/ext"
	capfs "calcside/internal/capability/fs"
	capio "calcside/internal/capability/io"
	capnet "calcside/internal/capability/net"
	"calcside/internal/runtime"
	"calcside/internal/types"
)

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

func extNode(t *testing.T, localRoots []string) *node {
	t.Helper()
	reg := capability.NewRegistry()
	reg.Register(capfs.Factory())
	reg.Register(capio.Factory())
	reg.Register(capnet.Factory())
	reg.Register(capext.Factory(capext.Options{
		LocalRoots: localRoots,
		CacheDir:   t.TempDir(),
	}))
	limits := defaultLimits()
	limits.SecretsAllowHTTP = true
	return newNode(t, Options{Registry: reg, Limits: limits}, nil)
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
	n := extNode(t, []string{extDir})

	spec := fmt.Sprintf(`{"policies":[],"capabilities":{
		"net": {"allow_hosts": [%q]},
		"ext": {"tavily": {"source": %q, "config": {"base_url": %q}}}
	}, "secrets": {"TAVILY_API_KEY": {"value": "s3cr3t-key", "allowed_domains": [%q]}}}`,
		host, filepath.Join(extDir, "tavily"), srv.URL, host)
	id, err := n.create(spec)
	if err != nil {
		t.Fatal(err)
	}
	res, err := n.exec(id, `r = ext.tavily.search("hi")
print(r[0]["title"])`)
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
	var extRec, netRec *runtime.AuditEvent
	events := n.audit()
	for i, ev := range events {
		b, _ := json.Marshal(ev)
		if strings.Contains(string(b), "s3cr3t-key") {
			t.Fatalf("secret leaked in audit event: %s", b)
		}
		if ev.Capability == types.CapExt && ev.Op == "tavily.search" {
			extRec = &events[i]
		}
		if ev.Capability == types.CapNet && ev.Op == "post" {
			netRec = &events[i]
		}
	}
	if extRec == nil || extRec.Decision != types.DecisionAllow {
		t.Fatalf("missing/!allow ext event: %+v", extRec)
	}
	if netRec == nil || netRec.Decision != types.DecisionAllow {
		t.Fatalf("missing/!allow net event: %+v", netRec)
	}
	if !strings.Contains(extRec.Args, `"query":"hi"`) {
		t.Fatalf("ext args: %s", extRec.Args)
	}

	completions, err := n.m.Inspect(ctx, &runtime.InspectRequest{InstanceID: id, Owner: n.owner, CompletionsOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	foundCompletion := false
	for _, symbol := range completions.Completions.Symbols {
		if symbol.Name == "ext.tavily.search" {
			foundCompletion = symbol.Detail == "(query, max_results)"
		}
	}
	if !foundCompletion {
		t.Fatal("missing loaded extension signature")
	}

	// Prompt documents the op. Fragments are rendered on the node,
	// because only it has the factories and the effective config.
	pd, err := n.m.Prompt(ctx, &runtime.PromptRequest{InstanceID: id, Owner: n.owner})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range pd.Fragments {
		if f.Capability == types.CapExt {
			if !strings.Contains(f.Text, "ext.tavily.search(query, max_results)") {
				t.Fatalf("prompt missing op line:\n%s", f.Text)
			}
			if !strings.Contains(f.Text, "Secrets used: api_key ← TAVILY_API_KEY") {
				t.Fatalf("prompt missing secrets line:\n%s", f.Text)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("ext not in prompt fragments")
	}
}

// TestExtSecretOverride overrides the api_key config with a different
// instance secret.
func TestExtSecretOverride(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		fmt.Fprint(w, `{"results":[{"title":"t","url":"u","content":"c"}]}`)
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")

	extDir := examplesDir(t)
	n := extNode(t, []string{extDir})
	spec := fmt.Sprintf(`{"policies":[],"capabilities":{
		"net": {"allow_hosts": [%q]},
		"ext": {"tavily": {"source": %q, "config": {"base_url": %q, "api_key": "{{secrets.MY_TAVILY}}"}}}
	}, "secrets": {"MY_TAVILY": {"value": "other-key-9", "allowed_domains": [%q]}}}`,
		host, filepath.Join(extDir, "tavily"), srv.URL, host)
	id, err := n.create(spec)
	if err != nil {
		t.Fatal(err)
	}
	res, err := n.exec(id, `ext.tavily.search("q")`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Error != nil {
		t.Fatalf("exec error: %+v", res.Error)
	}
	if gotAuth != "Bearer other-key-9" {
		t.Fatalf("Authorization %q", gotAuth)
	}
	for _, ev := range n.audit() {
		b, _ := json.Marshal(ev)
		if strings.Contains(string(b), "other-key-9") {
			t.Fatalf("secret leaked in audit event: %s", b)
		}
	}
}

func TestExtMissingSecretRef(t *testing.T) {
	extDir := examplesDir(t)
	n := extNode(t, []string{extDir})
	spec := fmt.Sprintf(`{"policies":[],"capabilities":{
		"net": {"allow_hosts": ["x.example"]},
		"ext": {"tavily": {"source": %q}}}}`,
		filepath.Join(extDir, "tavily"))
	_, err := n.create(spec)
	if err == nil || !strings.Contains(err.Error(), `references secret "TAVILY_API_KEY"`) {
		t.Fatalf("want missing-secret error, got %v", err)
	}
}

func TestExtMissingBaseDep(t *testing.T) {
	extDir := examplesDir(t)
	n := extNode(t, []string{extDir})
	spec := fmt.Sprintf(`{"policies":[],"capabilities":{"ext":{"tavily":{"source":%q}}}}`,
		filepath.Join(extDir, "tavily"))
	_, err := n.create(spec)
	if err == nil || !strings.Contains(err.Error(), `requires capability "net"`) {
		t.Fatalf("want requires-capability error, got %v", err)
	}
}

func TestExtNetAllowlistEnforced(t *testing.T) {
	extDir := examplesDir(t)
	n := extNode(t, []string{extDir})
	spec := fmt.Sprintf(`{"policies":[],"capabilities":{
		"net": {"allow_hosts": ["nowhere.example"]},
		"ext": {"tavily": {"source": %q, "config": {"base_url": "http://127.0.0.1:1"}}}},
		"secrets": {"TAVILY_API_KEY": {"value": "x", "allowed_domains": ["127.0.0.1:1"]}}}`,
		filepath.Join(extDir, "tavily"))
	id, err := n.create(spec)
	if err != nil {
		t.Fatal(err)
	}
	res, err := n.exec(id, `ext.tavily.search("hi")`)
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
	root := t.TempDir()
	dir := writeTmpExt(t, root, "bad", map[string]string{
		"capability.yaml": "name: bad\nops: [{name: go}]\ndependencies: [net]\n",
		"main.star":       "net.get(url=\"http://x.example\")\ndef go():\n    return 1\n",
	})
	n := extNode(t, []string{root})
	spec := fmt.Sprintf(`{"policies":[],"capabilities":{"net":{"allow_hosts":["x.example"]},"ext":{"bad":{"source":%q}}}}`, dir)
	_, err := n.create(spec)
	if err == nil || !strings.Contains(err.Error(), "outside its scope") {
		t.Fatalf("want out-of-scope error, got %v", err)
	}
}

func TestExtRemoteDisabled(t *testing.T) {
	n := extNode(t, nil)
	_, err := n.create(`{"policies":[],"capabilities":{"ext":{"x":{"source":"github.com/a/b@v1","sum":"h1:z"}}}}`)
	if err == nil {
		t.Fatal("remote source without allow-sources should fail")
	}
}
