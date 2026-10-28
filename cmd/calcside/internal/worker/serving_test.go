package worker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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
)

var wkey = []byte("0123456789abcdef0123456789abcdef")

type fakeNodes struct{ c *atomic.Int64 }

func (f *fakeNodes) list(ctx context.Context) ([]placement.NodeRef, error) {
	f.c.Add(1)
	return nil, nil
}

// servingClient builds a Client whose node URL points at a real
// manager behind httptest+Direct, or at nil nodes.
func servingClient(t *testing.T, m *instance.Manager, resolveErr error, nodesCalls *atomic.Int64) (*Client, *httptest.Server) {
	t.Helper()
	var srv *httptest.Server
	var addr, nodeID string
	if m != nil {
		srv = httptest.NewServer(remote.NewHandler(m, wkey))
		t.Cleanup(srv.Close)
		addr = strings.TrimPrefix(srv.URL, "http://")
		nodeID = "w1"
	}
	nc := nodesCalls
	if nc == nil {
		nc = &atomic.Int64{}
	}
	cl := NewClient(wkey, "api",
		func(ctx context.Context) ([]placement.NodeRef, error) {
			nc.Add(1)
			if m == nil {
				return nil, nil
			}
			return []placement.NodeRef{{NodeID: nodeID, Addr: addr}}, nil
		},
		func(ctx context.Context, id string) (*placement.Binding, error) {
			if resolveErr != nil {
				return nil, resolveErr
			}
			return &placement.Binding{NodeID: nodeID}, nil
		})
	return cl, srv
}

func servingSpec() json.RawMessage {
	return json.RawMessage(`{"ttl_seconds":300,"limits":{"exec_timeout_ms":30000,"max_steps":1000000},"capabilities":{"fs":{},"io":{}}}`)
}

func TestClientPrepareInvokeRoundtrip(t *testing.T) {
	reg := capability.NewRegistry()
	reg.Register(capfs.Factory())
	reg.Register(capio.Factory())
	m := instance.New(instance.Options{
		Engine: engine.New(8), Registry: reg,
		Limits:      capability.ServerLimits{MaxFSQuotaBytes: 256 << 20, MaxExecTimeout: 0},
		EvalTimeout: 0,
	})
	t.Cleanup(m.StopReaper)
	nc := &atomic.Int64{}
	cl, _ := servingClient(t, m, errors.New("resolve must not be called"), nc)
	ctx := context.Background()

	prep := &runtime.PrepareRequest{
		CreateRequest: runtime.CreateRequest{InstanceID: "rep-c1", Owner: runtime.Owner{UserID: "u1"},
			Spec: servingSpec(), ExpiresAt: time.Now().Add(300e9)},
		ReleaseID: "rel-1", Generation: 1, PrepareID: "p1",
		Program: runtime.ProgramBundle{
			Sources:      map[string][]byte{"main.star": []byte(`def main(i): return {"ok":i["n"]}`)},
			InputSchema:  json.RawMessage(`{}`),
			OutputSchema: json.RawMessage(`{}`),
		},
	}
	pr, err := cl.Prepare(ctx, prep)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if pr == nil || pr.Result.Error != nil {
		t.Fatalf("prepare result: %+v", pr)
	}
	if pr.NodeID != "w1" || pr.Epoch != 1 {
		t.Fatalf("route annotation lost: %+v", pr)
	}
	if nc.Load() != 1 {
		t.Fatalf("nodes called %d times", nc.Load())
	}
	// Invoke through the cached route: Resolve must never run (it errors).
	inv := &runtime.InvokeRequest{InstanceID: "rep-c1", Owner: runtime.Owner{UserID: "u1"},
		Actor: runtime.Owner{UserID: "act-7"}, ReleaseID: "rel-1", Generation: 1, Epoch: 1,
		InvokeID: "i1", Input: json.RawMessage(`{"n":9007199254740993}`)}
	out, err := cl.Invoke(ctx, inv)
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if out == nil || out.Result.Error != nil {
		t.Fatalf("invoke result: %+v", out)
	}
	if !strings.Contains(string(out.Result.Output), "9007199254740993") {
		t.Fatalf("big int lost: %s", out.Result.Output)
	}
	// Fence metadata untouched end to end (any mismatch => stale_epoch).
	bad := *inv
	bad.Epoch = 9
	bad.InvokeID = "i2"
	if _, err := cl.Invoke(ctx, &bad); !errors.Is(err, runtime.ErrStaleEpoch) {
		t.Fatalf("fence: %v", err)
	}
}

func TestClientPrepareFailureNoRoute(t *testing.T) {
	reg := capability.NewRegistry()
	reg.Register(capfs.Factory())
	reg.Register(capio.Factory())
	m := instance.New(instance.Options{Engine: engine.New(8), Registry: reg,
		Limits: capability.ServerLimits{MaxFSQuotaBytes: 256 << 20}})
	t.Cleanup(m.StopReaper)
	resolved := &atomic.Int64{}
	cl, _ := servingClient(t, m, nil, nil)
	// resolve counts calls so we can prove no cached route exists.
	cl.Resolve = func(ctx context.Context, id string) (*placement.Binding, error) {
		resolved.Add(1)
		return nil, errors.New("unbound")
	}
	ctx := context.Background()
	prep := &runtime.PrepareRequest{
		CreateRequest: runtime.CreateRequest{InstanceID: "rep-bad", Owner: runtime.Owner{UserID: "u1"},
			Spec: servingSpec(), ExpiresAt: time.Now().Add(300e9)},
		ReleaseID: "rel-1", Generation: 1, PrepareID: "p1",
		Program: runtime.ProgramBundle{
			Sources: map[string][]byte{"main.star": []byte(`print("top")
def main(i): return {}`)},
			InputSchema:  json.RawMessage(`{}`),
			OutputSchema: json.RawMessage(`{}`),
		},
	}
	pr, err := cl.Prepare(ctx, prep)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Result.Error == nil || pr.Result.Error.Code != "invalid_program" {
		t.Fatalf("expected invalid_program, got %+v", pr.Result.Error)
	}
	if len(pr.Audit.Events) == 0 {
		t.Fatal("failed-prepare audit lost")
	}
	// No route cached: invoke goes through Resolve, which fails.
	if _, err := cl.Invoke(ctx, &runtime.InvokeRequest{InstanceID: "rep-bad", Owner: runtime.Owner{UserID: "u1"},
		ReleaseID: "rel-1", Generation: 1, Epoch: 1, InvokeID: "i1", Input: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("invoke reached a node after failed prepare")
	}
	if resolved.Load() != 1 {
		t.Fatalf("resolve called %d", resolved.Load())
	}
}

func TestClientNoNodesNoRetry(t *testing.T) {
	nc := &atomic.Int64{}
	cl, _ := servingClient(t, nil, nil, nc)
	_, err := cl.Prepare(context.Background(), &runtime.PrepareRequest{
		CreateRequest: runtime.CreateRequest{InstanceID: "x", Owner: runtime.Owner{UserID: "u"},
			Spec: servingSpec(), ExpiresAt: time.Now().Add(60e9)},
		ReleaseID: "r", Generation: 1, PrepareID: "p",
		Program: runtime.ProgramBundle{Sources: map[string][]byte{"main.star": []byte("x")},
			InputSchema: json.RawMessage(`{}`), OutputSchema: json.RawMessage(`{}`)},
	})
	if !errors.Is(err, runtime.ErrTooMany) {
		t.Fatalf("expected ErrTooMany, got %v", err)
	}
	if nc.Load() != 1 {
		t.Fatalf("nodes called %d times (retry)", nc.Load())
	}
}
