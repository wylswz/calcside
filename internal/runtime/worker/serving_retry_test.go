package worker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"calcside/internal/placement"
	"calcside/internal/runtime"
)

type servingRoundTripper func(*http.Request) (*http.Response, error)

func (f servingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestClientServingDoesNotRetry(t *testing.T) {
	networkErr := errors.New("test transport failure")
	for _, operation := range []string{"prepare", "invoke"} {
		for _, failure := range []string{"stale_epoch", "not_running", "internal", "network"} {
			t.Run(operation+"/"+failure, func(t *testing.T) {
				requests, resolutions := 0, 0
				nodes := &atomic.Int64{}
				p := &testPlacement{
					nodes:      []placement.NodeRef{{NodeID: "w1", Addr: "unused.invalid"}},
					nodesCalls: nodes,
					resolve: func(context.Context, string) (*placement.Binding, error) {
						resolutions++
						return nil, errors.New("must not re-resolve within one attempt")
					},
				}
				cl := NewClient([]byte("test-key"), "api", p, p.Nodes, p.Resolve)
				cl.router.routes["rep"] = routeEnt{nodeID: "w1", addr: "unused.invalid"}
				cl.HTTP = &http.Client{Transport: servingRoundTripper(func(req *http.Request) (*http.Response, error) {
					requests++
					if req.URL.Path != "/runtime/v1/"+operation {
						t.Fatalf("unexpected path: %s", req.URL.Path)
					}
					if operation == "invoke" {
						var wire runtime.InvokeRequest
						if err := json.NewDecoder(req.Body).Decode(&wire); err != nil {
							t.Fatal(err)
						}
						if wire.ReleaseID != "release-7" || wire.Generation != 7 || wire.Epoch != 9 {
							t.Fatal("routing changed the invocation fence")
						}
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
				var err error
				if operation == "prepare" {
					_, err = cl.Prepare(context.Background(), &runtime.PrepareRequest{
						CreateRequest: runtime.CreateRequest{InstanceID: "rep"},
						PrepareID:     "p", ReleaseID: "release-7", Generation: 7,
					})
				} else {
					_, err = cl.Invoke(context.Background(), &runtime.InvokeRequest{
						InstanceID: "rep", InvokeID: "i", ReleaseID: "release-7", Generation: 7, Epoch: 9,
						Input: json.RawMessage(`{}`),
					})
				}
				if err == nil || requests != 1 || resolutions != 0 {
					t.Fatalf("err=%v HTTP attempts=%d resolutions=%d", err, requests, resolutions)
				}
				if operation == "prepare" && nodes.Load() != 1 {
					t.Fatalf("prepare selected a node %d times", nodes.Load())
				}
				if failure == "network" && !errors.Is(err, networkErr) {
					t.Fatalf("transport error changed: %v", err)
				}
			})
		}
	}
}
