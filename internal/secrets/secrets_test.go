package secrets

import (
	"context"
	"encoding/base64"
	"net/url"
	"strings"
	"testing"

	"calcside/internal/hostmatch"
)

func rules(t *testing.T, entries ...string) []hostmatch.Rule {
	t.Helper()
	rs, err := hostmatch.ParseAll(entries)
	if err != nil {
		t.Fatal(err)
	}
	return rs
}

func TestRedactAllEncodings(t *testing.T) {
	s := NewSet()
	val := "tok secret+/=?x"
	s.Add("T", []byte(val), rules(t, "api.x.com"))
	b64 := base64.StdEncoding.EncodeToString([]byte(val))
	rawURL := base64.RawURLEncoding.EncodeToString([]byte(val))
	in := strings.Join([]string{
		"a " + val + " b",
		b64,
		rawURL,
		url.QueryEscape(val),
		url.PathEscape(val),
		`\"tok`[1:], // sanity: the json-escaped form differs only if escapes exist
	}, " | ")
	out := s.Redact(in)
	for _, v := range []string{val, b64, rawURL, url.QueryEscape(val), url.PathEscape(val)} {
		if strings.Contains(out, v) {
			t.Fatalf("Redact left %q in %q", v, out)
		}
	}
	if !strings.Contains(out, "[REDACTED:T]") {
		t.Fatalf("expected marker, got %q", out)
	}
}

func TestRedactJSONEscaped(t *testing.T) {
	s := NewSet()
	s.Add("J", []byte("a\"b\\c"), rules(t, "x.com"))
	out := s.Redact(`{"k":"a\"b\\c"}`)
	if strings.Contains(out, `a\"b\\c`) || strings.Contains(out, "a\"b\\c") {
		t.Fatalf("json-escaped form not redacted: %q", out)
	}
}

func TestExpandAndRefs(t *testing.T) {
	s := NewSet()
	s.Add("A", []byte("va"), rules(t, "x.com"))
	s.Add("B", []byte("vb"), rules(t, "y.com"))
	out, names, err := s.Expand("u={{secrets.A}} x={{ secrets.B }}")
	if err != nil || out != "u=va x=vb" {
		t.Fatalf("Expand: %v %q", err, out)
	}
	if len(names) != 2 || names[0] != "A" || names[1] != "B" {
		t.Fatalf("names: %v", names)
	}
	if got := Refs("{{secrets.Z}} and {{  secrets.A }}"); len(got) != 2 {
		t.Fatalf("Refs: %v", got)
	}
	_, _, err = s.Expand("{{secrets.NOPE}}")
	if err == nil || !strings.Contains(err.Error(), `unknown secret "NOPE"`) {
		t.Fatalf("expected unknown secret error, got %v", err)
	}
	if strings.Contains(err.Error(), "va") || strings.Contains(err.Error(), "vb") {
		t.Fatal("error leaked a value")
	}
}

func TestUnrestrictedDomains(t *testing.T) {
	rs, err := ValidateDomains(nil)
	if err != nil || rs != nil {
		t.Fatalf("empty domains: %v %v", rs, err)
	}
	s := NewSet()
	s.Add("U", []byte("v"), nil)
	if !s.Lookup("U").Allows("anything.example", "443") {
		t.Fatal("unrestricted secret should allow any host")
	}
	if s.Lookup("U").Allows("127.0.0.1", "1") == false {
		t.Fatal("unrestricted secret should allow IP hosts")
	}
	if got := s.Domains("U"); len(got) != 0 {
		t.Fatalf("Domains of unrestricted: %v", got)
	}
}

func TestWipe(t *testing.T) {
	s := NewSet()
	s.Add("W", []byte("live"), rules(t, "x.com"))
	stored := s.Lookup("W").Value()
	s.Wipe()
	if len(s.Names()) != 0 || string(stored) != "\x00\x00\x00\x00" || s.Lookup("W") != nil {
		t.Fatal("wipe failed")
	}
}

func TestRedactBounded(t *testing.T) {
	set := NewSet()
	set.Add("TOKEN", []byte("s3cr3t"), nil)
	for _, text := range []string{"prefix s3cr3t suffix", "czNjcjN0", "not a secret"} {
		got, err := set.RedactBounded(context.Background(), text, 128)
		if err != nil || got != set.Redact(text) {
			t.Fatalf("redaction mismatch: %v", err)
		}
	}
	set = NewSet()
	set.Add("TOKEN", []byte("X"), nil)
	if _, err := set.RedactBounded(context.Background(), strings.Repeat("X", 100000), 100000); err == nil {
		t.Fatal("allowed redaction expansion")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := set.RedactBounded(ctx, "X", 100); err == nil {
		t.Fatal("ignored cancellation")
	}
}
