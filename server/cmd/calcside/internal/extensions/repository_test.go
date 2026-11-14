package extensions

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	capext "calcside/internal/capability/ext"
	"calcside/internal/extartifact"
	"calcside/internal/store"
)

type artifactStore struct {
	mu       sync.Mutex
	data     map[string]*store.ExtensionArtifact
	versions map[string]store.ExtensionVersion
}

func (s *artifactStore) GetExtensionArtifact(_ context.Context, source, sum string) (*store.ExtensionArtifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.data[source+"\x00"+sum]
	if a == nil {
		return nil, store.ErrNotFound
	}
	return a.Clone(), nil
}

func (s *artifactStore) PutExtensionArtifact(_ context.Context, a *store.ExtensionArtifact) error {
	if err := a.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data == nil {
		s.data = map[string]*store.ExtensionArtifact{}
	}
	s.data[a.Source+"\x00"+a.Sum] = a.Clone()
	return nil
}

// BindExtensionVersion mirrors the store contract: insert-then-compare
// with rollback of the artifact on conflict.
func (s *artifactStore) BindExtensionVersion(_ context.Context, v *store.ExtensionVersion, artifact *store.ExtensionArtifact) error {
	if err := v.Validate(); err != nil {
		return err
	}
	if artifact != nil && (artifact.Source != v.ArtifactSource || artifact.Sum != v.Sum) {
		return store.ErrInvalidExtensionArtifact
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data == nil {
		s.data = map[string]*store.ExtensionArtifact{}
	}
	if s.versions == nil {
		s.versions = map[string]store.ExtensionVersion{}
	}
	if artifact == nil {
		if s.data[v.ArtifactSource+"\x00"+v.Sum] == nil {
			return store.ErrNotFound
		}
	}
	key := v.Family + "\x00" + v.Version
	if existing, ok := s.versions[key]; ok && existing.Sum != v.Sum {
		return store.ErrConflict
	}
	if artifact != nil {
		if err := artifact.Validate(); err != nil {
			return err
		}
		s.data[artifact.Source+"\x00"+artifact.Sum] = artifact.Clone()
	}
	if _, ok := s.versions[key]; !ok {
		s.versions[key] = *v
	}
	return nil
}

func (s *artifactStore) GetExtensionVersion(_ context.Context, family, version string) (*store.ExtensionVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.versions[family+"\x00"+version]
	if !ok {
		return nil, store.ErrNotFound
	}
	out := v
	return &out, nil
}

func (s *artifactStore) ListExtensionVersions(_ context.Context) ([]store.ExtensionVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]store.ExtensionVersion, 0, len(s.versions))
	for _, v := range s.versions {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Family != out[j].Family {
			return out[i].Family < out[j].Family
		}
		return out[i].Version < out[j].Version
	})
	return out, nil
}

