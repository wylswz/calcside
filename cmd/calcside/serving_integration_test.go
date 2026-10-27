package main

// End-to-end persistent-instance restore proof: real SQLite store,
// signed callback HTTP, nodeagent and in-process/subprocess runtimes.
// Covers API restart while the worker stays alive, and worker restart
// with a new boot reclaiming after the old lease expires.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"calcside/cmd/calcside/internal/extensions"
	"calcside/cmd/calcside/internal/service/serving"
	"calcside/internal/node"
	"calcside/internal/node/subproc"
	"calcside/internal/nodeagent"
	"calcside/internal/runtime"
	"calcside/internal/runtime/remote"
	"calcside/internal/store"
	"calcside/internal/types"
)

var cbKey = []byte("0123456789abcdef0123456789abcdef")

// testControl is a minimal HTTP NodeControl over the generated callback
// surface — the production worker client is already HMAC-tested; this
// exercises the API side end to end.
type testControl struct {
	base   string
	hc     *http.Client
	nodeID string
}

func (c *testControl) post(ctx context.Context, path string, in, out any, max int64) error {
	body, _ := json.Marshal(in)
	req, err := http.NewRequestWithContext(ctx, "POST", c.base+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	remote.SignRequest(cbKey, c.nodeID, req, body, time.Now())
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > max {
		return errors.New("oversize")
	}
	if resp.StatusCode == http.StatusOK {
		return json.Unmarshal(data, out)
	}
	var env struct {
		Error struct {
			Kind string `json:"kind"`
		} `json:"error"`
	}
	json.Unmarshal(data, &env)
	switch {
	case resp.StatusCode == http.StatusConflict && env.Error.Kind == "stale_epoch":
		return runtime.ErrStaleEpoch
	case resp.StatusCode == http.StatusForbidden:
		return runtime.ErrNotOwner
	case resp.StatusCode == http.StatusBadRequest:
		return runtime.ErrBadSpec
	case resp.StatusCode == http.StatusNotFound:
		return runtime.ErrNotFound
	default:
		return fmt.Errorf("http %d", resp.StatusCode)
	}
}

func (c *testControl) RegisterNode(ctx context.Context, r *runtime.NodeRegisterRequest) (*runtime.NodeRegisterResponse, error) {
	var out runtime.NodeRegisterResponse
	err := c.post(ctx, "/internal/v1/nodes/register", r, &out, 1<<20)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *testControl) SyncNode(ctx context.Context, r *runtime.NodeSyncRequest) (*runtime.NodeSyncResponse, error) {
	var out runtime.NodeSyncResponse
	err := c.post(ctx, "/internal/v1/nodes/sync", r, &out, 1<<20)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *testControl) PrepareReplica(ctx context.Context, r *runtime.NodePrepareRequest) (*runtime.NodePrepareResponse, error) {
	var out runtime.NodePrepareResponse
	err := c.post(ctx, "/internal/v1/nodes/prepare", r, &out, 16<<20)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// swapHandler lets tests atomically replace the API handler (simulated
// restart) while draining in-flight requests.
type swapHandler struct {
	mu sync.RWMutex
	h  http.Handler
}

func (s *swapHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	s.h.ServeHTTP(w, r)
}
func (s *swapHandler) set(h http.Handler) { s.mu.Lock(); s.h = h; s.mu.Unlock() }

var api503 = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusServiceUnavailable)
})

