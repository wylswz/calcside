package worker

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"calcside/internal/placement"
	"calcside/internal/runtime"
)

// TestClientSideEffectingDoesNotRetry proves the contract's hard rule on
// the instance-op surface: Create, Exec, Delete and Write each make
// exactly one node attempt even when the transport fails or the node
// answers with an ambiguous/stale error — an exec may already have run
// script side effects, so a transparent second attempt could double
// them. Read-only Keepalive is the contrast: a stale route earns one
// re-resolve and retry.
func TestClientSideEffectingDoesNotRetry(t *testing.T) {
	networkErr := errors.New("test transport failure")
	ops := []struct {
		name string
		path string
		call func(cl *Client) error
	}{
		{"create", "/runtime/v1/create", func(cl *Client) error {
			_, err := cl.Create(context.Background(), &runtime.CreateRequest{
				InstanceID: "ins_x", Owner: runtime.Owner{UserID: "u"},
				Spec: []byte(`{}`), ExpiresAt: time.Now().Add(time.Hour),
			})
			return err
		}},
		{"exec", "/runtime/v1/exec", func(cl *Client) error {
			_, err := cl.Exec(context.Background(), &runtime.ExecRequest{
				InstanceID: "ins_x", Owner: runtime.Owner{UserID: "u"},
				ExecID: "e1", Code: "print(1)",
			})
			return err
		}},
		{"delete", "/runtime/v1/delete", func(cl *Client) error {
			_, err := cl.Delete(context.Background(), &runtime.DeleteRequest{
				InstanceID: "ins_x", Owner: runtime.Owner{UserID: "u"},
			})
			return err
		}},
		{"write", "/runtime/v1/write", func(cl *Client) error {
			_, err := cl.Write(context.Background(), &runtime.WriteRequest{
				InstanceID: "ins_x", Owner: runtime.Owner{UserID: "u"},
				Path: "f.txt", Content: []byte("x"),
			})
			return err
		}},
	}
	for _, failure := range []string{"network", "stale_epoch", "internal"} {
		for _, op := range ops {
			t.Run(op.name+"/"+failure, func(t *testing.T) {
				requests, resolutions := 0, 0
				p := &testPlacement{
					nodes: []placement.NodeRef{{NodeID: "w1", Addr: "unused.invalid"}},
					resolve: func(context.Context, string) (*placement.Binding, error) {
						resolutions++
						return nil, errors.New("must not re-resolve within one attempt")
					},
				}
				cl := NewClient([]byte("test-key"), "api", p, p.Nodes, p.Resolve)
				cl.router.routes["ins_x"] = routeEnt{nodeID: "w1", addr: "unused.invalid"}
				cl.HTTP = &http.Client{Transport: servingRoundTripper(func(req *http.Request) (*http.Response, error) {
					requests++
					if req.URL.Path != op.path {
						t.Fatalf("unexpected path: %s", req.URL.Path)
					}
					if failure == "network" {
						return nil, networkErr
					}
					return &http.Response{
						StatusCode: http.StatusConflict, Status: "409 Conflict",
						Header: http.Header{"Content-Type": []string{"application/json"}},
						Body:   io.NopCloser(strings.NewReader(`{"error":{"kind":"` + failure + `","message":"generic"}}`)),
					}, nil
				})}
				err := op.call(cl)
				if err == nil || requests != 1 || resolutions != 0 {
					t.Fatalf("err=%v HTTP attempts=%d resolutions=%d", err, requests, resolutions)
				}
				if failure == "network" && !errors.Is(err, networkErr) {
					t.Fatalf("transport error changed: %v", err)
				}
			})
		}
	}
}

// TestClientReadOnlyRetriesStaleRoute is the deliberate contrast: a
// read-only call on a provably stale binding re-resolves once and
// retries — safe because it carries no side effects.
func TestClientReadOnlyRetriesStaleRoute(t *testing.T) {
	requests, resolutions := 0, 0
	p := &testPlacement{
		nodes: []placement.NodeRef{{NodeID: "w1", Addr: "unused.invalid"}},
		resolve: func(context.Context, string) (*placement.Binding, error) {
			resolutions++
			return &placement.Binding{InstanceID: "ins_x", NodeID: "w1", Epoch: 2}, nil
		},
	}
	cl := NewClient([]byte("test-key"), "api", p, p.Nodes, p.Resolve)
	cl.router.routes["ins_x"] = routeEnt{nodeID: "w1", addr: "unused.invalid"}
	attempts := 0
	cl.HTTP = &http.Client{Transport: servingRoundTripper(func(req *http.Request) (*http.Response, error) {
		requests++
		attempts++
		if attempts == 1 {
			return &http.Response{
				StatusCode: http.StatusConflict, Status: "409 Conflict",
				Header: http.Header{"Content-Type": []string{"application/json"}},
				Body:   io.NopCloser(strings.NewReader(`{"error":{"kind":"stale_epoch","message":"old"}}`)),
			}, nil
		}
		// Second attempt still hits the (fake) transport — we only
		// assert it was attempted after a fresh resolve.
		return &http.Response{
			StatusCode: http.StatusConflict, Status: "409 Conflict",
			Header: http.Header{"Content-Type": []string{"application/json"}},
			Body:   io.NopCloser(strings.NewReader(`{"error":{"kind":"stale_epoch","message":"still stale"}}`)),
		}, nil
	})}
	_, err := cl.Keepalive(context.Background(), &runtime.KeepaliveRequest{
		InstanceID: "ins_x", Owner: runtime.Owner{UserID: "u"},
	})
	if err == nil {
		t.Fatal("expected stale_epoch error")
	}
	if requests != 2 || resolutions != 1 {
		t.Fatalf("keepalive: requests=%d resolves=%d, want 2/1", requests, resolutions)
	}
}
