package ext

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"calcside/internal/capability"
	"calcside/internal/types"
)

// secretExt writes an extension with one secret-typed and one
// string-typed config field, and validates the given alias config.
func secretExtValidate(t *testing.T, cfg map[string]any) error {
	t.Helper()
	dir := writeExt(t, map[string]string{
		"capability.yaml": `name: t
ops: [{name: a}]
config:
  - {name: key, type: secret, default: "{{secrets.DEF_KEY}}"}
  - {name: base, type: string, default: x}
`,
		"main.star": "def a():\n    return 1\n",
	})
	f := Factory(Options{LocalRoots: []string{dir, filepath.Dir(dir)}})
	spec, _ := json.Marshal(map[string]any{
		"t": map[string]any{"source": localID(dir), "config": cfg},
	})
	_, err := f.Validate(spec, capability.ServerLimits{})
	return err
}

func TestSecretConfigValidate(t *testing.T) {
	if err := secretExtValidate(t, nil); err != nil {
		t.Fatalf("default ref: %v", err)
	}
	if err := secretExtValidate(t, map[string]any{"key": "{{ secrets.MY_KEY }}"}); err != nil {
		t.Fatalf("whitespace ref: %v", err)
	}
	err := secretExtValidate(t, map[string]any{"key": "plaintext"})
	if err == nil || !strings.Contains(err.Error(), "secret reference") {
		t.Fatalf("plaintext secret field: %v", err)
	}
	err = secretExtValidate(t, map[string]any{"base": "http://x/{{secrets.K}}"})
	if err == nil || !strings.Contains(err.Error(), "type secret") {
		t.Fatalf("placeholder in string field: %v", err)
	}
	// Normalization: whitespace variant becomes {{secrets.NAME}}.
	dir := writeExt(t, map[string]string{
		"capability.yaml": "name: t\nops: [{name: a}]\nconfig:\n  - {name: key, type: secret}\n",
		"main.star":       "def a():\n    return 1\n",
	})
	f := Factory(Options{LocalRoots: []string{filepath.Dir(dir)}})
	spec, _ := json.Marshal(map[string]any{
		"t": map[string]any{"source": localID(dir), "config": map[string]any{"key": "{{  secrets.MY_KEY }}"}},
	})
	v, err := f.Validate(spec, capability.ServerLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if got := v.(validated)["t"].config["key"]; got != "{{secrets.MY_KEY}}" {
		t.Fatalf("not normalized: %v", got)
	}
}

func TestLLMProviderSnapshot(t *testing.T) {
	dir := writeExt(t, map[string]string{
		"llm/capability.yaml": `name: llm
capability: llm
ops: [{name: respond, params: [messages]}]
config:
  - {name: provider, type: string, default: ""}
  - {name: model, type: string, default: ""}
`,
		"llm/main.star": "def respond(messages):\n    return provider.list_models()\n",
		"prov/capability.yaml": `name: p
interface: llm
models: [m1, m2]
ops:
  - {name: respond, params: [messages, model, options]}
  - {name: structured, params: [messages, schema, model]}
config:
  - {name: api_key, type: secret, default: "{{secrets.KEY}}"}
`,
		"prov/main.star": "def respond(messages, model, options):\n    return model\n\ndef structured(messages, schema, model):\n    return {}\n",
	})
	base := filepath.Base(dir)
	llmSource := base + "/llm"
	provSource := base + "/prov"
	f := Factory(Options{LocalRoots: []string{dir}})
	snapshot := map[string]any{
		"name":   "main",
		"source": provSource,
		"models": []string{"m1"},
		"config": map[string]any{"api_key": "{{ secrets.KEY }}"},
	}
	validate := func(v any) error {
		alias := map[string]any{"source": llmSource}
		if v != nil {
			alias["llm"] = []any{v}
		}
		spec, _ := json.Marshal(map[string]any{"llm": alias})
		_, err := f.Validate(spec, capability.ServerLimits{})
		return err
	}
	if err := validate(snapshot); err != nil {
		t.Fatalf("valid snapshot: %v", err)
	}
	if err := validate(nil); err != nil {
		t.Fatalf("missing snapshot: %v", err)
	}
	bad := map[string]any{"name": "main", "source": base + "/missing", "models": []string{"m"}, "config": map[string]any{}}
	if err := validate(bad); err == nil {
		t.Fatal("expected missing provider ext error")
	}
}

func TestLLMExtInit(t *testing.T) {
	f := Factory(Options{LocalRoots: []string{filepath.Join("..", "..", "..", "contrib")}})
	spec, _ := json.Marshal(map[string]any{
		"llm": map[string]any{
			"source": "contrib/llm",
			"llm": []any{
				map[string]any{
					"name":   "local",
					"source": "contrib/llm-openai-compat",
					"models": []string{"llama-3"},
					"config": map[string]any{
						"base_url": "http://127.0.0.1:11434/v1",
						"api_key":  "",
					},
				},
				map[string]any{
					"name":   "openai",
					"source": "contrib/llm-openai",
					"config": map[string]any{"api_key": ""},
				},
			},
		},
	})
	cfg, err := f.Validate(spec, capability.ServerLimits{})
	if err != nil {
		t.Fatal(err)
	}
	netBinding := starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{})
	v, closer, err := f.New(cfg, capability.InstanceEnv{
		Context:  context.Background(),
		Bindings: map[types.CapabilityName]starlark.Value{types.CapNet: netBinding},
	})
	if closer != nil {
		defer closer.Close()
	}
	if err != nil {
		t.Fatalf("init llm ext: %v", err)
	}
	llm, err := v.(starlark.HasAttrs).Attr("llm")
	if err != nil {
		t.Fatal(err)
	}
	local, err := llm.(starlark.HasAttrs).Attr("local")
	if err != nil {
		t.Fatal(err)
	}
	enum, err := local.(starlark.HasAttrs).Attr("models")
	if err != nil {
		t.Fatal(err)
	}
	got, err := enum.(starlark.HasAttrs).Attr("LLAMA_3")
	if err != nil {
		t.Fatal(err)
	}
	if got != starlark.String("llama-3") {
		t.Fatalf("typed model: %v", got)
	}
}

func TestModelConst(t *testing.T) {
	cases := map[string]string{
		"gpt-6-luna":       "GPT_6_LUNA",
		"gemini-3.8-flash": "GEMINI_3_8_FLASH",
		"claude_fable-5-1": "CLAUDE_FABLE_5_1",
		"3d":               "M_3D",
		"":                 "MODEL",
	}
	for in, want := range cases {
		if got := modelConst(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}
