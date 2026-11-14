package dto

import (
	"encoding/json"
	"testing"

	"calcside/internal/store"
)

func TestNewLLMProviderIncludesModels(t *testing.T) {
	p := &store.LLMProvider{ID: "p", Name: "main", Source: "contrib/llm-openai", Models: []string{"gpt-5-mini"}}
	b, err := json.Marshal(NewLLMProvider(p))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	models, ok := got["models"].([]any)
	if !ok || len(models) != 1 || models[0] != "gpt-5-mini" {
		t.Fatalf("models missing from API view: %s", b)
	}
}
