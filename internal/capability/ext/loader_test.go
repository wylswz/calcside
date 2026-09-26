package ext

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.starlark.net/starlark"
)

// writeExt builds a local extension tree.
func writeExt(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, src := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// loadLocal loads dir as a local extension under LocalRoots=parent.
func loadLocal(t *testing.T, dir, sum string) (*Module, error) {
	t.Helper()
	l := &CapabilityLoader{
		Identifier: CapabilityIdentifier(dir),
		Sum:        sum,
		Options:    &Options{LocalRoots: []string{filepath.Dir(dir)}},
	}
	return l.Load(context.Background())
}

const goodManifest = `name: t
version: 0.1.0
dependencies: [net]
ops:
  - {name: search, params: [query]}
`

func TestLoadLocal(t *testing.T) {
	dir := writeExt(t, map[string]string{
		"capability.yaml": goodManifest,
		"main.star":       "def search(query):\n    return []\n",
	})
	m, err := loadLocal(t, dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if m.Manifest.Name != "t" || m.Manifest.Dependencies[0] != "net" {
		t.Fatalf("bad manifest %+v", m.Manifest)
	}
}

func TestLoadErrors(t *testing.T) {
	cases := []struct {
		name    string
		files   map[string]string
		wantErr string
	}{
		{"missing op fn", map[string]string{
			"capability.yaml": goodManifest,
			"main.star":       "x = 1\n",
		}, "not a function"},
		{"op not fn", map[string]string{
			"capability.yaml": goodManifest,
			"main.star":       "search = 3\n",
		}, "not a function"},
		{"unknown dep", map[string]string{
			"capability.yaml": "name: t\nops: [{name: a}]\ndependencies: [bogus]\n",
			"main.star":       "def a():\n    return 1\n",
		}, "invalid base dependency"},
		{"ext dep", map[string]string{
			"capability.yaml": "name: t\nops: [{name: a}]\ndependencies: [ext]\n",
			"main.star":       "def a():\n    return 1\n",
		}, "invalid base dependency"},
		{"source dep obj", map[string]string{
			"capability.yaml": "name: t\nops: [{name: a}]\ndependencies:\n  - {source: github.com/x/y@v1, sum: h1:x}\n",
			"main.star":       "def a():\n    return 1\n",
		}, "unmarshal"},
		{"syntax error", map[string]string{
			"capability.yaml": goodManifest,
			"main.star":       "def search(:\n",
		}, ""},
		{"no manifest", map[string]string{
			"main.star": "def search(q):\n    return []\n",
		}, "capability.yaml"},
		{"no main", map[string]string{
			"capability.yaml": goodManifest,
		}, "main.star"},
	}
	for _, tc := range cases {
		dir := writeExt(t, tc.files)
		_, err := loadLocal(t, dir, "")
		if err == nil {
			t.Errorf("%s: expected error", tc.name)
		} else if tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("%s: want %q in %v", tc.name, tc.wantErr, err)
		}
	}
}

func TestSymlinkRejected(t *testing.T) {
	dir := writeExt(t, map[string]string{
		"capability.yaml": goodManifest,
		"main.star":       "def search(q):\n    return []\n",
	})
	if err := os.Symlink("/etc/passwd", filepath.Join(dir, "evil.star")); err != nil {
		t.Skip("symlinks unsupported")
	}
	if _, err := loadLocal(t, dir, ""); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("want symlink error, got %v", err)
	}
}

func TestLocalOutsideRoots(t *testing.T) {
	dir := writeExt(t, map[string]string{
		"capability.yaml": goodManifest,
		"main.star":       "def search(q):\n    return []\n",
	})
	l := &CapabilityLoader{
		Identifier: CapabilityIdentifier(dir),
		Options:    &Options{LocalRoots: []string{filepath.Join(t.TempDir(), "elsewhere")}},
	}
	if _, err := l.Load(context.Background()); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("want outside-roots error, got %v", err)
	}
	// No roots at all: local disabled.
	l.Options = &Options{}
	if _, err := l.Load(context.Background()); err == nil {
		t.Fatal("local load with no roots should fail")
	}
}

func TestLoadEscape(t *testing.T) {
	dir := writeExt(t, map[string]string{
		"capability.yaml": "name: t\nops: [{name: a}]\n",
		"main.star":       "load(\"../escape.star\", \"x\")\ndef a():\n    return x\n",
	})
	m, err := loadLocal(t, dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.init(nil, nil, 0); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("want escape error, got %v", err)
	}
	if _, err := m.init(nil, nil, 0); err == nil {
		t.Fatal("expected load error")
	}
	// Absolute path load also rejected.
	dir2 := writeExt(t, map[string]string{
		"capability.yaml": "name: t\nops: [{name: a}]\n",
		"main.star":       "load(\"/etc/x.star\", \"x\")\ndef a():\n    return x\n",
	})
	m2, err := loadLocal(t, dir2, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m2.init(nil, nil, 0); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("want absolute error, got %v", err)
	}
}

func TestInitCallsAndConfig(t *testing.T) {
	dir := writeExt(t, map[string]string{
		"capability.yaml": `name: t
ops: [{name: get, params: []}]
config:
  - {name: n, type: int, default: 7}
`,
		"main.star": "load(\"util.star\", \"helper\")\ndef get():\n    return helper() + config[\"n\"]\n",
		"util.star": "def helper():\n    return 1\n",
	})
	m, err := loadLocal(t, dir, "")
	if err != nil {
		t.Fatal(err)
	}
	g, err := m.init(nil, map[string]any{"n": int64(5)}, 0)
	if err != nil {
		t.Fatal(err)
	}
	thread := &starlark.Thread{Name: "t"}
	v, err := starlark.Call(thread, g["get"], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if v.(starlark.Int) != starlark.MakeInt(6) {
		t.Fatalf("got %v", v)
	}
}
