package extensions

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	capext "calcside/internal/capability/ext"
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

const goodManifest = `name: t
version: 0.1.0
dependencies: [net]
ops:
  - {name: search, params: [query]}
`

func TestLocalSourceResolution(t *testing.T) {
	root := writeExt(t, map[string]string{
		"contrib/tavily/capability.yaml": goodManifest,
		"contrib/tavily/main.star":       "def search(query):\n    return []\n",
	})
	contrib := filepath.Join(root, "contrib")
	loader := &capext.CapabilityLoader{
		Identifier: "contrib/tavily",
		Options:    &capext.Options{LocalRoots: []string{filepath.Join(t.TempDir(), "contrib"), contrib}},
	}
	mod, err := loader.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if mod.ID != "contrib/tavily" || mod.Manifest.Identifier != "contrib/tavily" {
		t.Fatalf("source was rewritten: %q, %q", mod.ID, mod.Manifest.Identifier)
	}
	files, sum, err := ReadLocalTree(loader.Options.LocalRoots, "contrib/tavily")
	if err != nil || len(files) != 2 || !strings.HasPrefix(sum, "h1:") {
		t.Fatalf("read tree: files=%v sum=%q err=%v", files, sum, err)
	}
	loader.Sum = sum
	if _, err := loader.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	loader.Sum = "h1:wrong"
	if _, err := loader.Load(context.Background()); err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("sum mismatch: %v", err)
	}
	for _, source := range []string{"contrib/missing", "other/tavily", "contrib/../contrib/tavily", filepath.Join(contrib, "tavily")} {
		if _, _, err := ReadLocalTree(loader.Options.LocalRoots, source); err == nil {
			t.Errorf("accepted source %q", source)
		} else if strings.Contains(err.Error(), root) && !strings.Contains(source, root) {
			t.Errorf("error exposes root: %v", err)
		}
	}
	if _, _, err := ReadLocalTree(nil, "contrib/tavily"); err == nil {
		t.Fatal("local sources enabled without roots")
	}
	t.Chdir(root)
	if _, _, err := ReadLocalTree([]string{"contrib"}, "contrib/tavily"); err != nil {
		t.Fatalf("relative root: %v", err)
	}
	if err := os.Symlink(contrib, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadLocalTree([]string{filepath.Join(root, "linked")}, "linked/tavily"); err != nil {
		t.Fatalf("symlinked root: %v", err)
	}
	outside := writeExt(t, map[string]string{"capability.yaml": goodManifest, "main.star": "def search(query):\n    return []\n"})
	if err := os.Symlink(outside, filepath.Join(contrib, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadLocalTree([]string{contrib}, "contrib/escape"); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("symlink escape: %v", err)
	}
}

func TestLocalCatalogSources(t *testing.T) {
	root := writeExt(t, map[string]string{
		"contrib/tavily/capability.yaml": goodManifest,
		"contrib/tavily/main.star":       "def search(query):\n    return []\n",
	})
	contrib := filepath.Join(root, "contrib")
	f := NewCatalog(capext.Options{LocalRoots: []string{contrib, contrib}})
	catalog := f.Available()
	if len(catalog.Extensions) != 1 || catalog.Extensions[0].Source != "contrib/tavily" {
		t.Fatalf("catalog sources: %+v", catalog)
	}
	if _, err := (&capext.CapabilityLoader{Identifier: capext.CapabilityIdentifier(catalog.Extensions[0].Source), Options: f.opts}).Load(context.Background()); err != nil {
		t.Fatalf("catalog source cannot load: %v", err)
	}
	other := writeExt(t, map[string]string{
		"contrib/tavily/capability.yaml": goodManifest,
		"contrib/tavily/main.star":       "def search(query):\n    return [1]\n",
	})
	f.opts.LocalRoots = append(f.opts.LocalRoots, filepath.Join(other, "contrib"))
	if _, _, err := ReadLocalTree(f.opts.LocalRoots, "contrib/tavily"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous source: %v", err)
	}
	if catalog := f.Available(); len(catalog.Extensions) != 0 {
		t.Fatalf("ambiguous catalog entries: %+v", catalog)
	}
}
