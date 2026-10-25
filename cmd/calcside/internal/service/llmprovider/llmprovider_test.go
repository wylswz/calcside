package llmprovider

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	_ "calcside/cmd/calcside/internal/gormstore"
	"calcside/cmd/calcside/internal/service"
	"calcside/cmd/calcside/internal/service/access"
	capext "calcside/internal/capability/ext"
	"calcside/internal/store"
	"calcside/internal/store/storetest"
)

func TestCreateRequiresWorkspaceSecret(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, "sqlite", storetest.SQLite(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	u, err := st.UpsertUserByEmail(ctx, "prov@x.com", "", "")
	if err != nil {
		t.Fatal(err)
	}
	ws := store.PersonalWorkspaceID(u.ID)
	if err := st.CreateSecret(ctx, &store.Secret{UserID: u.ID, WorkspaceID: ws, Name: "OPENAI_API_KEY", Ciphertext: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	s := New(st, access.New(st), &capext.Options{LocalRoots: []string{filepath.Join("..", "..", "..", "..", "..", "contrib")}})
	a := service.Actor{UserID: u.ID, WorkspaceID: ws}
	cfg := json.RawMessage(`{"api_key":"{{secrets.MISSING}}"}`)
	if _, err := s.Create(ctx, a, "main", "contrib/llm-openai", "", []string{"gpt-5-mini"}, cfg); err == nil {
		t.Fatal("expected missing secret error")
	}
	cfg = json.RawMessage(`{"api_key":"{{secrets.OPENAI_API_KEY}}"}`)
	p, err := s.Create(ctx, a, "main", "contrib/llm-openai", "", []string{" gpt-5-mini ", "gpt-5"}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Models) != 2 || p.Models[0] != "gpt-5-mini" {
		t.Fatalf("models: %v", p.Models)
	}
	p, err = s.Create(ctx, a, "defaults", "contrib/llm-openai", "", nil, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Models) < 2 {
		t.Fatalf("expected official default model list, got %v", p.Models)
	}
	compat := json.RawMessage(`{"base_url":"http://localhost:11434/v1","api_key":""}`)
	if _, err := s.Create(ctx, a, "compat", "contrib/llm-openai-compat", "", nil, compat); err == nil {
		t.Fatal("expected models required for openai_compatible")
	}
}
