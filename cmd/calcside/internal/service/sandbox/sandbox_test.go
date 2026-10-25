package sandbox

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	_ "calcside/cmd/calcside/internal/gormstore"
	"calcside/internal/store"
	"calcside/internal/store/storetest"
)

func TestCapStored(t *testing.T) {
	// under the limit — unchanged, not truncated
	got, cut := capStored("short")
	if cut || got != "short" {
		t.Fatalf("short: %q %v", got, cut)
	}
	// exactly at the limit — unchanged, not truncated
	got, cut = capStored(strings.Repeat("a", storedOutputBytes))
	if cut || len(got) != storedOutputBytes {
		t.Fatalf("exact: %d %v", len(got), cut)
	}
	// over the limit — cut at the limit
	got, cut = capStored(strings.Repeat("a", storedOutputBytes+10))
	if !cut || len(got) != storedOutputBytes {
		t.Fatalf("over: %d %v", len(got), cut)
	}
	// a multi-byte rune straddling the boundary is dropped whole
	s := strings.Repeat("a", storedOutputBytes-1) + "€" + "tail"
	got, cut = capStored(s)
	if !cut || len(got) != storedOutputBytes-1 || !utf8.ValidString(got) {
		t.Fatalf("rune boundary: %d %v", len(got), cut)
	}
}

func TestResolveLLMProvider(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, "sqlite", storetest.SQLite(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	u, err := st.UpsertUserByEmail(ctx, "llm@x.com", "", "")
	if err != nil {
		t.Fatal(err)
	}
	ws := store.PersonalWorkspaceID(u.ID)
	p := &store.LLMProvider{UserID: u.ID, WorkspaceID: ws, Name: "main", Source: "contrib/llm-openai",
		Models: []string{"gpt-5-mini", "gpt-5"},
		Config: json.RawMessage(`{"api_key":"{{secrets.OPENAI_API_KEY}}"}`)}
	if err := st.CreateLLMProvider(ctx, p); err != nil {
		t.Fatal(err)
	}
	s := &Service{st: st}
	raw := []byte(`{"capabilities":{"ext":{"ai":{"source":"contrib/llm"}}}}`)
	out, err := s.resolveLLMProviders(ctx, ws, raw)
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Secrets map[string]struct {
			Ref string `json:"ref"`
		} `json:"secrets"`
		Capabilities struct {
			Ext map[string]struct {
				LLM []struct {
					Name   string   `json:"name"`
					Source string   `json:"source"`
					Models []string `json:"models"`
				} `json:"llm"`
			} `json:"ext"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(out, &spec); err != nil {
		t.Fatal(err)
	}
	got := spec.Capabilities.Ext["ai"].LLM
	if len(got) != 1 || got[0].Name != "main" || got[0].Source != "contrib/llm-openai" || len(got[0].Models) != 2 {
		t.Fatalf("resolved: %+v", got)
	}
	if spec.Secrets["OPENAI_API_KEY"].Ref != "OPENAI_API_KEY" {
		t.Fatalf("provider secret not carried into instance: %+v", spec.Secrets)
	}
}
