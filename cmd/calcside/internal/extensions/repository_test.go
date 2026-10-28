package extensions

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	capext "calcside/internal/capability/ext"
	"calcside/internal/extartifact"
	"calcside/internal/store"
)

type artifactStore struct {
	mu   sync.Mutex
	data map[string]*store.ExtensionArtifact
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

func TestRepositoryPinsAndPreservesJSON(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "fixtures")
	module := filepath.Join(root, "echo")
	if err := os.MkdirAll(module, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{
		"capability.yaml": "name: echo\nops: [{name: echo, params: [value]}]\n",
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
	if !strings.HasPrefix(ref.Sum, "h1:") {
		t.Fatal("local source was not pinned")
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
	newArtifact, err := repo.Capture(ctx, ref.Source, "")
	if err != nil || newArtifact.Sum == ref.Sum {
		t.Fatalf("new source did not get a new sum: %v", err)
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
