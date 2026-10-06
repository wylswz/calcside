package extensions

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
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
