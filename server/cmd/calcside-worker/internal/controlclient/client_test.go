package controlclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"calcside/internal/runtime"
	"calcside/internal/runtime/remote"
	"calcside/internal/types"
)

var key = []byte("0123456789abcdef0123456789abcdef")

// fakeAPI verifies the HMAC on every request and records bodies.
type fakeAPI struct {
	calls   int
	bodies  []string
	handler func(w http.ResponseWriter, body []byte)
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.calls++
	body, _ := io.ReadAll(r.Body)
	f.bodies = append(f.bodies, string(body))
	if err := remote.VerifyRequest(key, r, body, time.Now()); err != nil {
		http.Error(w, "bad sig", http.StatusUnauthorized)
		return
	}
	f.handler(w, body)
}

func newClient(t *testing.T, f *fakeAPI) *Client {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, "n1", key, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRegisterSyncPrepare(t *testing.T) {
	f := &fakeAPI{}
	f.handler = func(w http.ResponseWriter, body []byte) {
		json.NewEncoder(w).Encode(runtime.NodeRegisterResponse{
			Node:      runtime.NodeIdentity{NodeID: "n1", BootID: "b1", Incarnation: 7},
			ServerNow: time.Unix(1000, 0).UTC(),
		})
	}
	c := newClient(t, f)
	resp, err := c.RegisterNode(context.Background(), &runtime.NodeRegisterRequest{
		NodeID: "n1", BootID: "b1", Capacity: 4})
	if err != nil || resp.Node.Incarnation != 7 {
		t.Fatalf("register: %v %+v", err, resp)
	}
	if !strings.Contains(f.bodies[0], `"node_id":"n1"`) {
		t.Fatalf("body: %s", f.bodies[0])
	}

	f.handler = func(w http.ResponseWriter, body []byte) {
		json.NewEncoder(w).Encode(runtime.NodeSyncResponse{
			Assignments: []runtime.ReplicaAssignment{{
				ReplicaID: "rep_1", InstanceID: "pin_1", ReleaseID: "rel_1",
				Generation: 2, Epoch: 3, ExpiresAt: time.Unix(2000, 0).UTC(),
			}},
			ServerNow: time.Unix(1000, 0).UTC(),
		})
	}
	s, err := c.SyncNode(context.Background(), &runtime.NodeSyncRequest{
		Node:     runtime.NodeIdentity{NodeID: "n1", BootID: "b1", Incarnation: 7},
		Capacity: 4,
	})
	if err != nil || len(s.Assignments) != 1 || s.Assignments[0].Epoch != 3 {
		t.Fatalf("sync: %v %+v", err, s)
	}
	if !strings.Contains(f.bodies[1], `"incarnation":7`) {
		t.Fatalf("sync body: %s", f.bodies[1])
	}
}

func TestStaleAndErrors(t *testing.T) {
	f := &fakeAPI{}
	f.handler = func(w http.ResponseWriter, body []byte) {
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]string{"kind": "stale_epoch", "message": "secret-marker"}})
	}
	c := newClient(t, f)
	_, err := c.SyncNode(context.Background(), &runtime.NodeSyncRequest{
		Node:     runtime.NodeIdentity{NodeID: "n1", BootID: "old", Incarnation: 1},
		Capacity: 4,
	})
	if !errors.Is(err, runtime.ErrStaleEpoch) {
		t.Fatalf("stale: %v", err)
	}
	if f.calls != 1 {
		t.Fatalf("retried: %d calls", f.calls)
	}
	// Secret-marker text is never echoed.
	if strings.Contains(err.Error(), "secret-marker") {
		t.Fatalf("echoed body: %v", err)
	}

	// Network/500 failure: one attempt, generic error.
	f.handler = func(w http.ResponseWriter, body []byte) {
		http.Error(w, "secret-marker", http.StatusInternalServerError)
	}
	_, err = c.SyncNode(context.Background(), &runtime.NodeSyncRequest{
		Node:     runtime.NodeIdentity{NodeID: "n1", BootID: "b", Incarnation: 1},
		Capacity: 4,
	})
	if err == nil || errors.Is(err, runtime.ErrStaleEpoch) {
		t.Fatalf("500: %v", err)
	}
	if strings.Contains(err.Error(), "secret-marker") {
		t.Fatalf("echoed: %v", err)
	}
}

