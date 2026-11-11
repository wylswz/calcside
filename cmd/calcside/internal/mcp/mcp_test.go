package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"calcside/cmd/calcside/internal/audit"
	"calcside/cmd/calcside/internal/auth"
	_ "calcside/cmd/calcside/internal/gormstore"
	"calcside/cmd/calcside/internal/service"
	"calcside/cmd/calcside/internal/service/access"
	"calcside/cmd/calcside/internal/service/sandbox"
	"calcside/cmd/calcside/internal/service/vault"
	"calcside/internal/capability"
	capfs "calcside/internal/capability/fs"
	capio "calcside/internal/capability/io"
	"calcside/internal/engine"
	"calcside/internal/runtime/instance"
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

func newEnv(t *testing.T) (*httptest.Server, store.Store, *audit.Recorder, string, string, *sandbox.Service, *store.APIKey, *store.APIKey) {
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
	ac := access.New(st)
	sbx := sandbox.New(sandbox.Options{Store: st, Runtime: mgr, Secrets: vault.New(st, nil, ac), Audit: rec, Registry: reg, Limits: limits, Access: ac, MaxInstancesPerUser: 10})

	// Auth resolution against the store, as the api middleware does.
	authsvc := auth.NewService(st, false)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, err := authsvc.Resolve(r.Context(), r)
		if err == nil && p != nil {
			r = r.WithContext(auth.WithPrincipal(r.Context(), p))
		}
		if strings.HasPrefix(r.URL.Path, "/mcp/instances/") {
			InstanceHandler(sbx, ac, strings.TrimPrefix(r.URL.Path, "/mcp/instances/")).ServeHTTP(w, r)
			return
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
	restricted.WorkspaceID = store.PersonalWorkspaceID(u.ID)
	if err := st.CreateAPIKey(context.Background(), restricted); err != nil {
		t.Fatal(err)
	}
	secret2, full := auth.NewAPIKey(u.ID, "fk", nil)
	// ws set below
	full.WorkspaceID = store.PersonalWorkspaceID(u.ID)
	if err := st.CreateAPIKey(context.Background(), full); err != nil {
		t.Fatal(err)
	}
	return srv, st, rec, secret, secret2, sbx, restricted, full
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
	srv, st, _, secret, fullSecret, _, _, _ := newEnv(t)

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
	// ws set below
	k2.WorkspaceID = store.PersonalWorkspaceID(u2.ID)
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

// TestMCPNotebookTools covers the reduced agent surface: tools/list
// schemas, exec-with-section persistence (including failures), the
// absence of fine-grained authoring tools, and add_section placement.
func TestMCPNotebookTools(t *testing.T) {
	srv, st, _, secret, _, _, _, _ := newEnv(t)
	cs := connect(t, srv.URL, secret)

	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]json.RawMessage{}
	var names []string
	for _, tl := range tools.Tools {
		names = append(names, tl.Name)
		b, _ := json.Marshal(tl)
		byName[tl.Name] = b
	}
	// The reduced surface, exactly: outline/section reads, batch run
	// (canonical run_sections only — no alias), exec with inline
	// section metadata, add_section. No save/move/delete tools and no
	// run alias.
	wantTools := []string{"add_section", "create_instance", "delete_instance", "exec", "list_files", "notebook", "read_file", "read_section", "run_sections"}
	sort.Strings(names)
	if !slices.Equal(names, wantTools) {
		t.Fatalf("tool set = %v, want %v", names, wantTools)
	}
	for _, w := range wantTools {
		if byName[w] == nil {
			t.Fatalf("missing tool %s", w)
		}
	}
	// notebook's schema requires only instance_id — no dummy name.
	var nbTool struct {
		InputSchema struct {
			Required   []string                   `json:"required"`
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"inputSchema"`
	}
	if err := json.Unmarshal(byName["notebook"], &nbTool); err != nil {
		t.Fatal(err)
	}
	if len(nbTool.InputSchema.Required) != 1 || nbTool.InputSchema.Required[0] != "instance_id" {
		t.Fatalf("notebook schema required: %v", nbTool.InputSchema.Required)
	}
	if nbTool.InputSchema.Properties["name"] != nil {
		t.Fatal("notebook schema still exposes name")
	}
	// add_section schema exposes after_section, not revision.
	var addTool struct {
		InputSchema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"inputSchema"`
	}
	if err := json.Unmarshal(byName["add_section"], &addTool); err != nil {
		t.Fatal(err)
	}
	if addTool.InputSchema.Properties["after_section"] == nil {
		t.Fatal("add_section lacks after_section")
	}

	// create an instance through the tool.
	res, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "create_instance", Arguments: map[string]any{}})
	if err != nil || res.IsError {
		t.Fatalf("create_instance: %v %+v", err, res)
	}
	var created struct {
		InstanceID string `json:"instance_id"`
	}
	if err := json.Unmarshal([]byte(toolText(t, res)), &created); err != nil {
		t.Fatal(err)
	}
	id := created.InstanceID

	// exec with section metadata persists the section even when the
	// code fails — the failed intention is kept.
	res, err = cs.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "exec", Arguments: map[string]any{
		"instance_id": id, "code": "def broken(:\n",
		"section": map[string]any{"name": "bad", "description": "broken on purpose"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || !res.IsError {
		t.Fatal("expected exec error result")
	}
	sec, err := st.GetNotebookSection(context.Background(), id, "bad")
	if err != nil {
		t.Fatalf("failed section not persisted: %v", err)
	}
	if sec.Code != "def broken(:\n" || sec.Description != "broken on purpose" {
		t.Fatalf("persisted section: %+v", sec)
	}

	// add_section creates without running; after_section places it.
	res, err = cs.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "add_section", Arguments: map[string]any{
		"instance_id": id, "name": "first", "description": "runs first", "code": "a = 1",
	}})
	if err != nil || res.IsError {
		t.Fatalf("add_section first: %v %+v", err, res)
	}
	res, err = cs.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "add_section", Arguments: map[string]any{
		"instance_id": id, "name": "second", "description": "runs second", "code": "b = a + 1",
		"after_section": "first",
	}})
	if err != nil || res.IsError {
		t.Fatalf("add_section second: %v %+v", err, res)
	}
	// duplicate name → conflict, not silent overwrite.
	res, err = cs.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "add_section", Arguments: map[string]any{
		"instance_id": id, "name": "first", "description": "dup", "code": "c = 9",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || !res.IsError || !strings.Contains(toolText(t, res), "already exists") {
		t.Fatalf("duplicate add_section should conflict: %+v", res)
	}
	// order: bad was created first by exec, then first, then second
	// after first → bad, first, second.
	secs, err := st.ListNotebookSections(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, s := range secs {
		order = append(order, s.Name)
	}
	want := []string{"bad", "first", "second"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("section order: %v want %v", order, want)
	}

	// notebook outline works with instance_id only.
	res, err = cs.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "notebook", Arguments: map[string]any{"instance_id": id}})
	if err != nil || res.IsError {
		t.Fatalf("notebook: %v %+v", err, res)
	}
	if out := toolText(t, res); !strings.Contains(out, "bad") || !strings.Contains(out, "first") {
		t.Fatalf("notebook outline: %q", out)
	}
}