func TestRepositoryPinsAndPreservesJSON(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "fixtures")
	module := filepath.Join(root, "echo", "0.1.0")
	if err := os.MkdirAll(module, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{
		"capability.yaml": "name: echo\nversion: 0.1.0\nops: [{name: echo, params: [value]}]\n",
		"main.star":       "def echo(value): return value\n",
	} {
		if err := os.WriteFile(filepath.Join(module, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	st := &artifactStore{}
	opts := capext.Options{CacheDir: t.TempDir(), LocalRoots: []string{root}}
	repo, err := NewRepository(st, opts)
	if err != nil {
		t.Fatal(err)
	}
	// An unversioned legacy source resolves to the sole version
	// candidate and is rewritten to the canonical family@version form.
	pinned, err := repo.PinSpec(ctx, []byte(`{"capabilities":{"ext":{"echo":{"source":"fixtures/echo","config":{"n":9007199254740993}}}},"policies":[],"env":{"N":"v"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(pinned), "9007199254740993") || !strings.Contains(string(pinned), `"policies":[]`) {
		t.Fatalf("raw JSON changed: %s", pinned)
	}
	var spec struct {
		Capabilities struct {
			Ext map[string]struct{ Source, Sum string }
		}
	}
	if err := json.Unmarshal(pinned, &spec); err != nil {
		t.Fatal(err)
	}
	ref := spec.Capabilities.Ext["echo"]
	if ref.Source != "fixtures/echo@0.1.0" || !strings.HasPrefix(ref.Sum, "h1:") {
		t.Fatalf("source not canonicalized: %+v", ref)
	}
	dir, err := repo.Resolve(ctx, ref.Source, ref.Sum)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(module, "main.star"), []byte("def echo(value): return 99\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old, err := repo.Resolve(ctx, ref.Source, ref.Sum)
	if err != nil || old != dir {
		t.Fatalf("old snapshot unavailable: %v", err)
	}
	bytes, err := os.ReadFile(filepath.Join(old, "main.star"))
	if err != nil || string(bytes) != "def echo(value): return value\n" {
		t.Fatalf("published snapshot drifted: %v", err)
	}
	// The bound version keeps its original sum even after edits; a new
	// version directory captures under its own (family, version).
	if again, err := repo.Capture(ctx, ref.Source, ""); err != nil || again.Sum != ref.Sum {
		t.Fatalf("bound version drifted: %v", err)
	}
	module2 := filepath.Join(root, "echo", "0.2.0")
	if err := os.MkdirAll(module2, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{
		"capability.yaml": "name: echo\nversion: 0.2.0\nops: [{name: echo, params: [value]}]\n",
		"main.star":       "def echo(value): return 99\n",
	} {
		if err := os.WriteFile(filepath.Join(module2, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	newArtifact, err := repo.Capture(ctx, "fixtures/echo@0.2.0", "")
	if err != nil || newArtifact.Sum == ref.Sum {
		t.Fatalf("new version did not get a new sum: %v", err)
	}
	revoked, err := NewRepository(st, capext.Options{CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := revoked.Resolve(ctx, ref.Source, ref.Sum); err == nil {
		t.Fatal("cache bypassed revoked source policy")
	}
}

func TestPinProviderModules(t *testing.T) {
	st := &artifactStore{}
	sums := map[string]string{}
	for _, source := range []string{"fixtures/llm", "fixtures/provider"} {
		files := map[string][]byte{"main.star": []byte("def f(): pass\n")}
		sum, err := extartifact.TreeHash(files)
		if err != nil {
			t.Fatal(err)
		}
		sums[source] = sum
		if err := st.PutExtensionArtifact(context.Background(), &store.ExtensionArtifact{
			Source: source, Sum: sum, Files: files, CreatedAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	repo, err := NewRepository(st, capext.Options{CacheDir: t.TempDir(), LocalRoots: []string{"/configured/fixtures"}})
	if err != nil {
		t.Fatal(err)
	}
	raw := `{"capabilities":{"ext":{"llm":{"source":"fixtures/llm","sum":"` + sums["fixtures/llm"] +
		`","llm":[{"name":"p","source":"fixtures/provider","sum":"` + sums["fixtures/provider"] +
		`","config":{"key":"{{secrets.KEY}}","n":9007199254740993}}]}}},"policies":[]}`
	pinned, err := repo.PinSpec(context.Background(), []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(pinned), "{{secrets.KEY}}") || !strings.Contains(string(pinned), "9007199254740993") {
		t.Fatal("provider configuration was modified")
	}
}

func TestRepositoryManifest(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "fixtures")
	module := filepath.Join(root, "echo", "0.1.0")
	if err := os.MkdirAll(module, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{
		"capability.yaml": "name: echo\nversion: 0.1.0\nops: [{name: echo, params: [value]}]\nconfig:\n  - {name: api_key, type: secret}\n",
		"main.star":       "def echo(value): return value\n",
	} {
		if err := os.WriteFile(filepath.Join(module, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	st := &artifactStore{}
	repo, err := NewRepository(st, capext.Options{CacheDir: t.TempDir(), LocalRoots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	m, err := repo.Manifest(ctx, "fixtures/echo@0.1.0", "")
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "echo" || len(m.Config) != 1 || m.Config[0].Type != "secret" {
		t.Fatalf("manifest: %+v", m)
	}
	// Revoked local namespace cannot be inspected either.
	revoked, err := NewRepository(st, capext.Options{CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := revoked.Manifest(ctx, "fixtures/echo@0.1.0", ""); err == nil {
		t.Fatal("manifest read bypassed revoked source policy")
	}
}