func TestPrepareOversize(t *testing.T) {
	f := &fakeAPI{}
	f.handler = func(w http.ResponseWriter, body []byte) {
		w.Write(make([]byte, 17<<20)) // > 16 MiB bound
	}
	c := newClient(t, f)
	_, err := c.PrepareReplica(context.Background(), &runtime.NodePrepareRequest{
		Node: runtime.NodeIdentity{NodeID: "n1", BootID: "b1", Incarnation: 1},
		Assignment: runtime.ReplicaAssignment{
			ReplicaID: "rep_1", InstanceID: "pin_1", ReleaseID: "rel_1",
			Generation: 1, Epoch: 1,
		},
	})
	if err == nil {
		t.Fatal("oversize accepted")
	}
	if f.calls != 1 {
		t.Fatalf("retried: %d", f.calls)
	}
}

func TestBadURL(t *testing.T) {
	for _, u := range []string{"", "ftp://h", "http://", "http://u:p@h", "http://h/?q=1"} {
		if _, err := New(u, "n1", key, nil); err == nil {
			t.Fatalf("accepted %q", u)
		}
	}
}

func TestPrepareSuccess(t *testing.T) {
	f := &fakeAPI{}
	f.handler = func(w http.ResponseWriter, body []byte) {
		json.NewEncoder(w).Encode(runtime.NodePrepareResponse{
			ServerNow: time.Unix(1000, 0).UTC(),
			Request: runtime.PrepareRequest{
				CreateRequest: runtime.CreateRequest{
					InstanceID: "rep_1",
					Owner:      runtime.Owner{UserID: "u1"},
					ExpiresAt:  time.Unix(2000, 0).UTC(),
					Epoch:      3,
					Secrets:    []runtime.Secret{{Name: "T", Value: "v", Source: types.SecretVault}},
					Policies:   runtime.PolicyBundle{Global: map[string]string{"g": "p"}},
				},
				ReleaseID:  "rel_1",
				Generation: 2,
				PrepareID:  "prp_1",
				Program: runtime.ProgramBundle{
					Sources:     map[string][]byte{"main.star": []byte("s")},
					Assets:      map[string][]byte{"a.bin": {0, 1, 2, 255}},
					InputSchema: []byte(`{"type":"object"}`),
				},
			},
		})
	}
	c := newClient(t, f)
	resp, err := c.PrepareReplica(context.Background(), &runtime.NodePrepareRequest{
		Node: runtime.NodeIdentity{NodeID: "n1", BootID: "b1", Incarnation: 1},
		Assignment: runtime.ReplicaAssignment{
			ReplicaID: "rep_1", InstanceID: "pin_1", ReleaseID: "rel_1",
			Generation: 2, Epoch: 3,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := resp.Request
	if r.InstanceID != "rep_1" || r.ReleaseID != "rel_1" || r.Generation != 2 ||
		r.Epoch != 3 || r.PrepareID != "prp_1" ||
		string(r.Program.Assets["a.bin"]) != string([]byte{0, 1, 2, 255}) ||
		r.Secrets[0].Value != "v" || r.Policies.Global["g"] != "p" ||
		r.Program.InputSchema == nil {
		t.Fatalf("roundtrip: %+v", r)
	}
	// Body carried the full fence.
	if !strings.Contains(f.bodies[0], `"generation":2`) ||
		!strings.Contains(f.bodies[0], `"epoch":3`) {
		t.Fatalf("body: %s", f.bodies[0])
	}
}

func TestNullSuccessAndKindMismatch(t *testing.T) {
	f := &fakeAPI{}
	c := newClient(t, f)
	// JSON null on 200 is not an authoritative empty response.
	f.handler = func(w http.ResponseWriter, body []byte) {
		w.Write([]byte("null"))
	}
	if _, err := c.SyncNode(context.Background(), &runtime.NodeSyncRequest{
		Node:     runtime.NodeIdentity{NodeID: "n1", BootID: "b", Incarnation: 1},
		Capacity: 4,
	}); err == nil {
		t.Fatal("null 200 accepted")
	}
	// A 500 wearing a stale kind is transport, not a fence failure.
	f.handler = func(w http.ResponseWriter, body []byte) {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]string{"kind": "stale_epoch", "message": "x"}})
	}
	if _, err := c.SyncNode(context.Background(), &runtime.NodeSyncRequest{
		Node:     runtime.NodeIdentity{NodeID: "n1", BootID: "b", Incarnation: 1},
		Capacity: 4,
	}); errors.Is(err, runtime.ErrStaleEpoch) || err == nil {
		t.Fatalf("500 stale mapped: %v", err)
	}
	// Cancelled ctx surfaces as cancellation, not transport.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.SyncNode(ctx, &runtime.NodeSyncRequest{
		Node:     runtime.NodeIdentity{NodeID: "n1", BootID: "b", Incarnation: 1},
		Capacity: 4,
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("ctx: %v", err)
	}
}
