package vaultcipher

import (
	"encoding/base64"
	"testing"
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
