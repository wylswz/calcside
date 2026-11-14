package catalog

import (
	"strings"
	"testing"

	"calcside/internal/capability"
	"calcside/internal/policy"
	"calcside/internal/types"
)

func TestEditorInputMatchesPolicy(t *testing.T) {
	fields := map[string]bool{}
	for _, symbol := range EditorSymbols() {
		if strings.HasPrefix(symbol.Name, "input.") {
			fields[symbol.Name] = true
		}
	}
	in := policy.InputFor(types.PhaseAfter, &capability.Call{Args: map[string]any{}}, &capability.Result{Meta: map[string]any{}})
	var visit func(string, map[string]any)
	visit = func(prefix string, object map[string]any) {
		for key, value := range object {
			name := prefix + "." + key
			if !fields[name] {
				t.Errorf("missing schema field %s", name)
			}
			delete(fields, name)
			if child, ok := value.(map[string]any); ok {
				visit(name, child)
			}
		}
	}
	visit("input", in)
	if len(fields) > 0 {
		t.Fatalf("schema fields missing from actual input: %v", fields)
	}
}

func TestEditorOnlyAllowedBuiltins(t *testing.T) {
	allowed := map[string]bool{}
	for _, builtin := range policy.UserCapabilities().Builtins {
		allowed[builtin.Name] = true
	}
	for _, symbol := range EditorSymbols() {
		if symbol.Kind == "function" && !allowed[symbol.Name] {
			t.Errorf("unexpected builtin %s", symbol.Name)
		}
		for _, blocked := range []string{"http.send", "opa.runtime", "net.lookup_ip_addr", "trace", "print"} {
			if symbol.Name == blocked {
				t.Errorf("blocked builtin %s", blocked)
			}
		}
	}
}
