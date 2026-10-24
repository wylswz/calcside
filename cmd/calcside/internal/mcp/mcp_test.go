package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"calcside/cmd/calcside/internal/audit"
	"calcside/cmd/calcside/internal/auth"
	_ "calcside/cmd/calcside/internal/gormstore"
	"calcside/cmd/calcside/internal/service/sandbox"
	"calcside/cmd/calcside/internal/service/vault"
	"calcside/internal/capability"
	capfs "calcside/internal/capability/fs"
	capio "calcside/internal/capability/io"
	"calcside/internal/engine"
	"calcside/internal/instance"
	"calcside/internal/store"
	"calcside/internal/store/storetest"
	"calcside/internal/types"
)

type bearerRT struct{ tok string }

func (b bearerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r2 := r.Clone(r.Context())
	r2.Header.Set("Authorization", "Bearer "+b.tok)
	return http.DefaultTransport.RoundTrip(r2)
}

func newEnv(t *testing.T) (*httptest.Server, store.Store, *audit.Recorder, string, string) {
	st, err := store.Open(context.Background(), "sqlite", storetest.SQLite(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	reg := capability.NewRegistry()
	reg.Register(capfs.Factory())
	reg.Register(capio.Factory())
	limits := capability.ServerLimits{DefaultTTL: 15 * time.Minute, MaxTTL: 24 * time.Hour, MaxExecTimeout: 5 * time.Minute, MaxFSQuotaBytes: 256 << 20}
	mgr := instance.New(instance.Options{Engine: engine.New(8), Registry: reg, Limits: limits, EvalTimeout: time.Second, ReapInterval: time.Hour})
	t.Cleanup(mgr.StopReaper)
	rec := audit.NewRecorder(st)
	t.Cleanup(rec.Close)
	sbx := sandbox.New(sandbox.Options{Store: st, Runtime: mgr, Secrets: vault.New(st, nil), Audit: rec, Registry: reg, Limits: limits, MaxInstancesPerUser: 10})

	// Auth resolution against the store, as the api middleware does.
	authsvc := auth.NewService(st, false)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, err := authsvc.Resolve(r.Context(), r)
		if err == nil && p != nil {
			r = r.WithContext(auth.WithPrincipal(r.Context(), p))
		}
		Handler(sbx).ServeHTTP(w, r)
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	// user + keys
	u, _ := st.UpsertUserByEmail(context.Background(), "m@x.com", "", "")
	spec := `{"ttl_seconds":600,"capabilities":{"fs":{}}}`
	secret, restricted := auth.NewAPIKey(u.ID, "rk", nil)
	restricted.Spec = []byte(spec)
	restricted.MaxInstances = 2
	if err := st.CreateAPIKey(context.Background(), restricted); err != nil {
		t.Fatal(err)
	}
	secret2, full := auth.NewAPIKey(u.ID, "fk", nil)
	if err := st.CreateAPIKey(context.Background(), full); err != nil {
		t.Fatal(err)
	}
	return srv, st, rec, secret, secret2
}

func connect(t *testing.T, url, tok string) *sdkmcp.ClientSession {
	c := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test"}, nil)
	cs, err := c.Connect(context.Background(), &sdkmcp.StreamableClientTransport{
		Endpoint: url, HTTPClient: &http.Client{Transport: bearerRT{tok}},
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func toolText(t *testing.T, res *sdkmcp.CallToolResult) string {
	t.Helper()
	if len(res.Content) == 0 {
		t.Fatal("no content")
	}
	tc, ok := res.Content[0].(*sdkmcp.TextContent)
	if !ok {
		t.Fatalf("not text: %T", res.Content[0])
	}
	return tc.Text
}

func TestMCPTools(t *testing.T) {
	srv, st, _, secret, fullSecret := newEnv(t)

	// full-access key rejected
	code := 0
	req, _ := http.NewRequest("POST", srv.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"x","version":"0"}}}`))
	req.Header.Set("Authorization", "Bearer "+fullSecret)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		code = resp.StatusCode
		resp.Body.Close()
	}
	if code != http.StatusForbidden {
		t.Fatalf("full-access key: %d", code)
	}
	// unauthenticated → 401
	resp2, err := http.Post(srv.URL+"/mcp", "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"x","version":"0"}}}`))
	if err != nil || resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauth: %v %v", err, resp2.StatusCode)
	}
	resp2.Body.Close()

	cs := connect(t, srv.URL, secret)

	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range tools.Tools {
		names = append(names, tl.Name)
	}
	for _, want := range []string{"create_instance", "exec", "list_files", "read_file", "delete_instance"} {
		found := false
		for _, n := range names {
			if n == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing tool %s in %v", want, names)
		}
	}

	// create → exec → read_file → delete round trip
	res, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "create_instance", Arguments: map[string]any{"labels": map[string]any{"task": "mcp"}}})
	if err != nil || res.IsError {
		t.Fatalf("create_instance: %v %+v", err, res)
	}
	body := toolText(t, res)
	var created struct {
		InstanceID string `json:"instance_id"`
		Prompt     string `json:"prompt"`
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatal(err)
	}
	if created.InstanceID == "" || created.Prompt == "" {
		t.Fatalf("create result: %s", body)
	}
	if !strings.Contains(created.Prompt, "help(") {
		t.Fatalf("prompt lacks help hint: %.200s", created.Prompt)
	}

	res, err = cs.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "exec", Arguments: map[string]any{
		"instance_id": created.InstanceID,
		"code":        "print(help(\"stats\"))\nfs.write(\"x.txt\", \"hello\")",
	}})
	if err != nil || res.IsError {
		t.Fatalf("exec: %v %+v", err, res)
	}
	if out := toolText(t, res); !strings.Contains(out, "stats.ols(") {
		t.Fatalf("exec output lacks help: %q", out)
	}

	res, err = cs.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "read_file", Arguments: map[string]any{
		"instance_id": created.InstanceID, "path": "x.txt",
	}})
	if err != nil || res.IsError {
		t.Fatalf("read_file: %v %+v", err, res)
	}
	if !strings.Contains(toolText(t, res), "hello") {
		t.Fatalf("read_file: %s", toolText(t, res))
	}

	// another restricted key cannot reach this instance
	u2, _ := st.UpsertUserByEmail(context.Background(), "n@x.com", "", "")
	s2, k2 := auth.NewAPIKey(u2.ID, "rk2", nil)
	k2.Spec = []byte(`{"ttl_seconds":600,"capabilities":{"fs":{}}}`)
	k2.MaxInstances = 1
	if err := st.CreateAPIKey(context.Background(), k2); err != nil {
		t.Fatal(err)
	}
	cs2 := connect(t, srv.URL, s2)
	res, err = cs2.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "read_file", Arguments: map[string]any{
		"instance_id": created.InstanceID, "path": "x.txt",
	}})
	if err == nil && !res.IsError {
		t.Fatalf("other key reached instance: %+v", res)
	}

	// exec error carries type
	res, _ = cs.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "exec", Arguments: map[string]any{
		"instance_id": created.InstanceID, "code": "1/0",
	}})
	if res == nil || !res.IsError || !strings.Contains(toolText(t, res), "error[") {
		t.Fatalf("exec error: %+v", res)
	}

	res, err = cs.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "delete_instance", Arguments: map[string]any{
		"instance_id": created.InstanceID,
	}})
	if err != nil || res.IsError {
		t.Fatalf("delete: %v %+v", err, res)
	}

	// exec recorded via api_key and audited
	exs, _ := st.ListExecutions(context.Background(), created.InstanceID, 50)
	var sawVia bool
	for _, ex := range exs {
		if ex.Via == types.AuthAPIKey {
			sawVia = true
		}
	}
	if !sawVia {
		t.Fatal("no execution recorded with via=api_key")
	}
	var evs []*store.AuditEvent
	for range 20 {
		evs, _ = st.ListAuditEvents(context.Background(), store.AuditFilter{InstanceID: created.InstanceID, Limit: 50})
		if len(evs) > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(evs) == 0 {
		t.Fatal("no audit events recorded")
	}
}

func TestMCPNoFS(t *testing.T) {
	srv, st, _, _, _ := newEnv(t)
	u, _ := st.UpsertUserByEmail(context.Background(), "nf@x.com", "", "")
	s3, k3 := auth.NewAPIKey(u.ID, "nofk", nil)
	k3.Spec = []byte(`{"ttl_seconds":600}`)
	k3.MaxInstances = 1
	if err := st.CreateAPIKey(context.Background(), k3); err != nil {
		t.Fatal(err)
	}
	cs := connect(t, srv.URL, s3)
	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range tools.Tools {
		if tl.Name == "list_files" || tl.Name == "read_file" {
			t.Fatalf("fs tool listed without fs grant: %v", tl.Name)
		}
	}
}
