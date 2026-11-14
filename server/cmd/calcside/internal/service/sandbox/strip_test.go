package sandbox

import (
	"encoding/json"
	"testing"

	"calcside/internal/types"
)

func env(alias, field, ref string) map[string]any {
	return map[string]any{"extensions": map[string]any{alias: map[string]any{
		"bindings": map[string]any{"default": map[string]any{"secrets": map[string]any{field: map[string]any{"ref": ref}}}}}}}
}
func spec(t *testing.T, e map[string]any, secs map[string]any, extCfg map[string]any) []byte {
	b, _ := json.Marshal(map[string]any{"environment": e, "secrets": secs, "capabilities": map[string]any{"ext": extCfg}})
	return b
}

func TestStripDerivedRemovesOnlyVerified(t *testing.T) {
	inner := envSecretName("tav", "api_key")
	out, err := StripEnvironmentDerived(spec(t, env("tav", "api_key", "KEY"),
		map[string]any{inner: map[string]any{"ref": "KEY", "source": types.SecretVault}, "USER": map[string]any{"ref": "U"}},
		map[string]any{"tav": map[string]any{"source": "fixture/tav", "config": map[string]any{"api_key": "{{secrets." + inner + "}}", "other": "x"}}}))
	if err != nil {
		t.Fatal(err)
	}
	var r map[string]json.RawMessage
	_ = json.Unmarshal(out, &r)
	var secs map[string]json.RawMessage
	_ = json.Unmarshal(r["secrets"], &secs)
	if _, ok := secs[inner]; ok {
		t.Fatal("derived secret not stripped")
	}
	if _, ok := secs["USER"]; !ok {
		t.Fatal("unrelated secret dropped")
	}
}

func TestStripDerivedCollision(t *testing.T) {
	inner := envSecretName("tav", "api_key")
	if _, err := StripEnvironmentDerived(spec(t, env("tav", "api_key", "NEW"),
		map[string]any{inner: map[string]any{"ref": "OLD"}}, nil)); err == nil {
		t.Fatal("mismatched ref silently stripped")
	}
	if _, err := StripEnvironmentDerived(spec(t, env("tav", "api_key", "KEY"),
		map[string]any{inner: map[string]any{"value": "plaintext"}}, nil)); err == nil {
		t.Fatal("inline value silently stripped")
	}
}

func TestStripDerivedKeepsUndeclaredAlias(t *testing.T) {
	other := envSecretName("other", "k")
	out, err := StripEnvironmentDerived(spec(t, env("tav", "api_key", "KEY"), nil,
		map[string]any{"other": map[string]any{"source": "fixture/o", "config": map[string]any{"k": "{{secrets." + other + "}}"}}}))
	if err != nil {
		t.Fatal(err)
	}
	var r struct {
		Cap struct {
			Ext map[string]struct {
				Config map[string]string `json:"config"`
			} `json:"ext"`
		} `json:"capabilities"`
	}
	_ = json.Unmarshal(out, &r)
	if r.Cap.Ext["other"].Config["k"] != "{{secrets."+other+"}}" {
		t.Fatalf("undeclared alias placeholder stripped: %v", r.Cap.Ext["other"].Config)
	}
}
