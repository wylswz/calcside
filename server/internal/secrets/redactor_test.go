package secrets

import (
	"strings"
	"testing"
)

func redactorFor(t *testing.T, pairs map[string]string, extras ...string) *StreamRedactor {
	t.Helper()
	s := NewSet()
	for n, v := range pairs {
		s.Add(n, []byte(v), nil)
	}
	if len(extras) > 0 {
		s = s.WithRedactions(func() [][]byte {
			var b [][]byte
			for _, e := range extras {
				b = append(b, []byte(e))
			}
			return b
		}()...)
	}
	return s.NewStreamRedactor(0)
}

// splitAll pushes payload at every possible split and asserts the
// concatenated output is identical to the one-shot redaction.
func splitAll(t *testing.T, s *Set, payload []byte, want string) {
	t.Helper()
	for split := 0; split <= len(payload); split++ {
		r := s.NewStreamRedactor(0)
		a, err := r.Push(payload[:split], false)
		if err != nil {
			t.Fatalf("split %d: %v", split, err)
		}
		b, err := r.Push(payload[split:], true)
		if err != nil {
			t.Fatalf("split %d eof: %v", split, err)
		}
		if got := string(a) + string(b); got != want {
			t.Fatalf("split %d: %q != %q", split, got, want)
		}
	}
}

func TestRedactorEveryBoundary(t *testing.T) {
	s := NewSet()
	s.Add("K", []byte("sk-live-9f2e"), nil)
	payload := []byte("pre sk-li" + "ve-9f2e post")
	splitAll(t, s, payload, "pre [REDACTED:K] post")
}

func TestRedactorVariants(t *testing.T) {
	s := NewSet()
	s.Add("K", []byte("sec ret&x"), nil)
	for _, v := range variants([]byte("sec ret&x")) {
		for split := 0; split <= len(v); split++ {
			r := s.NewStreamRedactor(0)
			a, _ := r.Push([]byte("a"+v[:split]), false)
			b, err := r.Push([]byte(v[split:]+"b"), true)
			if err != nil {
				t.Fatal(err)
			}
			if got := string(a) + string(b); got != "a[REDACTED:K]b" {
				t.Fatalf("variant %q split %d -> %q", v, split, got)
			}
		}
	}
}

func TestRedactorFalsePrefixEOF(t *testing.T) {
	s := NewSet()
	s.Add("K", []byte("abcdef"), nil)
	r := s.NewStreamRedactor(0)
	out, err := r.Push([]byte("abc"), false)
	if err != nil || len(out) != 0 {
		t.Fatalf("prefix retained wrong: %q %v", out, err)
	}
	out, err = r.Push([]byte("xyz"), true)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "abcxyz" {
		t.Fatalf("false prefix: %q", out)
	}
}

func TestRedactorImmediateFlushLongSecret(t *testing.T) {
	s := NewSet()
	s.Add("K", []byte(strings.Repeat("Z", 16<<10)), nil)
	r := s.NewStreamRedactor(0)
	out, err := r.Push([]byte("hello world"), false)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "hello world" {
		t.Fatalf("long secret delayed small output: %q", out)
	}
}

func TestRedactorAdjacentUTF8(t *testing.T) {
	s := NewSet()
	s.Add("K", []byte("café-secret"), nil)
	payload := []byte("héllo café-secret wörld")
	splitAll(t, s, payload, "héllo [REDACTED:K] wörld")
}

func TestRedactorExtras(t *testing.T) {
	s := NewSet().WithRedactions([]byte("tok123"))
	splitAll(t, s, []byte("x tok123 y"), "x [REDACTED] y")
}

func TestRedactorOutputCap(t *testing.T) {
	s := NewSet()
	s.Add("K", []byte("s"), nil) // one-byte secret expands to marker
	r := s.NewStreamRedactor(10)
	if _, err := r.Push([]byte("s s s s s s"), true); err == nil {
		t.Fatal("expansion cap not enforced")
	}
}

func TestRedactorOverlappingPrefixes(t *testing.T) {
	s := NewSet()
	s.Add("K", []byte("abab"), nil)
	r := s.NewStreamRedactor(0)
	out, err := r.Push([]byte("ababab"), true)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "[REDACTED:K]ab" {
		t.Fatalf("overlap: %q", out)
	}
}

// A short secret must never mask a longer variant split across chunks:
// SHORT=abc, LONG=abcdef — every split yields the LONG marker.
func TestRedactorShortLongInvariant(t *testing.T) {
	s := NewSet()
	s.Add("SHORT", []byte("abc"), nil)
	s.Add("LONG", []byte("abcdef"), nil)
	for split := 0; split <= len("abcdef"); split++ {
		r := s.NewStreamRedactor(0)
		a, _ := r.Push([]byte("xabcdefy"[:split]), false)
		b, err := r.Push([]byte("xabcdefy"[split:]), true)
		if err != nil {
			t.Fatal(err)
		}
		if got := string(a) + string(b); got != "x[REDACTED:LONG]y" {
			t.Fatalf("split %d: %q", split, got)
		}
	}
	// A false longer-prefix at EOF still emits the SHORT marker.
	r := s.NewStreamRedactor(0)
	out, err := r.Push([]byte("zabc"), true)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "z[REDACTED:SHORT]" {
		t.Fatalf("false prefix EOF: %q", out)
	}
}
