package secrets

import (
	"encoding/base64"
	"net/url"
	"strings"
	"testing"

	"calcside/internal/hostmatch"
)

func testCipher(t *testing.T) *Cipher {
	t.Helper()
	c, err := NewCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCipherRoundtrip(t *testing.T) {
	c := testCipher(t)
	ct, err := c.Seal([]byte("s3cr3t"), "usr_1/TOKEN")
	if err != nil {
		t.Fatal(err)
	}
	pt, err := c.Open(ct, "usr_1/TOKEN")
	if err != nil || string(pt) != "s3cr3t" {
		t.Fatalf("roundtrip: %v %q", err, pt)
	}
	// wrong AAD fails
	if _, err := c.Open(ct, "usr_1/OTHER"); err == nil {
		t.Fatal("expected AAD failure")
	}
	if _, err := c.Open(ct, "usr_2/TOKEN"); err == nil {
		t.Fatal("expected AAD failure (other user)")
	}
	// tamper fails
	ct[len(ct)-1] ^= 0xff
	if _, err := c.Open(ct, "usr_1/TOKEN"); err == nil {
		t.Fatal("expected tamper failure")
	}
}

func TestNewCipherBadKey(t *testing.T) {
	if _, err := NewCipher("not base64!!!"); err == nil {
		t.Fatal("expected base64 error")
	}
	if _, err := NewCipher(base64.StdEncoding.EncodeToString(make([]byte, 16))); err == nil {
		t.Fatal("expected length error")
	}
}

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

func TestWipe(t *testing.T) {
	s := NewSet()
	s.Add("W", []byte("live"), rules(t, "x.com"))
	stored := s.Lookup("W").Value()
	s.Wipe()
	if len(s.Names()) != 0 || string(stored) != "\x00\x00\x00\x00" || s.Lookup("W") != nil {
		t.Fatal("wipe failed")
	}
}
