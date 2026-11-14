package storetest

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"calcside/internal/extartifact"
	"calcside/internal/store"
)

func RunExtensionArtifacts(t *testing.T, s store.Store) {
	t.Helper()
	ctx := context.Background()
	files := map[string][]byte{"main.star": []byte("def echo(value): return value\n")}
	sum, err := extartifact.TreeHash(files)
	if err != nil {
		t.Fatal(err)
	}
	artifact := &store.ExtensionArtifact{Source: "fixtures/echo", Sum: sum,
		Files: files, CreatedAt: time.Now().UTC().Truncate(time.Millisecond)}
	if err := s.PutExtensionArtifact(ctx, artifact); err != nil {
		t.Fatal(err)
	}
	if err := s.PutExtensionArtifact(ctx, artifact); err != nil {
		t.Fatalf("idempotent insert: %v", err)
	}
	artifact.Files["main.star"][0] = 'x'
	loaded, err := s.GetExtensionArtifact(ctx, artifact.Source, sum)
	if err != nil || string(loaded.Files["main.star"]) != "def echo(value): return value\n" {
		t.Fatalf("input alias leaked: %v", err)
	}
	loaded.Files["main.star"][0] = 'y'
	again, err := s.GetExtensionArtifact(ctx, artifact.Source, sum)
	if err != nil || again.Files["main.star"][0] != 'd' {
		t.Fatalf("output alias leaked: %v", err)
	}
	if err := s.PutExtensionArtifact(ctx, artifact); !errors.Is(err, store.ErrInvalidExtensionArtifact) {
		t.Fatalf("forged hash accepted: %v", err)
	}
	if _, err := s.GetExtensionArtifact(ctx, "other/source", sum); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("different source resolved: %v", err)
	}
}

