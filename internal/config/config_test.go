package config

import "testing"

func TestCheckDevAddr(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:8080", "127.0.0.1:8787", "[::1]:8080", "localhost:8080"} {
		if err := CheckDevAddr(addr, false); err != nil {
			t.Fatalf("%s should be allowed: %v", addr, err)
		}
	}
	for _, addr := range []string{":8080", "0.0.0.0:8080", "192.168.1.5:8080", "example.com:8080"} {
		if err := CheckDevAddr(addr, false); err == nil {
			t.Fatalf("%s should be refused", addr)
		}
	}
	// --dev-allow-remote overrides
	for _, addr := range []string{":8080", "0.0.0.0:8080", "example.com:8080"} {
		if err := CheckDevAddr(addr, true); err != nil {
			t.Fatalf("%s with allow-remote should be allowed: %v", addr, err)
		}
	}
}

func TestParseDevFlags(t *testing.T) {
	c, err := Parse([]string{"--dev"})
	if err != nil {
		t.Fatal(err)
	}
	if !c.Dev || c.DevAllowRemote || c.AddrExplicit {
		t.Fatalf("cfg: %+v", c)
	}
	c, err = Parse([]string{"--dev", "--addr", "0.0.0.0:9000", "--dev-allow-remote"})
	if err != nil {
		t.Fatal(err)
	}
	if !c.AddrExplicit || !c.DevAllowRemote {
		t.Fatalf("cfg: %+v", c)
	}
}
