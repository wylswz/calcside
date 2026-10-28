package remote_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	"calcside/internal/runtime"
	"calcside/internal/runtime/remote"
)

var testKey = []byte("0123456789abcdef0123456789abcdef")

func testManager(t *testing.T) *instance.Manager {
	t.Helper()
	reg := capability.NewRegistry()
	reg.Register(capfs.Factory())
	reg.Register(capio.Factory())
	m := instance.New(instance.Options{
		Engine:      engine.New(8),
		Registry:    reg,
		Limits:      capability.ServerLimits{MaxFSQuotaBytes: 256 << 20, MaxExecTimeout: time.Minute},
		EvalTimeout: time.Second,
	})
	return m
}

func specBody() json.RawMessage {
	return json.RawMessage(`{"ttl_seconds":300,"limits":{"exec_timeout_ms":30000,"max_steps":1000000,"max_output_bytes":1048576},"capabilities":{"fs":{},"io":{}}}`)
}

func prepareReq(id string, assets map[string][]byte) *runtime.PrepareRequest {
	return &runtime.PrepareRequest{
		CreateRequest: runtime.CreateRequest{
			InstanceID: id,
			Owner:      runtime.Owner{UserID: "u1", Email: "u@x"},
			Spec:       specBody(),
			ExpiresAt:  time.Now().Add(10 * time.Minute),
		},
		ReleaseID:  "rel-1",
		Generation: 1,
		PrepareID:  id + "-p",
		Program: runtime.ProgramBundle{
			Sources: map[string][]byte{"main.star": []byte(`def main(input):
    return {"greeting": "hello " + input["name"], "n": input.get("n", 0)}
`)},
			Assets:       assets,
			InputSchema:  json.RawMessage(`{"type":"object","required":["name"],"properties":{"name":{"type":"string"},"n":{"type":"integer"}},"additionalProperties":false}`),
			OutputSchema: json.RawMessage(`{"type":"object","required":["greeting"],"additionalProperties":true}`),
		},
	}
}

