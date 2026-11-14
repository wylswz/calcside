package render

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

var testSpec = []byte(`{"$schema":"https://vega.github.io/schema/vega-lite/v6.json","width":200,"height":120,"data":{"values":[{"x":1,"y":2}]},"mark":"point","encoding":{"x":{"field":"x","type":"quantitative"},"y":{"field":"y","type":"quantitative"}}}`)

var tinyPNG = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0x0d}

type recorded struct {
	path        string
	auth        string
	contentType string
	body        map[string]any
}

func server(t *testing.T, status int, contentType string, body []byte, rec *recorded) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.path = r.URL.Path
		rec.auth = r.Header.Get("Authorization")
		rec.contentType = r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &rec.body)
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
}

func TestRenderSuccess(t *testing.T) {
	var rec recorded
	srv := server(t, 200, "image/png", tinyPNG, &rec)
	defer srv.Close()

	png, err := New(srv.URL, "sekret").Render(context.Background(), testSpec)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if string(png[:8]) != string(tinyPNG[:8]) {
		t.Fatalf("png mismatch: %x", png)
	}
	if rec.path != "/render" {
		t.Fatalf("path %q", rec.path)
	}
	if rec.auth != "Bearer sekret" {
		t.Fatalf("auth %q", rec.auth)
	}
	if rec.contentType != "application/json" {
		t.Fatalf("content-type %q", rec.contentType)
	}
	if rec.body["scale"] != 2.0 || rec.body["max_side"] != float64(1024) {
		t.Fatalf("body scale/max_side: %v", rec.body)
	}
	// The spec field must be the canonical re-marshaled JSON.
	specJSON, _ := json.Marshal(rec.body["spec"])
	if !strings.Contains(string(specJSON), `"width":200`) {
		t.Fatalf("spec not forwarded canonically: %s", specJSON)
	}
}

func TestRenderNon200(t *testing.T) {
	var rec recorded
	srv := server(t, 422, "application/json", []byte(`{"error":"render failed"}`), &rec)
	defer srv.Close()
	if _, err := New(srv.URL, "k").Render(context.Background(), testSpec); err == nil ||
		!strings.Contains(err.Error(), "422") {
		t.Fatalf("want status error, got %v", err)
	}
}

func TestRenderWrongContentType(t *testing.T) {
	var rec recorded
	srv := server(t, 200, "image/svg+xml", []byte("<svg/>"), &rec)
	defer srv.Close()
	if _, err := New(srv.URL, "k").Render(context.Background(), testSpec); err == nil ||
		!strings.Contains(err.Error(), "content type") {
		t.Fatalf("want content-type error, got %v", err)
	}
}

func TestRenderOversize(t *testing.T) {
	var rec recorded
	big := make([]byte, maxResponseBytes+16)
	copy(big, tinyPNG)
	srv := server(t, 200, "image/png", big, &rec)
	defer srv.Close()
	if _, err := New(srv.URL, "k").Render(context.Background(), testSpec); err == nil ||
		!strings.Contains(err.Error(), "too large") {
		t.Fatalf("want oversize error, got %v", err)
	}
}

func TestRenderNotPNG(t *testing.T) {
	var rec recorded
	srv := server(t, 200, "image/png", []byte("not a png at all............"), &rec)
	defer srv.Close()
	if _, err := New(srv.URL, "k").Render(context.Background(), testSpec); err == nil ||
		!strings.Contains(err.Error(), "not a PNG") {
		t.Fatalf("want png-magic error, got %v", err)
	}
}

func TestRenderInvalidSpecNeverSent(t *testing.T) {
	var rec recorded
	srv := server(t, 200, "image/png", tinyPNG, &rec)
	defer srv.Close()
	bad := []byte(`{"data":{"url":"https://evil.example/x.json"},"mark":"point"}`)
	if _, err := New(srv.URL, "k").Render(context.Background(), bad); err == nil {
		t.Fatal("want validation error")
	}
	if rec.path != "" {
		t.Fatalf("invalid spec reached the renderer: %s", rec.path)
	}
}

func TestRenderUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	if _, err := New(url, "k").Render(context.Background(), testSpec); err == nil {
		t.Fatal("want connection error")
	}
}
