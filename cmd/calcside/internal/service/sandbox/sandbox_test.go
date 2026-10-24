package sandbox

import (
	"strings"
	"testing"
	"unicode/utf8"
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
