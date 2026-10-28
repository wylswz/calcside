package extensions

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"calcside/internal/extartifact"
	"calcside/internal/runtime"
	"calcside/internal/runtime/remote"
	"calcside/internal/store"
)

var testKey = []byte("test-shared-key-0123456789abcdef")

type fakeArtifactStore map[string]*store.ExtensionArtifact

func (f fakeArtifactStore) PutExtensionArtifact(_ context.Context, a *store.ExtensionArtifact) error {
	f[a.Source+"\x00"+a.Sum] = a
	return nil
}
func (f fakeArtifactStore) GetExtensionArtifact(_ context.Context, source, sum string) (*store.ExtensionArtifact, error) {
	if a, ok := f[source+"\x00"+sum]; ok {
		return a, nil
	}
	return nil, store.ErrNotFound
}

func seedArtifact(t *testing.T, st fakeArtifactStore, source string, files map[string][]byte) string {
	t.Helper()
	sum, err := extartifact.TreeHash(files)
	if err != nil {
		t.Fatal(err)
	}
	st.PutExtensionArtifact(context.Background(), &store.ExtensionArtifact{Source: source, Sum: sum, Files: files})
	return sum
}

func TestExtTreeHandlerRejectsUnsigned(t *testing.T) {
	srv := httptest.NewServer(ExtTreeHandler(fakeArtifactStore{}, testKey))
	defer srv.Close()
	resp, err := srv.Client().Get(srv.URL + remote.ExtTreePath + "?path=/x&sum=h1:x")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("unsigned request: got %d", resp.StatusCode)
	}
}

func TestExtTreeHandlerRoundTrip(t *testing.T) {
	files := map[string][]byte{
		"capability.yaml":  []byte("name: myext\n"),
		"main.star":        []byte("def hello():\n    return 42\n"),
		"sub/helpers.star": []byte("x = 1\n"),
	}
	st := fakeArtifactStore{}
	sum := seedArtifact(t, st, "contrib/myext", files)
	handler := ExtTreeHandler(st, testKey)
	for _, tc := range []struct {
		name, source, sum string
		key               []byte
		status            int
	}{
		{"ok", "contrib/myext", sum, testKey, 200},
		{"missing", "contrib/missing", sum, testKey, 404},
		{"badsum", "contrib/myext", "h1:not-base64", testKey, 400},
		{"nosum", "contrib/myext", "", testKey, 400},
		{"wrongkey", "contrib/myext", sum, []byte("wrong-key"), 401},
	} {
		q := url.Values{"path": {tc.source}}
		if tc.sum != "" {
			q.Set("sum", tc.sum)
		}
		req := httptest.NewRequest(http.MethodGet, remote.ExtTreePath+"?"+q.Encode(), nil)
		remote.SignRequest(tc.key, "worker", req, nil, time.Now())
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != tc.status {
			t.Fatalf("%s: %d %s", tc.name, w.Code, w.Body.String())
		}
		if w.Code == 200 {
			var body struct {
				Files map[string][]byte `json:"files"`
				Sum   string            `json:"sum"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Sum != sum || len(body.Files) != 3 || string(body.Files["sub/helpers.star"]) != "x = 1\n" {
				t.Fatalf("tree not preserved: %+v", body)
			}
		}
	}
	req := httptest.NewRequest(http.MethodGet, remote.ExtTreePath+"?path=contrib/myext&sum="+sum, nil)
	remote.SignRequest(testKey, "worker", req, nil, time.Now())
	w := httptest.NewRecorder()
	ExtTreeHandler(st, nil).ServeHTTP(w, req)
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
	srv := httptest.NewServer(WithControl(fakeArtifactStore{}, key, ctl))
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
	srv2 := httptest.NewServer(WithControl(fakeArtifactStore{}, key, nil))
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

// ctxCapStore captures the context passed to the store — the handler
// must never forward the pooled gin.Context.
type ctxCapStore struct {
	fakeArtifactStore
	seen context.Context
}

func (f *ctxCapStore) GetExtensionArtifact(ctx context.Context, source, sum string) (*store.ExtensionArtifact, error) {
	f.seen = ctx
	return f.fakeArtifactStore.GetExtensionArtifact(ctx, source, sum)
}

func TestExtTreeHandlerContextAndValidation(t *testing.T) {
	files := map[string][]byte{"a": []byte("b")}
	st := &ctxCapStore{fakeArtifactStore: fakeArtifactStore{}}
	sum := seedArtifact(t, st.fakeArtifactStore, "contrib/x", files)
	h := ExtTreeHandler(st, testKey)
	get := func(q string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, remote.ExtTreePath+"?"+q, nil)
		remote.SignRequest(testKey, "worker", req, nil, time.Now())
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	// nil store -> 500, not panic
	nilH := ExtTreeHandler(nil, testKey)
	req := httptest.NewRequest(http.MethodGet, remote.ExtTreePath+"?path=contrib/x&sum="+sum, nil)
	remote.SignRequest(testKey, "worker", req, nil, time.Now())
	w := httptest.NewRecorder()
	nilH.ServeHTTP(w, req)
	if w.Code != 500 {
		t.Fatalf("nil store: %d", w.Code)
	}
	// invalid source identifier -> 400
	if w := get("path=..%2Fx&sum=" + sum); w.Code != 400 {
		t.Fatalf("bad path: %d", w.Code)
	}
	// artifact source mismatch -> 500 (tampered store)
	bad := fakeArtifactStore{}
	a := &store.ExtensionArtifact{Source: "other", Sum: sum, Files: files}
	bad["contrib/x\x00"+sum] = a
	w = get("path=contrib/x&sum=" + sum)
	if w.Code != 200 {
		t.Fatalf("seeded: %d %s", w.Code, w.Body.String())
	}
	h2 := ExtTreeHandler(bad, testKey)
	req = httptest.NewRequest(http.MethodGet, remote.ExtTreePath+"?path=contrib/x&sum="+sum, nil)
	remote.SignRequest(testKey, "worker", req, nil, time.Now())
	w = httptest.NewRecorder()
	h2.ServeHTTP(w, req)
	if w.Code != 500 {
		t.Fatalf("source mismatch: %d", w.Code)
	}
	// the store saw the real request context, not the pooled gin ctx
	if st.seen == nil {
		t.Fatal("no context reached the store")
	}
	if _, isGin := st.seen.(*gin.Context); isGin {
		t.Fatal("pooled gin.Context leaked to the store")
	}
}
