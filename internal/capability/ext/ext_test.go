package ext

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"calcside/internal/capability"
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
		"t": map[string]any{"source": dir, "config": cfg},
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
	f := Factory(Options{LocalRoots: []string{dir}})
	spec, _ := json.Marshal(map[string]any{
		"t": map[string]any{"source": dir, "config": map[string]any{"key": "{{  secrets.MY_KEY }}"}},
	})
	v, err := f.Validate(spec, capability.ServerLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if got := v.(validated)["t"].config["key"]; got != "{{secrets.MY_KEY}}" {
		t.Fatalf("not normalized: %v", got)
	}
}
