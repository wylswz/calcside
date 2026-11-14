package serving

import (
	"testing"

	"calcside/internal/module"
)

func TestUsesInterrupt(t *testing.T) {
	for src, want := range map[string]bool{
		"def main(i):\n    return i\n":                            false,
		"def main(i):\n    r = interrupt.new()\n    return r\n":   true,
		"def main(i):\n    s = 'interrupt.new()'\n    return s\n": false,
		"def main(i):\n    return i\n# interrupt.new()\n":         false,
		"def main(i)\n": true,
	} {
		if got := UsesInterrupt(map[string][]byte{"main.star": []byte(src)}); got != want {
			t.Errorf("%q: got %v want %v", src, got, want)
		}
	}
}

func TestUsesModuleInterrupt(t *testing.T) {
	// interrupt referenced only inside a module's .star file counts.
	mods := map[string]module.Tree{
		"m": {Name: "m", Version: 1, Files: map[string][]byte{
			"u.star": []byte(`x = interrupt.claim("k")`),
		}},
	}
	if !UsesModuleInterrupt(map[string][]byte{"main.star": []byte(`def main(i): return {}`)}, mods) {
		t.Fatal("module interrupt not detected")
	}
	// The word inside module.yaml (YAML, not code) must NOT mint slots.
	modsYaml := map[string]module.Tree{
		"m": {Name: "m", Version: 1, Files: map[string][]byte{
			"module.yaml": []byte("description: uses interrupt for waits\n"),
			"u.star":      []byte("x = 1"),
		}},
	}
	if UsesModuleInterrupt(map[string][]byte{"main.star": []byte(`def main(i): return {}`)}, modsYaml) {
		t.Fatal("module.yaml mention minted a slot")
	}
}