func TestMCPNoFS(t *testing.T) {
	srv, st, _, _, _, _, _, _ := newEnv(t)
	u, _ := st.UpsertUserByEmail(context.Background(), "nf@x.com", "", "")
	s3, k3 := auth.NewAPIKey(u.ID, "nofk", nil)
	k3.Spec = []byte(`{"ttl_seconds":600}`)
	k3.MaxInstances = 1
	// ws set below
	k3.WorkspaceID = store.PersonalWorkspaceID(u.ID)
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

// Instance mode: tools = exec (+fs when the instance has fs);
// instructions carry the agent prompt; only keys with Run may use it.
func TestMCPInstanceMode(t *testing.T) {
	srv, st, _, restrictedSecret, fullSecret, sbx, rk, _ := newEnv(t)
	ctx := context.Background()
	u, _ := st.UpsertUserByEmail(ctx, "m@x.com", "", "")
	a := service.Actor{UserID: u.ID, WorkspaceID: store.PersonalWorkspaceID(u.ID)}
	// create via the restricted key's actor so that key owns the instance
	ra := service.Actor{UserID: u.ID, Kind: types.AuthAPIKey, KeyID: rk.ID,
		KeySpec:         json.RawMessage(`{"ttl_seconds":600,"capabilities":{"fs":{}}}`),
		KeyMaxInstances: 2, WorkspaceID: store.PersonalWorkspaceID(u.ID)}
	in, err := sbx.Create(ctx, ra, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	in2, err := sbx.Create(ctx, a, json.RawMessage(`{"capabilities":{"fs":{}}}`))
	if err != nil {
		t.Fatal(err)
	}
	// bound key
	boundSecret, bound := auth.NewAPIKey(u.ID, "bk", nil)
	bound.WorkspaceID = store.PersonalWorkspaceID(u.ID)
	bound.BoundID = in.ID
	if err := st.CreateAPIKey(ctx, bound); err != nil {
		t.Fatal(err)
	}
	// view-only key
	voSecret, vo := auth.NewAPIKey(u.ID, "vo", nil)
	vo.WorkspaceID = store.PersonalWorkspaceID(u.ID)
	vo.ViewOnly = true
	if err := st.CreateAPIKey(ctx, vo); err != nil {
		t.Fatal(err)
	}
	url := srv.URL + "/mcp/instances/" + in.ID

	tools := func(t *testing.T, cs *sdkmcp.ClientSession) []string {
		res, err := cs.ListTools(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, tl := range res.Tools {
			out = append(out, tl.Name)
		}
		return out
	}
	has := func(list []string, name string) bool {
		for _, n := range list {
			if n == name {
				return true
			}
		}
		return false
	}

	// exec round trip + instructions carry the prompt
	cs := connect(t, url, boundSecret)
	lt := tools(t, cs)
	if !has(lt, "exec") || !has(lt, "list_files") || !has(lt, "read_file") || has(lt, "create_instance") || has(lt, "delete_instance") {
		t.Fatalf("bound key tools: %v", lt)
	}
	res, err := cs.CallTool(ctx, &sdkmcp.CallToolParams{Name: "exec", Arguments: map[string]any{"instance_id": in.ID, "code": "print(41+1)"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(toolText(t, res), "42") {
		t.Fatalf("exec: %v", res)
	}
	// other instance id on the bound key → isError
	res2, _ := cs.CallTool(ctx, &sdkmcp.CallToolParams{Name: "exec", Arguments: map[string]any{"instance_id": in2.ID, "code": "print(1)"}})
	if res2 == nil || !res2.IsError {
		t.Fatal("bound key reached another instance")
	}
	// non-existent instance URL → 403/404 envelope (getServer nil → no tools/session)
	csX := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "x"}, nil)
	_, err = csX.Connect(ctx, &sdkmcp.StreamableClientTransport{Endpoint: srv.URL + "/mcp/instances/ins_zzz", HTTPClient: &http.Client{Transport: bearerRT{boundSecret}}}, nil)
	if err == nil {
		t.Fatal("nonexistent instance connected")
	}
	// full + creator-restricted keys work on the instance URL
	csFull := connect(t, url, fullSecret)
	if !has(mustTools(t, csFull), "exec") {
		t.Fatal("full key missing exec")
	}
	connect(t, url, restrictedSecret)
	// view-only → 403
	code := postInitialize(t, url, voSecret)
	if code != 403 {
		t.Fatalf("view-only: %d", code)
	}
	// bound key on workspace /mcp → 403
	code = postInitialize(t, srv.URL+"/mcp", boundSecret)
	if code != 403 {
		t.Fatalf("bound on /mcp: %d", code)
	}
	// no key → 401
	req, _ := http.NewRequest("POST", url, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"x","version":"0"}}}`))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 401 {
		t.Fatalf("no key: %d", resp.StatusCode)
	}
}

func mustTools(t *testing.T, cs *sdkmcp.ClientSession) []string {
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, tl := range res.Tools {
		out = append(out, tl.Name)
	}
	return out
}

func postInitialize(t *testing.T, url, tok string) int {
	req, _ := http.NewRequest("POST", url, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"x","version":"0"}}}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode
}
