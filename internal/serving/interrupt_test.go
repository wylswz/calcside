package serving

import "testing"

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
