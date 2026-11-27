package main

// Persistent-application replica pool, end to end: real SQLite store,
// two (then three) worker processes behind the signed remote protocol,
// real nodeagents syncing through the API control surface, and real
// application publish + invoke over HTTP. Proves the pool prepares one
// replica per live worker, invokes fan out across them concurrently,
// a third worker joins without disturbing existing replicas, and a
// release update invalidates the whole old pool.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"calcside/internal/node"
	"calcside/internal/nodeagent"
	"calcside/internal/runtime"
	"calcside/internal/runtime/remote"
	"calcside/internal/runtime/subproc"
	"calcside/internal/store"
	"calcside/internal/types"
)

// poolControl is the nodeagent control client bound to the app's own
// shared worker key — unlike testControl it signs with cfg.WorkerKey.
type poolControl struct {
	base   string
	hc     *http.Client
	nodeID string
	key    []byte
}

func (c *poolControl) post(ctx context.Context, path string, in, out any, max int64) error {
	body, _ := json.Marshal(in)
	req, err := http.NewRequestWithContext(ctx, "POST", c.base+path, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	remote.SignRequest(c.key, c.nodeID, req, body, time.Now())
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
		return fmt.Errorf("oversize")
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

func (c *poolControl) RegisterNode(ctx context.Context, r *runtime.NodeRegisterRequest) (*runtime.NodeRegisterResponse, error) {
	var out runtime.NodeRegisterResponse
	if err := c.post(ctx, "/internal/v1/nodes/register", r, &out, 1<<20); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *poolControl) SyncNode(ctx context.Context, r *runtime.NodeSyncRequest) (*runtime.NodeSyncResponse, error) {
	var out runtime.NodeSyncResponse
	if err := c.post(ctx, "/internal/v1/nodes/sync", r, &out, 1<<20); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *poolControl) PrepareReplica(ctx context.Context, r *runtime.NodePrepareRequest) (*runtime.NodePrepareResponse, error) {
	var out runtime.NodePrepareResponse
	if err := c.post(ctx, "/internal/v1/nodes/prepare", r, &out, 16<<20); err != nil {
		return nil, err
	}
	return &out, nil
}

type poolWorker struct {
	hits *atomic.Int64
}

func TestPersistentPoolScalesAcrossWorkers(t *testing.T) {
	cfg := testConfig(t)
	st, err := store.Open(context.Background(), types.DriverSQLite, cfg.DSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	appA, cleanupA, err := initializeApp(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanupA)
	appB, cleanupB, err := initializeApp(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanupB)
	// The nodeagent control plane is the app's own signed surface.
	apiSrv := httptest.NewServer(appA.Server.Handler)
	t.Cleanup(apiSrv.Close)

	// Deterministic concurrency barrier: the app's net.get blocks until
	// TWO requests are inside the stub simultaneously, then releases.
	// A single-replica pool serializes the invokes on one worker, so
	// in-flight never reaches 2 — maxSeen is the discriminator.
	var inFlight, maxSeen atomic.Int32
	release := make(chan struct{})
	var once sync.Once
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := inFlight.Add(1)
		for {
			m := maxSeen.Load()
			if n <= m || maxSeen.CompareAndSwap(m, n) {
				break
			}
		}
		defer inFlight.Add(-1)
		if n == 2 {
			once.Do(func() { close(release) })
		}
		select {
		case <-release:
		case <-time.After(20 * time.Second):
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(stub.Close)
	stubAuthority := strings.TrimPrefix(stub.URL, "http://")

	newWorker := func(name string) *poolWorker {
		sup, err := subproc.New(subproc.Options{
			Child: subproc.ChildConfig{Node: node.Config{Limits: cfg.ExecLimits()}},
		})
		if err != nil {
			t.Fatal(err)
		}
		var hits atomic.Int64
		h := remote.NewHandler(sup, []byte(cfg.WorkerKey))
		wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "/runtime/v1/invoke") {
				hits.Add(1)
			}
			h.ServeHTTP(w, r)
		})
		srv := httptest.NewServer(wrapped)
		ctl := &poolControl{base: apiSrv.URL, hc: apiSrv.Client(), nodeID: name, key: []byte(cfg.WorkerKey)}
		agent, err := nodeagent.New(nodeagent.Options{
			Control: ctl, Runtime: sup, NodeID: name, BootID: "boot-" + name,
			AdvertiseAddr: strings.TrimPrefix(srv.URL, "http://"),
			Capacity:      8, PollInterval: 20 * time.Millisecond, CallTimeout: 10 * time.Second,
		})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { defer close(done); agent.Run(ctx) }()
		w := &poolWorker{hits: &hits}
		t.Cleanup(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("worker agent did not stop")
			}
			srv.Close()
			sup.Close()
		})
		return w
	}

	w1 := newWorker("pw1")
	w2 := newWorker("pw2")

	// Publish's prepare picks a live node — wait until both workers
	// have registered and heartbeated, or the pick sees an empty set.
	waitFor(t, 30*time.Second, "two registered workers", func() bool {
		nodes, err := st.ListServingNodes(context.Background(), time.Now())
		return err == nil && len(nodes) == 2
	})

	// Publish a persistent app whose main() hits the barrier stub.
	code := `def main(input):
    r = net.get(input["url"])
    return {"status": r["status"]}`
	spec := fmt.Sprintf(`{"capabilities":{"net":{"allow_hosts":[%q],"timeout_ms":30000}}}`, stubAuthority)
	created := request(t, appA.Server.Handler, "POST", "/api/v1/applications",
		fmt.Sprintf(`{"name":"pool","sections":[{"id":"main","code":%q}],"spec":%s}`, code, spec), http.StatusOK)
	app := created["application"].(map[string]any)
	appID := app["id"].(string)

	preview := request(t, appA.Server.Handler, "POST",
		"/api/v1/applications/"+appID+"/publish/preview", `{"version":1}`, http.StatusOK)
	candidate := preview["candidate"].(map[string]any)["digest"].(string)
	request(t, appA.Server.Handler, "POST", "/api/v1/applications/"+appID+"/publish",
		fmt.Sprintf(`{"version":1,"expected_base":"","candidate":%q}`, candidate), http.StatusAccepted)

	appRow, err := st.GetApplication(context.Background(), appID)
	if err != nil || appRow.PersistentInstanceID == "" {
		t.Fatalf("published app: %+v %v", appRow, err)
	}
	pinID := appRow.PersistentInstanceID

	readyPool := func(want int, gen int64) bool {
		rows, err := st.ListInstanceServingReplicas(context.Background(), pinID, time.Now())
		if err != nil {
			return false
		}
		n := 0
		for _, r := range rows {
			if r.Generation == gen && r.State == store.ServingReplicaReady && r.LeaseUntil.After(time.Now()) {
				n++
			}
		}
		return n == want
	}
	pin, _ := st.GetPersistentInstance(context.Background(), appRow.WorkspaceID, pinID)
	waitFor(t, 90*time.Second, "two ready replicas", func() bool { return readyPool(2, pin.Generation) })

	invoke := func(h http.Handler, body string) (int, string) {
		req := httptest.NewRequest("POST", "/api/v1/applications/"+appID+"/invoke", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Requested-With", "calcside")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}
	invokeBody := fmt.Sprintf(`{"url":%q}`, stub.URL+"/go")

	// Two concurrent invokes must overlap inside the stub: round-robin
	// lands them on different workers, the barrier releases only when
	// both are inside. Bounded by the invoke HTTP timeout below.
	type res struct {
		code int
		body string
		err  error
	}
	results := make(chan res, 2)
	for i := 0; i < 2; i++ {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, "POST",
				apiSrv.URL+"/api/v1/applications/"+appID+"/invoke", strings.NewReader(invokeBody))
			if err != nil {
				results <- res{err: err}
				return
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Requested-With", "calcside")
			resp, err := apiSrv.Client().Do(req)
			if err != nil {
				results <- res{err: err}
				return
			}
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			results <- res{code: resp.StatusCode, body: string(b)}
		}()
	}
	for i := 0; i < 2; i++ {
		r := <-results
		if r.err != nil || r.code != http.StatusOK {
			t.Fatalf("concurrent invoke: %v %d %s", r.err, r.code, r.body)
		}
	}
	if maxSeen.Load() != 2 {
		t.Fatalf("stub max concurrency %d, want 2 — invokes did not overlap", maxSeen.Load())
	}

	// Sequential invokes spread across the pool: both workers serve —
	// snapshot first so the earlier concurrent pair cannot satisfy this.
	h1, h2 := w1.hits.Load(), w2.hits.Load()
	for i := 0; i < 6; i++ {
		if code, body := invoke(appA.Server.Handler, invokeBody); code != http.StatusOK {
			t.Fatalf("invoke %d: %d %s", i, code, body)
		}
	}
	if w1.hits.Load() <= h1 || w2.hits.Load() <= h2 {
		t.Fatalf("sequential invokes did not reach both workers: w1 %d->%d w2 %d->%d",
			h1, w1.hits.Load(), h2, w2.hits.Load())
	}

	// API-B shares the store: it selects from the same pool with no
	// local pointer state.
	if code, body := invoke(appB.Server.Handler, invokeBody); code != http.StatusOK {
		t.Fatalf("api-b invoke: %d %s", code, body)
	}

	// Existing replica ids survive a third worker joining.
	before := map[string]bool{}
	for _, r := range func() []*store.ServingReplica {
		rows, _ := st.ListInstanceServingReplicas(context.Background(), pinID, time.Now())
		return rows
	}() {
		before[r.ID] = true
	}
	w3 := newWorker("pw3")
	waitFor(t, 30*time.Second, "three registered workers", func() bool {
		nodes, err := st.ListServingNodes(context.Background(), time.Now())
		return err == nil && len(nodes) == 3
	})
	waitFor(t, 90*time.Second, "three ready replicas", func() bool { return readyPool(3, pin.Generation) })
	rows, _ := st.ListInstanceServingReplicas(context.Background(), pinID, time.Now())
	for _, r := range rows {
		if r.Generation == pin.Generation && !before[r.ID] && r.NodeID != "pw3" {
			t.Fatalf("existing replica replaced: %+v", r)
		}
	}
	for i := 0; i < 9; i++ {
		if code, body := invoke(appA.Server.Handler, invokeBody); code != http.StatusOK {
			t.Fatalf("post-join invoke %d: %d %s", i, code, body)
		}
	}
	if w3.hits.Load() == 0 {
		t.Fatal("third worker received no invokes")
	}

	// A release update bumps the generation: the entire old pool stops
	// being eligible and a fresh pool forms on the new release.
	code2 := `def main(input):
    r = net.get(input["url"])
    return {"status": r["status"], "v": 2}`
	upd := request(t, appA.Server.Handler, "PUT", "/api/v1/applications/"+appID,
		fmt.Sprintf(`{"version":1,"name":"pool","sections":[{"id":"main","code":%q}],"spec":%s}`, code2, spec), http.StatusOK)
	newVer := upd["application"].(map[string]any)["version"].(float64)
	prev := request(t, appA.Server.Handler, "POST",
		fmt.Sprintf("/api/v1/applications/%s/publish/preview", appID),
		fmt.Sprintf(`{"version":%d}`, int64(newVer)), http.StatusOK)
	cand2 := prev["candidate"].(map[string]any)["digest"].(string)
	request(t, appA.Server.Handler, "POST", "/api/v1/applications/"+appID+"/publish",
		fmt.Sprintf(`{"version":%d,"expected_base":%q,"candidate":%q}`, int64(newVer), candidate, cand2), http.StatusAccepted)
	pin2, _ := st.GetPersistentInstance(context.Background(), appRow.WorkspaceID, pinID)
	if pin2.Generation == pin.Generation {
		t.Fatal("publish did not advance generation")
	}
	waitFor(t, 90*time.Second, "new-generation pool", func() bool { return readyPool(3, pin2.Generation) })
	// The new release actually serves: decode output_json and require
	// the v2 marker — an old-pool response cannot satisfy this.
	status, resp := invoke(appA.Server.Handler, invokeBody)
	if status != http.StatusOK {
		t.Fatalf("post-update invoke: %d %s", status, resp)
	}
	var run struct {
		OutputJSON string `json:"output_json"`
	}
	if err := json.Unmarshal([]byte(resp), &run); err != nil {
		t.Fatalf("invoke decode: %v %s", err, resp)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(run.OutputJSON), &out); err != nil || out["v"] != float64(2) {
		t.Fatalf("post-update output: %v %s", err, run.OutputJSON)
	}

	// Disable the pin: invokes through BOTH API handlers must fail
	// not_running — no handler may keep serving the dead pool.
	if ok, err := st.SetPersistentEnabled(context.Background(), appRow.WorkspaceID, pinID, pin2.Generation, false, time.Now()); !ok || err != nil {
		t.Fatalf("disable pin: %v %v", ok, err)
	}
	for name, h := range map[string]http.Handler{"api-a": appA.Server.Handler, "api-b": appB.Server.Handler} {
		if c, b := invoke(h, invokeBody); c != http.StatusServiceUnavailable {
			t.Fatalf("%s invoke after disable: %d %s", name, c, b)
		}
	}
}
