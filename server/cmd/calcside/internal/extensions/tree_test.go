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
		"contrib/tavily/0.1.0/capability.yaml": goodManifest,
		"contrib/tavily/0.1.0/main.star":       "def search(query):\n    return []\n",
	})
	contrib := filepath.Join(root, "contrib")
	loader := &capext.CapabilityLoader{
		Identifier: "contrib/tavily@0.1.0",
		Options:    &capext.Options{LocalRoots: []string{filepath.Join(t.TempDir(), "contrib"), contrib}},
	}
	mod, err := loader.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if mod.ID != "contrib/tavily@0.1.0" || mod.Manifest.Identifier != "contrib/tavily@0.1.0" {
		t.Fatalf("source was rewritten: %q, %q", mod.ID, mod.Manifest.Identifier)
	}
	files, sum, err := ReadLocalTree(loader.Options.LocalRoots, "contrib/tavily@0.1.0")
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
	for _, source := range []string{"contrib/missing@0.1.0", "other/tavily@0.1.0", "contrib/../contrib/tavily@0.1.0", filepath.Join(contrib, "tavily", "0.1.0")} {
		if _, _, err := ReadLocalTree(loader.Options.LocalRoots, source); err == nil {
			t.Errorf("accepted source %q", source)
		} else if strings.Contains(err.Error(), root) && !strings.Contains(source, root) {
			t.Errorf("error exposes root: %v", err)
		}
	}
	// Unversioned local sources are not resolvable from the filesystem.
	if _, _, err := ReadLocalTree(loader.Options.LocalRoots, "contrib/tavily"); err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("unversioned source resolved: %v", err)
	}
	if _, _, err := ReadLocalTree(nil, "contrib/tavily@0.1.0"); err == nil {
		t.Fatal("local sources enabled without roots")
	}
	t.Chdir(root)
	if _, _, err := ReadLocalTree([]string{"contrib"}, "contrib/tavily@0.1.0"); err != nil {
		t.Fatalf("relative root: %v", err)
	}
	if err := os.Symlink(contrib, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadLocalTree([]string{filepath.Join(root, "linked")}, "linked/tavily@0.1.0"); err != nil {
		t.Fatalf("symlinked root: %v", err)
	}
	outside := writeExt(t, map[string]string{
		"0.1.0/capability.yaml": goodManifest,
		"0.1.0/main.star":       "def search(query):\n    return []\n",
	})
	if err := os.Symlink(outside, filepath.Join(contrib, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadLocalTree([]string{contrib}, "contrib/escape@0.1.0"); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("symlink escape: %v", err)
	}
}

func TestLocalCatalogSources(t *testing.T) {
	root := writeExt(t, map[string]string{
		"contrib/tavily/0.1.0/capability.yaml": goodManifest,
		"contrib/tavily/0.1.0/main.star":       "def search(query):\n    return []\n",
	})
	contrib := filepath.Join(root, "contrib")
	repo := mustRepo(t, &artifactStore{}, capext.Options{CacheDir: t.TempDir(), LocalRoots: []string{contrib, contrib}})
	f := NewCatalog(repo)
	catalog := f.Available(context.Background())
	if len(catalog.Extensions) != 1 || catalog.Extensions[0].Source != "contrib/tavily@0.1.0" {
		t.Fatalf("catalog sources: %+v", catalog)
	}
	if catalog.Extensions[0].Family != "contrib/tavily" || catalog.Extensions[0].Version != "0.1.0" || !strings.HasPrefix(catalog.Extensions[0].Sum, "h1:") {
		t.Fatalf("catalog entry identity: %+v", catalog.Extensions[0])
	}
	if _, err := (&capext.CapabilityLoader{Identifier: capext.CapabilityIdentifier(catalog.Extensions[0].Source), Options: &capext.Options{LocalRoots: []string{contrib}}}).Load(context.Background()); err != nil {
		t.Fatalf("catalog source cannot load: %v", err)
	}
	other := writeExt(t, map[string]string{
		"contrib/tavily/0.1.0/capability.yaml": goodManifest,
		"contrib/tavily/0.1.0/main.star":       "def search(query):\n    return [1]\n",
	})
	repo2 := mustRepo(t, &artifactStore{}, capext.Options{CacheDir: t.TempDir(),
		LocalRoots: []string{contrib, filepath.Join(other, "contrib")}})
	if _, _, err := ReadLocalTree(repo2.opts.LocalRoots, "contrib/tavily@0.1.0"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous source: %v", err)
	}
	if catalog := NewCatalog(repo2).Available(context.Background()); len(catalog.Extensions) != 0 {
		t.Fatalf("ambiguous catalog entries: %+v", catalog)
	}
}

func mustRepo(t *testing.T, st *artifactStore, opts capext.Options) *Repository {
	t.Helper()
	repo, err := NewRepository(st, opts)
	if err != nil {
		t.Fatal(err)
	}
	return repo
}
