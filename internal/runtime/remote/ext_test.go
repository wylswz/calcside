package remote_test

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	capext "calcside/internal/capability/ext"
	"calcside/internal/runtime/remote"
)

func TestLocalExtResolverRoundTrip(t *testing.T) {
	// API side: a local root holding one extension tree.
	root := t.TempDir()
	extDir := filepath.Join(root, "myext")
	mustWrite := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(filepath.Join(extDir, "capability.yaml"), "name: myext\nversion: 0.1.0\n")
	mustWrite(filepath.Join(extDir, "main.star"), "def hello():\n    return 1\n")
	mustWrite(filepath.Join(extDir, "sub", "helpers.star"), "def h():\n    return 2\n")

	srv := httptest.NewServer(remote.ExtTreeHandler([]string{root}, testKey))
	defer srv.Close()

	cache := t.TempDir()
	resolve := remote.LocalExtResolver(srv.URL, "w1", testKey, cache)
	p := capext.ParsedIdentifier{Local: extDir}

	dir, err := resolve(context.Background(), p)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	for _, rel := range []string{"capability.yaml", "main.star", "sub/helpers.star"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); err != nil {
			t.Errorf("missing cached file %s: %v", rel, err)
		}
	}
	got, _ := os.ReadFile(filepath.Join(dir, "main.star"))
	if string(got) != "def hello():\n    return 1\n" {
		t.Fatalf("bad content: %q", got)
	}
	// Second resolve hits the cache (sum unchanged) but must not fail.
	if _, err := resolve(context.Background(), p); err != nil {
		t.Fatalf("cached resolve: %v", err)
	}
	// Outside the roots is refused by the API, not just locally.
	if _, err := resolve(context.Background(), capext.ParsedIdentifier{Local: t.TempDir()}); err == nil {
		t.Fatal("expected containment failure")
	}
}

func TestExtTreeHandlerRejectsUnsigned(t *testing.T) {
	root := t.TempDir()
	srv := httptest.NewServer(remote.ExtTreeHandler([]string{root}, testKey))
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
