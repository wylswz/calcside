package hostmatch

import "testing"

func pr(t *testing.T, s string) Rule {
	t.Helper()
	r, err := Parse(s)
	if err != nil {
		t.Fatalf("Parse(%q): %v", s, err)
	}
	return r
}

func TestMatchesPorts(t *testing.T) {
	if !pr(t, "a.com").Matches("a.com", "443") || !pr(t, "a.com").Matches("a.com", "80") {
		t.Fatal("no-port rule should allow 80/443")
	}
	if pr(t, "a.com").Matches("a.com", "8443") {
		t.Fatal("no-port rule must not allow other ports")
	}
	if !pr(t, "a.com:8443").Matches("a.com", "8443") {
		t.Fatal("pinned port should match")
	}
	if pr(t, "a.com:8443").Matches("a.com", "443") {
		t.Fatal("pinned port must not allow 443")
	}
	if pr(t, "*.a.com").Matches("a.com", "443") || !pr(t, "*.a.com").Matches("x.a.com", "443") {
		t.Fatal("wildcard semantics")
	}
}

func TestCovers(t *testing.T) {
	cases := []struct {
		outer, inner string
		want         bool
	}{
		{"a.com", "a.com", true},
		{"a.com", "b.com", false},
		{"*.a.com", "x.a.com", true},   // wildcard covers matching exact
		{"*.a.com", "a.com", false},    // wildcard does not cover bare domain
		{"*.a.com", "*.x.a.com", true}, // deeper wildcard covered
		{"*.a.com", "*.a.com", true},
		{"*.x.a.com", "*.a.com", false}, // widening rejected
		{"a.com", "*.a.com", false},     // exact never covers wildcard
		{"a.com:8443", "a.com:8443", true},
		{"a.com:8443", "a.com", false},
		{"a.com", "a.com:8443", false},
		{"*.a.com:8443", "*.a.com:8443", true},
		{"*.a.com:8443", "*.a.com", false},
		{"10.0.0.1", "10.0.0.1", true},
		{"10.0.0.1", "10.0.0.2", false},
	}
	for _, c := range cases {
		if got := Covers(pr(t, c.outer), pr(t, c.inner)); got != c.want {
			t.Errorf("Covers(%q, %q) = %v, want %v", c.outer, c.inner, got, c.want)
		}
	}
}
