package extensions

import (
	"calcside/internal/runtime"
	"context"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"calcside/internal/runtime/remote"
)

var testKey = []byte("test-shared-key-0123456789abcdef")

func TestExtTreeHandlerRejectsUnsigned(t *testing.T) {
	root := t.TempDir()
	srv := httptest.NewServer(ExtTreeHandler([]string{root}, testKey))
	defer srv.Close()
	resp, err := srv.Client().Get(srv.URL + remote.ExtTreePath + "?path=/x")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("unsigned request: got %d", resp.StatusCode)
	}
}

func TestExtTreeHandlerRoundTrip(t *testing.T) {
	root := writeExt(t, map[string]string{"contrib/myext/capability.yaml": "name: myext\n", "contrib/myext/main.star": "def hello():\n    return 42\n", "contrib/myext/sub/helpers.star": "x = 1\n"})
	handler := ExtTreeHandler([]string{filepath.Join(root, "contrib")}, testKey)
	for _, tc := range []struct {
		source string
		key    []byte
		status int
	}{
		{"contrib/myext", testKey, 200},
		{"contrib/missing", testKey, 400},
		{"../myext", testKey, 400},
		{filepath.Join(root, "contrib/myext"), testKey, 400},
		{"contrib/myext", []byte("wrong-key"), 401},
	} {
		req := httptest.NewRequest(http.MethodGet, remote.ExtTreePath+"?path="+url.QueryEscape(tc.source), nil)
		remote.SignRequest(tc.key, "worker", req, nil, time.Now())
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != tc.status {
			t.Fatalf("%s: %d %s", tc.source, w.Code, w.Body.String())
		}
		if w.Code == 200 {
			var body struct {
				Files map[string][]byte `json:"files"`
				Sum   string            `json:"sum"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			_, sum, err := ReadLocalTree([]string{filepath.Join(root, "contrib")}, tc.source)
			if err != nil || body.Sum != sum || len(body.Files) != 3 || string(body.Files["sub/helpers.star"]) != "x = 1\n" {
				t.Fatalf("tree not preserved: %v %+v", err, body)
			}
		}
	}
	req := httptest.NewRequest(http.MethodGet, remote.ExtTreePath+"?path=contrib/myext", nil)
	remote.SignRequest(testKey, "worker", req, nil, time.Now())
	w := httptest.NewRecorder()
	ExtTreeHandler([]string{root}, nil).ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("unconfigured auth: %d", w.Code)
	}
}

type fakeCtl struct {
	syncs, regs, preps int
	syncErr            error
	ctx                context.Context
}

func (f *fakeCtl) RegisterNode(ctx context.Context, r *runtime.NodeRegisterRequest) (*runtime.NodeRegisterResponse, error) {
	f.regs++
	f.ctx = ctx
	return &runtime.NodeRegisterResponse{}, f.syncErr
}
func (f *fakeCtl) SyncNode(ctx context.Context, r *runtime.NodeSyncRequest) (*runtime.NodeSyncResponse, error) {
	f.syncs++
	f.ctx = ctx
	if f.syncErr != nil {
		return nil, f.syncErr
	}
	return &runtime.NodeSyncResponse{}, nil
}
func (f *fakeCtl) PrepareReplica(ctx context.Context, r *runtime.NodePrepareRequest) (*runtime.NodePrepareResponse, error) {
	f.preps++
	return &runtime.NodePrepareResponse{}, f.syncErr
}

func cbReq(t *testing.T, srv *httptest.Server, key []byte, node, path, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("POST", srv.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	remote.SignRequest(key, node, req, []byte(body), time.Now())
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestCallbackNodeControl(t *testing.T) {
	key := []byte("k12345678901234567890123456789012")
	ctl := &fakeCtl{}
	srv := httptest.NewServer(CallbackHandler(nil, key, ctl))
	defer srv.Close()

	// Unsigned request -> 401.
	req, _ := http.NewRequest("POST", srv.URL+"/internal/v1/nodes/sync", strings.NewReader(`{}`))
	resp, _ := srv.Client().Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unsigned: %d", resp.StatusCode)
	}

	// Wrong node in body -> 403, no call.
	resp = cbReq(t, srv, key, "n1", "/internal/v1/nodes/sync",
		`{"node":{"node_id":"n2","boot_id":"b","incarnation":1},"capacity":4}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || ctl.syncs != 0 {
		t.Fatalf("node mismatch: %d calls=%d", resp.StatusCode, ctl.syncs)
	}

	// Stale boot -> 409 typed kind.
	ctl.syncErr = runtime.ErrStaleEpoch
	resp = cbReq(t, srv, key, "n1", "/internal/v1/nodes/sync",
		`{"node":{"node_id":"n1","boot_id":"b","incarnation":1},"capacity":4}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict || ctl.syncs != 1 {
		t.Fatalf("stale: %d", resp.StatusCode)
	}
	ctl.syncErr = nil

	// Request context is the real request ctx, not the pooled gin one.
	resp = cbReq(t, srv, key, "n1", "/internal/v1/nodes/sync",
		`{"node":{"node_id":"n1","boot_id":"b","incarnation":1},"capacity":4}`)
	resp.Body.Close()
	if resp.StatusCode != 200 || ctl.ctx == nil {
		t.Fatalf("sync: %d", resp.StatusCode)
	}
	if _, isGin := ctl.ctx.(*gin.Context); isGin {
		t.Fatal("pooled gin ctx leaked to control")
	}

	// nil control -> 503, no panic.
	srv2 := httptest.NewServer(CallbackHandler(nil, key, nil))
	defer srv2.Close()
	resp = cbReq(t, srv2, key, "n1", "/internal/v1/nodes/sync",
		`{"node":{"node_id":"n1","boot_id":"b","incarnation":1},"capacity":4}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("nil control: %d", resp.StatusCode)
	}

	// Malformed body -> generic 400 without echo.
	resp = cbReq(t, srv, key, "n1", "/internal/v1/nodes/sync",
		`{"node_id":"n1","broken":`)
	out, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || strings.Contains(string(out), "broken") {
		t.Fatalf("bind: %d %s", resp.StatusCode, out)
	}
}
