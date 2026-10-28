package storetest

import (
	"context"
	"errors"
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
