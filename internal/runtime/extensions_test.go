package runtime

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestExtensionRequirements(t *testing.T) {
	sum := "h1:" + strings.Repeat("A", 43) + "="
	raw := json.RawMessage(`{"capabilities":{"ext":{"x":{"source":"fixtures/llm","sum":"` + sum +
		`","config":{"n":9007199254740993},"llm":[{"source":"fixtures/provider","sum":"` + sum + `"}]}}}}`)
	refs, err := ExtensionRequirements(raw)
	if err != nil || len(refs) != 2 || refs[0].Source != "fixtures/llm" || refs[1].Source != "fixtures/provider" {
		t.Fatalf("provider dependencies omitted: %+v %v", refs, err)
	}
	if _, err := ExtensionRequirements(json.RawMessage(`{"capabilities":{"ext":{"x":{"source":"fixtures/unpinned"}}}}`)); err == nil {
		t.Fatal("unpinned extension accepted")
	}
	if ValidExtensionSum("h1:forged") {
		t.Fatal("invalid integrity hash accepted")
	}
}