func seedServing(t *testing.T, st store.Store) (*store.User, *store.PersistentInstance) {
	t.Helper()
	u, err := st.UpsertUserByEmail(context.Background(), "pin@x.com", "Pin", "sub-pin")
	if err != nil {
		t.Fatal(err)
	}
	prog := runtime.ProgramBundle{
		Sources: map[string][]byte{"main.star": []byte(`counter = [0]
def main(input):
    counter[0] += 1
    seed = fs.read("seed.txt")
    seen = fs.exists("scratch.txt")
    fs.write("scratch.txt", "x")
    return {"n": counter[0], "seed": seed, "scratch_seen": seen}`)},
		Assets:       map[string][]byte{"seed.txt": []byte("seed")},
		InputSchema:  json.RawMessage(`{"type":"object"}`),
		OutputSchema: json.RawMessage(`{"type":"object","required":["n","seed","scratch_seen"]}`),
	}
	rel, err := store.NewServingRelease("ws_1", u.ID,
		json.RawMessage(`{"ttl_seconds":300,"capabilities":{"fs":{}}}`),
		runtime.PolicyBundle{Global: map[string]string{}, User: map[string]string{}},
		prog, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateServingRelease(context.Background(), rel); err != nil {
		t.Fatal(err)
	}
	inst := &store.PersistentInstance{
		WorkspaceID: "ws_1", UserID: u.ID, ReleaseID: rel.ID,
		Enabled: true, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := st.CreatePersistentInstance(context.Background(), inst); err != nil {
		t.Fatal(err)
	}
	return u, inst
}

func waitFor(t *testing.T, d time.Duration, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func invokeN(t *testing.T, rt runtime.Runtime, rep *store.ServingReplica, u *store.User) map[string]any {
	out, err := rt.Invoke(context.Background(), &runtime.InvokeRequest{
		InstanceID: rep.ID, Owner: runtime.Owner{UserID: u.ID},
		ReleaseID: rep.ReleaseID, Generation: rep.Generation,
		Epoch: rep.Epoch, InvokeID: store.NewID("inv_"), Input: []byte(`{}`),
	})
	if err != nil || out == nil {
		t.Fatalf("invoke: %v", err)
	}
	if out.Result.Error != nil {
		t.Fatalf("invoke program error: %+v", out.Result.Error)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Result.Output, &got); err != nil {
		t.Fatalf("output decode: %v", err)
	}
	return got
}

func TestServingRestart(t *testing.T) {
	for _, mode := range []string{"inproc", "process"} {
		t.Run(mode, func(t *testing.T) {
			lease, ttl := time.Second, 3*time.Second
			if mode == "process" {
				lease, ttl = 5*time.Second, 10*time.Second
			}
			cfg := testConfig(t)
			st, err := store.Open(context.Background(), types.DriverSQLite, cfg.DSN)
			if err != nil {
				t.Fatal(err)
			}
			u, inst := seedServing(t, st)

			newSvc := func(st store.Store) *serving.Service {
				svc, err := serving.New(serving.Options{
					Store: st, Nodes: map[string]string{"worker-test": "configured-worker"},
					LeaseDuration: lease, NodeTTL: ttl,
					Now: func() time.Time { return time.Now().Add(2 * time.Hour) },
				})
				if err != nil {
					t.Fatal(err)
				}
				return svc
			}
			svc := newSvc(st)
			swap := &swapHandler{h: extensions.CallbackHandler(nil, cbKey, svc)}
			apiSrv := httptest.NewServer(swap)
			t.Cleanup(apiSrv.Close)

			newRT := func() (runtime.Runtime, func()) {
				if mode == "process" {
					sup, err := subproc.New(subproc.Options{
						Child: subproc.ChildConfig{Node: node.Config{Limits: cfg.ExecLimits()}},
					})
					if err != nil {
						t.Fatal(err)
					}
					return sup, sup.Close
				}
				nd := node.Build(node.Config{Limits: cfg.ExecLimits()})
				return nd.Manager, nd.Close
			}
			rt, closeRT := newRT()
			var rtOnce sync.Once
			t.Cleanup(func() { rtOnce.Do(closeRT) })
			t.Cleanup(func() {
				if st != nil {
					st.Close()
				}
			})
			ctl := &testControl{base: apiSrv.URL, hc: apiSrv.Client(), nodeID: "worker-test"}
			agent, err := nodeagent.New(nodeagent.Options{
				Control: ctl, Runtime: rt, NodeID: "worker-test", BootID: "boot1",
				Capacity: 2, PollInterval: 20 * time.Millisecond, CallTimeout: 2 * time.Second,
			})
			if err != nil {
				t.Fatal(err)
			}
			agentCtx, agentStop := context.WithCancel(context.Background())
			agentDone := make(chan struct{})
			go func() { defer close(agentDone); agent.Run(agentCtx) }()
			t.Cleanup(func() { agentStop(); <-agentDone })

			budget := 5 * time.Second
			if mode == "process" {
				budget = 15 * time.Second
			}
			// Claim + prepare lands the first ready replica.
			var rep *store.ServingReplica
			waitFor(t, budget, "ready replica", func() bool {
				got, _ := st.GetPersistentInstance(context.Background(), "ws_1", inst.ID)
				if got.ActiveReplicaID == "" {
					return false
				}
				rep, _ = st.GetServingReplica(context.Background(), got.ActiveReplicaID)
				return rep != nil && rep.State == store.ServingReplicaReady
			})
			if rep.NodeID != "worker-test" || rep.BootID != "boot1" {
				t.Fatalf("fence: %+v", rep)
			}
			// Fresh sessions: both invokes see n=1, seed, no scratch.
			for i := 0; i < 2; i++ {
				got := invokeN(t, rt, rep, u)
				if got["n"] != float64(1) || got["seed"] != "seed" || got["scratch_seen"] != false {
					t.Fatalf("invoke %d: %v", i, got)
				}
			}

			// --- API restart, worker stays alive ---
			preRestart, _ := st.GetServingReplica(context.Background(), rep.ID)
			preLease := preRestart.LeaseUntil
			swap.set(api503)
			st.Close()
			st, err = store.Open(context.Background(), types.DriverSQLite, cfg.DSN)
			if err != nil {
				t.Fatal(err)
			}
			svc = newSvc(st)
			swap.set(extensions.CallbackHandler(nil, cbKey, svc))
			// The same boot keeps renewing through the restored API:
			// the lease must actually move forward, same id/epoch.
			waitFor(t, budget, "renewal after api restart", func() bool {
				cur2, err := st.GetServingReplica(context.Background(), rep.ID)
				return err == nil && cur2.LeaseUntil.After(preLease)
			})
			cur, err := st.GetCurrentServingReplica(context.Background(), inst.ID)
			if err != nil || cur.ID != rep.ID || cur.Epoch != rep.Epoch {
				t.Fatalf("api restart lost replica: %+v", cur)
			}
			got2 := invokeN(t, rt, cur, u)
			if got2["n"] != float64(1) || got2["scratch_seen"] != false {
				t.Fatalf("post-restart invoke: %v", got2)
			}

			// --- Worker restart: new boot, old lease preserved ---
			// Take the API dark first so no heartbeat can renew after
			// this point; then capture the lease the old boot left.
			swap.set(api503)
			agentStop()
			<-agentDone
			rep2, err := st.GetServingReplica(context.Background(), cur.ID)
			if err != nil {
				t.Fatal(err)
			}
			oldLease := rep2.LeaseUntil
			rtOnce.Do(closeRT)

			ctl2 := &testControl{base: apiSrv.URL, hc: apiSrv.Client(), nodeID: "worker-test"}
			rt2, closeRT2 := newRT()
			var rt2Once sync.Once
			t.Cleanup(func() { rt2Once.Do(closeRT2) })
			swap.set(extensions.CallbackHandler(nil, cbKey, svc))
			agent2, err := nodeagent.New(nodeagent.Options{
				Control: ctl2, Runtime: rt2, NodeID: "worker-test", BootID: "boot2",
				Capacity: 2, PollInterval: 20 * time.Millisecond, CallTimeout: 2 * time.Second,
			})
			if err != nil {
				t.Fatal(err)
			}
			agentCtx2, agentStop2 := context.WithCancel(context.Background())
			agentDone2 := make(chan struct{})
			go func() { defer close(agentDone2); agent2.Run(agentCtx2) }()
			t.Cleanup(func() { agentStop2(); <-agentDone2 })

			// New boot registers as incarnation 2.
			waitFor(t, budget, "incarnation 2", func() bool {
				nodes, _ := st.ListServingNodes(context.Background(), time.Now().Add(2*time.Hour))
				for _, n := range nodes {
					if n.NodeID == "worker-test" && n.BootID == "boot2" && n.Incarnation == 2 {
						return true
					}
				}
				return false
			})
			// boot1 is now the stale historical boot — re-registering
			// it is rejected forever, and the current node stays boot2.
			_, rerr := ctl2.RegisterNode(context.Background(), &runtime.NodeRegisterRequest{
				NodeID: "worker-test", BootID: "boot1", Capacity: 2})
			if !errors.Is(rerr, runtime.ErrStaleEpoch) {
				t.Fatalf("old boot re-register: %v", rerr)
			}
			// While the old lease is still alive the replica row is
			// untouched: same id, unchanged expiry.
			if oldLease.After(time.Now().Add(2 * time.Hour)) {
				rep3, err := st.GetServingReplica(context.Background(), rep.ID)
				if err != nil || rep3.LeaseUntil != oldLease {
					t.Fatalf("lease touched: %v %+v", err, rep3)
				}
				cur2, err := st.GetCurrentServingReplica(context.Background(), inst.ID)
				if err != nil || cur2.ID != rep.ID {
					t.Fatalf("early claim: %+v", cur2)
				}
			}
			// After the old lease expires the new boot claims the next
			// epoch with a fresh replica id.
			var rep4 *store.ServingReplica
			waitFor(t, budget+lease+3*time.Second, "new epoch replica", func() bool {
				got, _ := st.GetPersistentInstance(context.Background(), "ws_1", inst.ID)
				if got.ActiveReplicaID == "" || got.ActiveReplicaID == rep.ID {
					return false
				}
				rep4, _ = st.GetServingReplica(context.Background(), got.ActiveReplicaID)
				return rep4 != nil && rep4.State == store.ServingReplicaReady &&
					rep4.BootID == "boot2" && rep4.Epoch == rep.Epoch+1 &&
					rep4.Generation == rep.Generation
			})
			out := invokeN(t, rt2, rep4, u)
			if out["n"] != float64(1) || out["seed"] != "seed" ||
				out["scratch_seen"] != false || rep4.WorkspaceID != inst.WorkspaceID {
				t.Fatalf("post-restart invoke: %v", out)
			}
			agentStop2()
			<-agentDone2
			rt2Once.Do(closeRT2)
		})
	}
}
