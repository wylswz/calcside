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
	capio "calcside/internal/capability/io"
	"calcside/internal/engine"
	"calcside/internal/instance"
	"calcside/internal/placement"
	"calcside/internal/runtime"
	"calcside/internal/runtime/remote"
)

var testKey = []byte("test-shared-key-0123456789abcdef")

// newWorker stands up an execution node behind the authenticated
// transport. The manager it wraps holds no store handle — everything it
// knows arrives inside the request.
func newWorker(t *testing.T, key []byte) (addr string) {
	t.Helper()
	reg := capability.NewRegistry()
	reg.Register(capio.Factory())
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
