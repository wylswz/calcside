package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"calcside/internal/node"
	"calcside/internal/runtime/remote"
	"calcside/internal/store"
	"calcside/internal/types"
)

// TestRegisteredWorkerMembership proves the self-registration path end
// to end at the app seam: with no registered workers instance create
// fails too_many, and a worker that registers through the signed
// callback surface becomes schedulable without any API-side node
// configuration.
func TestRegisteredWorkerMembership(t *testing.T) {
	cfg := testConfig(t)
	nd := node.Build(node.Config{Limits: cfg.ExecLimits()})
	t.Cleanup(nd.Close)
	worker := httptest.NewServer(remote.NewHandler(nd.Manager, []byte(cfg.WorkerKey)))
	t.Cleanup(worker.Close)
	addr := strings.TrimPrefix(worker.URL, "http://")

	app, cleanup, err := initializeApp(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	h := app.Server.Handler

	// Nothing registered yet: placement has no live members.
	request(t, h, "POST", "/api/v1/instances", `{"capabilities":{}}`, http.StatusTooManyRequests)

	registerWorker(t, h, cfg.WorkerKey, "w1", addr)

	created := request(t, h, "POST", "/api/v1/instances", `{"capabilities":{}}`, http.StatusCreated)
	id := created["instance"].(map[string]any)["id"].(string)
	out := request(t, h, "POST", "/api/v1/instances/"+id+"/exec", `{"code":"io.println(7)"}`, http.StatusOK)
	if out["output"] != "7\n" {
		t.Fatalf("exec through registered worker: %v", out)
	}
}

// TestWorkerAdditionKeepsNotebookPinned is the multi-API pinning
// regression: two independent app handlers share one store, w2 joins
// after a notebook exists, new placements reach w2, and the original
// notebook keeps reading its w1-local state identically through the
// warm handler (route-cache hit) and the cold handler (binding resolved
// from the DB) — the stored binding never moves.
func TestWorkerAdditionKeepsNotebookPinned(t *testing.T) {
	cfg := testConfig(t)
	st, err := store.Open(context.Background(), types.DriverSQLite, cfg.DSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	newWorker := func(t *testing.T) string {
		t.Helper()
		nd := node.Build(node.Config{Limits: cfg.ExecLimits()})
		t.Cleanup(nd.Close)
		srv := httptest.NewServer(remote.NewHandler(nd.Manager, []byte(cfg.WorkerKey)))
		t.Cleanup(srv.Close)
		return strings.TrimPrefix(srv.URL, "http://")
	}
	w1addr := newWorker(t)

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
	hA, hB := appA.Server.Handler, appB.Server.Handler

	binding := func(t *testing.T, id string) (string, int64) {
		t.Helper()
		in, err := st.GetInstance(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		return in.NodeID, in.LeaseEpoch
	}

	// Only w1 exists: the notebook must bind to it.
	registerWorker(t, hA, cfg.WorkerKey, "w1", w1addr)
	created := request(t, hA, "POST", "/api/v1/instances", `{"capabilities":{"fs":{}}}`, http.StatusCreated)
	id := created["instance"].(map[string]any)["id"].(string)
	request(t, hA, "POST", "/api/v1/instances/"+id+"/exec",
		`{"code":"x = 6 * 7\nfs.write(\"marker.txt\", \"pinned-state\")"}`, http.StatusOK)
	node1, epoch1 := binding(t, id)
	if node1 != "w1" {
		t.Fatalf("notebook bound to %q, want w1", node1)
	}

	// w2 joins through the OTHER API replica — shared DB membership,
	// no API config change anywhere.
	w2addr := newWorker(t)
	registerWorker(t, hB, cfg.WorkerKey, "w2", w2addr)

	// New instances spread over the live set: both workers must appear
	// regardless of round-robin order.
	seen := map[string]bool{}
	for i := 0; i < 4 && !(seen["w1"] && seen["w2"]); i++ {
		c := request(t, hB, "POST", "/api/v1/instances", `{"capabilities":{}}`, http.StatusCreated)
		nid := c["instance"].(map[string]any)["id"].(string)
		n, _ := binding(t, nid)
		seen[n] = true
	}
	if !seen["w1"] || !seen["w2"] {
		t.Fatalf("new placements did not reach both workers: %v", seen)
	}

	// The stored binding is untouched by the join.
	node2, epoch2 := binding(t, id)
	if node2 != node1 || epoch2 != epoch1 {
		t.Fatalf("binding moved: %s@%d -> %s@%d", node1, epoch1, node2, epoch2)
	}

	readState := `{"code":"print(x)\nprint(fs.read(\"marker.txt\"))"}`
	want := "42\npinned-state\n"
	// Warm API (route-cache hit) and cold API (fresh binding resolve)
	// must return identical w1-local state — a re-route to w2 would
	// fail outright since w2 never ran this instance.
	for name, h := range map[string]http.Handler{"warm": hA, "cold": hB} {
		out := request(t, h, "POST", "/api/v1/instances/"+id+"/exec", readState, http.StatusOK)
		if out["output"] != want {
			t.Fatalf("%s exec: %v", name, out)
		}
	}
}