// putArtifact stores files under source and returns its h1 sum.
func putArtifact(t *testing.T, ctx context.Context, s store.Store, source string, files map[string][]byte) string {
	t.Helper()
	sum, err := extartifact.TreeHash(files)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutExtensionArtifact(ctx, &store.ExtensionArtifact{
		Source: source, Sum: sum, Files: files, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	return sum
}

// RunExtensionVersions exercises the immutable (family,version)->sum
// binding contract (EP-0020).
func RunExtensionVersions(t *testing.T, s store.Store) {
	t.Helper()
	ctx := context.Background()

	// Validation: bad version tokens and sums are rejected before any write.
	sumA := putArtifact(t, ctx, s, "fixtures/echo@0.1.0", map[string][]byte{"main.star": []byte("def a():\n    return 1\n")})
	for _, bad := range []*store.ExtensionVersion{
		{Family: "", Version: "0.1.0", Sum: sumA, ArtifactSource: "fixtures/echo@0.1.0"},
		{Family: "fixtures/echo", Version: "1..2", Sum: sumA, ArtifactSource: "fixtures/echo@0.1.0"},
		{Family: "fixtures/echo", Version: ".1", Sum: sumA, ArtifactSource: "fixtures/echo@0.1.0"},
		{Family: "fixtures/echo", Version: "", Sum: sumA, ArtifactSource: "fixtures/echo@0.1.0"},
		{Family: "fixtures/echo", Version: "0.1.0", Sum: "nope", ArtifactSource: "fixtures/echo@0.1.0"},
		{Family: "fixtures/echo", Version: "0.1.0", Sum: sumA, ArtifactSource: ""},
	} {
		if err := s.BindExtensionVersion(ctx, bad, nil); !errors.Is(err, store.ErrInvalidExtensionArtifact) {
			t.Fatalf("invalid binding %+v: %v", bad, err)
		}
	}

	// nil artifact with a missing artifact -> ErrNotFound.
	missing := &store.ExtensionVersion{Family: "fixtures/ghost", Version: "0.1.0",
		Sum: sumA, ArtifactSource: "fixtures/ghost@0.1.0", CreatedAt: time.Now().UTC()}
	if err := s.BindExtensionVersion(ctx, missing, nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("binding to missing artifact: %v", err)
	}

	// Bind with artifact in one shot, then idempotent rebind.
	v1 := &store.ExtensionVersion{Family: "fixtures/echo", Version: "0.1.0",
		Sum: sumA, ArtifactSource: "fixtures/echo@0.1.0", CreatedAt: time.Now().UTC()}
	filesA := map[string][]byte{"main.star": []byte("def a():\n    return 1\n")}
	if err := s.BindExtensionVersion(ctx, v1, &store.ExtensionArtifact{
		Source: "fixtures/echo@0.1.0", Sum: sumA, Files: filesA, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.BindExtensionVersion(ctx, v1, nil); err != nil {
		t.Fatalf("idempotent rebind: %v", err)
	}
	got, err := s.GetExtensionVersion(ctx, "fixtures/echo", "0.1.0")
	if err != nil || got.Sum != sumA || got.ArtifactSource != "fixtures/echo@0.1.0" {
		t.Fatalf("binding: %v %+v", err, got)
	}

	// Artifact/source mismatch on the pair is rejected.
	if err := s.BindExtensionVersion(ctx, &store.ExtensionVersion{Family: "f/e", Version: "0.1.0",
		Sum: sumA, ArtifactSource: "other@0.1.0", CreatedAt: time.Now().UTC()},
		&store.ExtensionArtifact{Source: "fixtures/echo@0.1.0", Sum: sumA, Files: filesA}); !errors.Is(err, store.ErrInvalidExtensionArtifact) {
		t.Fatalf("artifact/binding mismatch: %v", err)
	}

	// The same sum under another version is allowed.
	v2 := &store.ExtensionVersion{Family: "fixtures/echo", Version: "0.2.0",
		Sum: sumA, ArtifactSource: "fixtures/echo@0.1.0", CreatedAt: time.Now().UTC()}
	if err := s.BindExtensionVersion(ctx, v2, nil); err != nil {
		t.Fatalf("same sum under second version: %v", err)
	}

	// A different sum for a bound version is a conflict that stores
	// nothing — including the conflicting artifact bytes.
	sumB, err := extartifact.TreeHash(map[string][]byte{"main.star": []byte("def b():\n    return 2\n")})
	if err != nil {
		t.Fatal(err)
	}
	conflict := &store.ExtensionVersion{Family: "fixtures/echo", Version: "0.1.0",
		Sum: sumB, ArtifactSource: "fixtures/echo@0.1.0", CreatedAt: time.Now().UTC()}
	err = s.BindExtensionVersion(ctx, conflict, &store.ExtensionArtifact{
		Source: "fixtures/echo@0.1.0", Sum: sumB,
		Files: map[string][]byte{"main.star": []byte("def b():\n    return 2\n")}, CreatedAt: time.Now().UTC(),
	})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("conflicting rebind: %v", err)
	}
	if _, err := s.GetExtensionArtifact(ctx, "fixtures/echo@0.1.0", sumB); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("conflicting artifact persisted: %v", err)
	}
	got, err = s.GetExtensionVersion(ctx, "fixtures/echo", "0.1.0")
	if err != nil || got.Sum != sumA {
		t.Fatalf("binding overwritten: %v %+v", err, got)
	}

	// List is ordered by (family, version).
	list, err := s.ListExtensionVersions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, b := range list {
		if b.Family == "fixtures/echo" {
			order = append(order, b.Version)
		}
	}
	if len(order) != 2 || order[0] != "0.1.0" || order[1] != "0.2.0" {
		t.Fatalf("list order: %v", order)
	}
	if _, err := s.GetExtensionVersion(ctx, "fixtures/echo", "9.9.9"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing version: %v", err)
	}
}

// RunExtensionVersionConcurrency binds different sums to one
// (family,version) from two store handles on the same database:
// exactly one wins, the loser gets ErrConflict (EP-0020).
func RunExtensionVersionConcurrency(t *testing.T, s1, s2 store.Store) {
	t.Helper()
	ctx := context.Background()
	src := "fixtures/race@0.1.0"
	files1 := map[string][]byte{"main.star": []byte("def a():\n    return 1\n")}
	files2 := map[string][]byte{"main.star": []byte("def a():\n    return 2\n")}
	sum1 := putArtifact(t, ctx, s1, src, files1)
	sum2 := putArtifact(t, ctx, s2, src, files2)

	var wg sync.WaitGroup
	errs := [2]error{}
	bind := func(st store.Store, sum string) error {
		return st.BindExtensionVersion(ctx, &store.ExtensionVersion{
			Family: "fixtures/race", Version: "0.1.0", Sum: sum,
			ArtifactSource: src, CreatedAt: time.Now().UTC(),
		}, nil)
	}
	wg.Go(func() { errs[0] = bind(s1, sum1) })
	wg.Go(func() { errs[1] = bind(s2, sum2) })
	wg.Wait()

	got, err := s1.GetExtensionVersion(ctx, "fixtures/race", "0.1.0")
	if err != nil {
		t.Fatalf("no binding: %v", err)
	}
	switch got.Sum {
	case sum1:
		if errs[0] != nil || !errors.Is(errs[1], store.ErrConflict) {
			t.Fatalf("winner sum1: errs %v %v", errs[0], errs[1])
		}
	case sum2:
		if errs[1] != nil || !errors.Is(errs[0], store.ErrConflict) {
			t.Fatalf("winner sum2: errs %v %v", errs[0], errs[1])
		}
	default:
		t.Fatalf("binding %s is neither contender", got.Sum)
	}
}
