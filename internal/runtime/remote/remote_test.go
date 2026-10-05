package remote_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"calcside/internal/capability"
	capfs "calcside/internal/capability/fs"
	capio "calcside/internal/capability/io"
	"calcside/internal/engine"
	"calcside/internal/instance"
	"calcside/internal/placement"
	"calcside/internal/runtime"
	"calcside/internal/runtime/remote"
	"calcside/internal/types"
)

var testKey = []byte("test-shared-key-0123456789abcdef")

// newWorker stands up an execution node behind the authenticated
// transport. The manager it wraps holds no store handle — everything it
// knows arrives inside the request.
func newWorker(t *testing.T, key []byte) (addr string) {
	t.Helper()
	reg := capability.NewRegistry()
	reg.Register(capio.Factory())
	reg.Register(capfs.Factory())
	mgr := instance.New(instance.Options{
		Engine:   engine.New(8),
		Registry: reg,
		Limits: capability.ServerLimits{
			DefaultTTL:     15 * time.Minute,
			MaxTTL:         24 * time.Hour,
			MaxExecTimeout: 5 * time.Minute,
		},
	})
	t.Cleanup(mgr.StopReaper)
	srv := httptest.NewServer(remote.NewHandler(mgr, key))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

func newClient(addr string, bindings map[string]*placement.Binding) *remote.Client {
	return remote.NewClient(testKey, "api-1",
		func(context.Context) ([]placement.NodeRef, error) {
			return []placement.NodeRef{{NodeID: "w1", Addr: addr}}, nil
		},
		func(_ context.Context, id string) (*placement.Binding, error) {
			b, ok := bindings[id]
			if !ok {
				return nil, placement.ErrNotBound
			}
			return b, nil
		})
}

func createReq(id string) *runtime.CreateRequest {
	return &runtime.CreateRequest{
		InstanceID: id,
		Owner:      runtime.Owner{UserID: "usr_1", Email: "u@x.com"},
		Spec:       []byte(`{"capabilities":{}}`),
		ExpiresAt:  time.Now().Add(time.Hour),
	}
}

func TestRemoteExecRoundTrip(t *testing.T) {
	addr := newWorker(t, testKey)
	bindings := map[string]*placement.Binding{}
	rt := newClient(addr, bindings)
	ctx := context.Background()
	owner := runtime.Owner{UserID: "usr_1", Email: "u@x.com"}

	cresp, err := rt.Create(ctx, createReq("ins_1"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if cresp.NodeID != "w1" || cresp.Epoch != 1 {
		t.Fatalf("binding not reported: %+v", cresp)
	}
	// The API tier persists the binding the response reported.
	bindings["ins_1"] = &placement.Binding{InstanceID: "ins_1", NodeID: cresp.NodeID, Epoch: cresp.Epoch}

	resp, err := rt.Exec(ctx, &runtime.ExecRequest{
		InstanceID: "ins_1", Owner: owner, Epoch: 1, ExecID: "exe_1",
		Code: "calls = 0",
	})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	resp, err = rt.Exec(ctx, &runtime.ExecRequest{
		InstanceID: "ins_1", Owner: owner, Epoch: 1, ExecID: "exe_2",
		Code: "calls += 1\nprint(calls)",
	})
	if err != nil || resp.Result.Output != "1\n" {
		t.Fatalf("exec: %v out=%q", err, resp.Result.Output)
	}
	// Idempotency: replaying the same exec_id must return the recorded
	// result, not re-run — a re-run would print 2.
	resp, err = rt.Exec(ctx, &runtime.ExecRequest{
		InstanceID: "ins_1", Owner: owner, Epoch: 1, ExecID: "exe_2",
		Code: "calls += 1\nprint(calls)",
	})
	if err != nil || resp.Result.Output != "1\n" {
		t.Fatalf("dedup replay: %v out=%q", err, resp.Result.Output)
	}

	// Inspect rides the same authenticated transport.
	vars, err := rt.Inspect(ctx, &runtime.InspectRequest{InstanceID: "ins_1", Owner: owner, Epoch: 1})
	if err != nil || vars.Variables["calls"] != "1" {
		t.Fatalf("inspect: %v vars=%v", err, vars.Variables)
	}
}

func TestRemoteCompletions(t *testing.T) {
	addr := newWorker(t, testKey)
	bindings := map[string]*placement.Binding{}
	rt := newClient(addr, bindings)
	ctx := context.Background()
	r, err := rt.Create(ctx, createReq("ins_completion"))
	if err != nil {
		t.Fatal(err)
	}
	bindings["ins_completion"] = &placement.Binding{InstanceID: "ins_completion", NodeID: r.NodeID, Epoch: r.Epoch}
	req := &runtime.InspectRequest{InstanceID: "ins_completion", Owner: runtime.Owner{UserID: "usr_1"}, Epoch: r.Epoch, CompletionsOnly: true}
	out, err := rt.Inspect(ctx, req)
	if err != nil || out.Completions == nil || len(out.Completions.Symbols) == 0 || out.Variables != nil {
		t.Fatalf("completion transport: %v %+v", err, out)
	}
	req.Epoch++
	if _, err := rt.Inspect(ctx, req); !errors.Is(err, runtime.ErrStaleEpoch) {
		t.Fatalf("stale completion route: %v", err)
	}
	req.Epoch = r.Epoch
	req.Owner.UserID = "usr_other"
	if _, err := rt.Inspect(ctx, req); !errors.Is(err, runtime.ErrNotOwner) {
		t.Fatalf("foreign completions: %v", err)
	}
}

func TestRemoteRejectsStaleEpochAndForeignOwner(t *testing.T) {
	addr := newWorker(t, testKey)
	bindings := map[string]*placement.Binding{}
	rt := newClient(addr, bindings)
	ctx := context.Background()

	if _, err := rt.Create(ctx, createReq("ins_1")); err != nil {
		t.Fatalf("create: %v", err)
	}
	bindings["ins_1"] = &placement.Binding{InstanceID: "ins_1", NodeID: "w1", Epoch: 1}

	// A caller working off a stale binding must be fenced out.
	_, err := rt.Exec(ctx, &runtime.ExecRequest{
		InstanceID: "ins_1", Owner: runtime.Owner{UserID: "usr_1"},
		Epoch: 9, ExecID: "exe_1", Code: "print(1)",
	})
	if !errors.Is(err, runtime.ErrStaleEpoch) {
		t.Fatalf("want ErrStaleEpoch, got %v", err)
	}

	// And an API-tier bug must not become cross-tenant execution.
	_, err = rt.Exec(ctx, &runtime.ExecRequest{
		InstanceID: "ins_1", Owner: runtime.Owner{UserID: "usr_2"},
		Epoch: 1, ExecID: "exe_2", Code: "print(1)",
	})
	if !errors.Is(err, runtime.ErrNotOwner) {
		t.Fatalf("want ErrNotOwner, got %v", err)
	}
}

func TestRemoteAuth(t *testing.T) {
	addr := newWorker(t, testKey)

	// Unsigned request: rejected before reaching the runtime.
	resp, err := http.Post(fmt.Sprintf("http://%s/runtime/v1/exec", addr), "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unsigned request: got %d", resp.StatusCode)
	}

	// Wrong key: same.
	rt := remote.NewClient([]byte("wrong-key-wrong-key-wrong-key!"), "api-1",
		func(context.Context) ([]placement.NodeRef, error) {
			return []placement.NodeRef{{NodeID: "w1", Addr: addr}}, nil
		},
		nil)
	if _, err := rt.Create(context.Background(), createReq("ins_x")); err == nil {
		t.Fatal("expected auth failure")
	}

	// A worker with no key configured is unreachable, not open.
	addr2 := newWorker(t, nil)
	resp, err = http.Post(fmt.Sprintf("http://%s/runtime/v1/exec", addr2), "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("unconfigured worker: got %d", resp.StatusCode)
	}
}

type contextRuntime struct {
	runtime.Runtime
	ctx context.Context
}

func (r *contextRuntime) Create(ctx context.Context, _ *runtime.CreateRequest) (*runtime.CreateResponse, error) {
	r.ctx = ctx
	return &runtime.CreateResponse{}, nil
}

func TestRuntimeReceivesRequestContext(t *testing.T) {
	rt := &contextRuntime{}
	h := remote.NewHandler(rt, testKey)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest("POST", "/runtime/v1/create", strings.NewReader(`{}`)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	remote.SignRequest(testKey, "api-test", req, []byte(`{}`), time.Now())
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("request failed: %d %s", w.Code, w.Body.String())
	}
	if rt.ctx != ctx {
		t.Fatalf("runtime received pooled context %T instead of request context", rt.ctx)
	}
	cancel()
	if !errors.Is(rt.ctx.Err(), context.Canceled) {
		t.Fatal("request cancellation not propagated")
	}
}

func TestRemoteArtifactExport(t *testing.T) {
	addr := newWorker(t, testKey)
	bindings := map[string]*placement.Binding{}
	client := newClient(addr, bindings)
	ctx := context.Background()
	req := createReq("ins_artifact")
	req.Spec = []byte(`{"capabilities":{"fs":{}}}`)
	req.Policies = runtime.PolicyBundle{Global: map[string]string{"deny.rego": `package calcside.hooks
deny contains "blocked" if { input.phase == "after"; input.op == "read"; input.args.path in {"/work/denied.txt", "/work/denied.js"} }`}}
	created, err := client.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	bindings[req.InstanceID] = &placement.Binding{InstanceID: req.InstanceID, NodeID: created.NodeID, Epoch: created.Epoch}
	result, err := client.Exec(ctx, &runtime.ExecRequest{InstanceID: req.InstanceID, Owner: req.Owner, Epoch: created.Epoch, ExecID: "write", Code: `fs.write("ok.csv", "a,b\r\n1,2\r\n"); fs.write("denied.txt", "private"); fs.write("index.html", '<link rel="stylesheet" href="view.css"><script src="view.js" defer></script>'); fs.write("view.css", "h1 { color: red }"); fs.write("view.js", "window.remote=1;"); fs.write("denied.html", '<script type="module">import "./denied.js";</script>'); fs.write("denied.js", "window.private=1;")`})
	if err != nil || result.Result.Error != nil {
		t.Fatal("write failed")
	}
	out, err := client.Export(ctx, &runtime.ExportRequest{InstanceID: req.InstanceID, Owner: req.Owner, Epoch: created.Epoch, Paths: []string{"ok.csv"}})
	if err != nil || out.Error != nil || len(out.Files) != 1 || string(out.Files[0].Content) != "a,b\r\n1,2\r\n" {
		t.Fatalf("round trip failed: %v", err)
	}
	out, err = client.Export(ctx, &runtime.ExportRequest{InstanceID: req.InstanceID, Owner: req.Owner, Epoch: created.Epoch, Paths: []string{"ok.csv", "denied.txt"}, Recursive: true})
	if err != nil || out.Error == nil || len(out.Files) != 0 || len(out.Audit.Events) == 0 {
		t.Fatal("failure or audit lost over HTTP")
	}
	denied := false
	for _, event := range out.Audit.Events {
		if event.Decision == "deny" && event.Phase == "after" {
			denied = true
		}
	}
	if !denied {
		t.Fatal("after-denial was lost over HTTP")
	}
	out, err = client.Export(ctx, &runtime.ExportRequest{InstanceID: req.InstanceID, Owner: req.Owner, Epoch: created.Epoch, Paths: []string{"index.html"}, PreviewMode: "interactive"})
	if err != nil || out.Error != nil || len(out.Files) != 1 || !strings.Contains(string(out.PreviewHTML), "data:text/javascript;charset=utf-8;base64,") || !strings.Contains(string(out.PreviewHTML), "color: red") {
		t.Fatalf("inline preview lost over HTTP: %v", err)
	}
	out, err = client.Export(ctx, &runtime.ExportRequest{InstanceID: req.InstanceID, Owner: req.Owner, Epoch: created.Epoch, Paths: []string{"denied.html"}, PreviewMode: "interactive"})
	if err != nil || out.Error == nil || out.Error.Code != types.ErrCodeForbidden || len(out.Files) != 0 || len(out.PreviewHTML) != 0 {
		t.Fatal("partial inline preview or denial lost over HTTP")
	}
	denied = false
	for _, event := range out.Audit.Events {
		denied = denied || event.Decision == "deny" && event.Phase == "after"
	}
	if !denied {
		t.Fatal("inline denial audit lost over HTTP")
	}

}