// TestDirectServingParity drives both transports against one Manager
// and asserts the contract is identical.
func TestDirectServingParity(t *testing.T) {
	m := testManager(t)
	t.Cleanup(m.StopReaper)
	srv := httptest.NewServer(remote.NewHandler(m, testKey))
	defer srv.Close()
	cl, err := remote.NewDirect(srv.URL, srv.Client(), testKey, "caller")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	var _ runtime.ServingRuntime = cl

	prep := prepareReq("rep-parity", map[string][]byte{"seed.txt": []byte("v1")})
	d1, err := m.Prepare(ctx, prep)
	if err != nil {
		t.Fatalf("local prepare: %v", err)
	}
	prep2 := prepareReq("rep-parity2", map[string][]byte{"seed.txt": []byte("v1")})
	r1, err := cl.Prepare(ctx, prep2)
	if err != nil {
		t.Fatalf("remote prepare: %v", err)
	}
	if d1.Result.Error != nil || r1.Result.Error != nil {
		t.Fatalf("prepares failed: %+v / %+v", d1.Result.Error, r1.Result.Error)
	}

	inv := func(id string) *runtime.InvokeRequest {
		return &runtime.InvokeRequest{InstanceID: id, Owner: runtime.Owner{UserID: "u1"},
			ReleaseID: "rel-1", Generation: 1, Epoch: 0, InvokeID: id + "-i",
			Input: json.RawMessage(`{"name":"ann","n":2}`)}
	}
	lo, err := m.Invoke(ctx, inv("rep-parity"))
	if err != nil {
		t.Fatalf("local invoke: %v", err)
	}
	ro, err := cl.Invoke(ctx, inv("rep-parity2"))
	if err != nil {
		t.Fatalf("remote invoke: %v", err)
	}
	if lo.Result.Error != nil || ro.Result.Error != nil {
		t.Fatalf("invokes: %+v / %+v", lo.Result.Error, ro.Result.Error)
	}
	if string(lo.Result.Output) != string(ro.Result.Output) {
		t.Fatalf("output diverged: %s vs %s", lo.Result.Output, ro.Result.Output)
	}
	if !strings.Contains(string(ro.Result.Output), "hello ann") {
		t.Fatalf("bad output: %s", ro.Result.Output)
	}

	// Fences and classified errors survive the transport.
	bad := inv("rep-parity2")
	bad.Input = json.RawMessage(`{"nope":1}`)
	ro2, err := cl.Invoke(ctx, bad)
	if err != nil || ro2.Result.Error == nil || ro2.Result.Error.Code != "invalid_input" {
		t.Fatalf("invalid_input lost: %v %+v", err, ro2.Result.Error)
	}
	if len(ro2.Audit.Events) != 0 {
		t.Fatal("audit leaked for invalid input")
	}
	bad2 := inv("rep-parity2")
	bad2.ReleaseID = "rel-2"
	if _, err := cl.Invoke(ctx, bad2); !errors.Is(err, runtime.ErrStaleEpoch) {
		t.Fatalf("fence: %v", err)
	}
	if _, err := cl.Invoke(ctx, &runtime.InvokeRequest{InstanceID: "missing"}); !errors.Is(err, runtime.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
}

// TestPrepareBodyBound exercises the 16MiB prepare allowance vs the
// 4MiB cap on every other route, and the HMAC path still working.
func TestPrepareBodyBound(t *testing.T) {
	m := testManager(t)
	t.Cleanup(m.StopReaper)
	srv := httptest.NewServer(remote.NewHandler(m, testKey))
	defer srv.Close()
	cl, err := remote.NewDirect(srv.URL, srv.Client(), testKey, "caller")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// >4MiB of assets is accepted on prepare (per-file cap is 1MiB,
	// so spread across files; JSON base64 body lands ~7MiB).
	big := map[string][]byte{}
	for i := 0; i < 5; i++ {
		big[fmt.Sprintf("b%d.bin", i)] = make([]byte, 1<<20)
	}
	prep := prepareReq("rep-big", big)
	r, err := cl.Prepare(ctx, prep)
	if err != nil {
		t.Fatalf("big prepare rejected: %v", err)
	}
	if r.Result.Error != nil {
		t.Fatalf("big prepare: %+v", r.Result.Error)
	}

	// >16MiB rejected with 413 before any session build.
	huge := map[string][]byte{}
	for i := 0; i < 13; i++ {
		huge[fmt.Sprintf("h%d.bin", i)] = make([]byte, 1<<20)
	}
	prep2 := prepareReq("rep-huge", huge)
	_, err = cl.Prepare(ctx, prep2)
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("oversize prepare should be rejected 413, got %v", err)
	}

	// An ordinary exec body >4MiB is still rejected.
	bigCode := strings.Repeat("#", 4<<20+16)
	_, err = cl.Exec(ctx, &runtime.ExecRequest{InstanceID: "x", Owner: runtime.Owner{UserID: "u"}, ExecID: "e", Code: bigCode})
	if err == nil {
		t.Fatal("oversize exec accepted")
	}

	// Tampered signature still fails.
	req, _ := http.NewRequest("POST", srv.URL+"/runtime/v1/invoke", bytes.NewReader([]byte(`{"instance_id":"x"}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Calcside-Node", "caller")
	req.Header.Set("X-Calcside-Signature", "tampered")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("tampered sig: %d", resp.StatusCode)
	}
}

// TestBindErrorSanitized asserts binding failures return a generic
// message, never echoing request diagnostics.
func TestBindErrorSanitized(t *testing.T) {
	m := testManager(t)
	t.Cleanup(m.StopReaper)
	srv := httptest.NewServer(remote.NewHandler(m, testKey))
	defer srv.Close()

	body := []byte(`{"instance_id":"x","this_field_is_secret_marker":true`)
	req, _ := http.NewRequest("POST", srv.URL+"/runtime/v1/invoke", bytes.NewReader(body))
	remote.SignRequest(testKey, "caller", req, body, time.Now())
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(out), "this_field_is_secret_marker") || strings.Contains(string(out), "instance_id") {
		t.Fatalf("bind error echoed request: %s", out)
	}
}

// Failure-path audit and classified errors survive the transport.
func TestDirectServingFailures(t *testing.T) {
	m := testManager(t)
	t.Cleanup(m.StopReaper)
	srv := httptest.NewServer(remote.NewHandler(m, testKey))
	defer srv.Close()
	cl, err := remote.NewDirect(srv.URL, srv.Client(), testKey, "caller")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// Pure-init denied op: failure result + deny audit on the 200.
	prep := prepareReq("rep-deny", nil)
	prep.Program.Sources["main.star"] = []byte(`fs.write("/work/x", "d")
def main(input): return {}
`)
	prep.Program.OutputSchema = json.RawMessage(`{}`)
	r, err := cl.Prepare(ctx, prep)
	if err != nil {
		t.Fatal(err)
	}
	if r.Result.Error == nil || r.Result.Error.Code != "invalid_program" {
		t.Fatalf("expected invalid_program, got %+v", r.Result.Error)
	}
	if len(r.Audit.Events) == 0 {
		t.Fatal("denied prepare carried no audit")
	}

	// Output-schema failure and actor audit on invoke.
	prep2 := prepareReq("rep-out", nil)
	prep2.Program.Sources["main.star"] = []byte(`def main(input):
    fs.write("/work/f", "x")
    return {"bad": 1}
`)
	prep2.Program.OutputSchema = json.RawMessage(`{"type":"object","required":["greeting"]}`)
	if _, err := cl.Prepare(ctx, prep2); err != nil {
		t.Fatal(err)
	}
	inv := &runtime.InvokeRequest{InstanceID: "rep-out", Owner: runtime.Owner{UserID: "u1"},
		Actor: runtime.Owner{UserID: "actor-9"}, ReleaseID: "rel-1", Generation: 1,
		InvokeID: "i1", Input: json.RawMessage(`{"name":"x"}`)}
	out, err := cl.Invoke(ctx, inv)
	if err != nil || out.Result.Error == nil || out.Result.Error.Code != "invalid_output" {
		t.Fatalf("expected invalid_output, got %v %+v", err, out.Result.Error)
	}
	found := false
	for _, e := range out.Audit.Events {
		if e.UserID == "actor-9" && e.Capability == "fs" {
			found = true
		}
	}
	if !found {
		t.Fatal("actor audit lost over transport")
	}
}
