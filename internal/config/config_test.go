package config

import (
	"errors"
	"flag"
	"os"
	"strings"
	"testing"
)

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

func TestParseNetAllowCIDRs(t *testing.T) {
	c, err := Parse([]string{"--net-allow-cidrs", "198.18.0.0/15, 10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.NetAllowCIDRs) != 2 || c.NetAllowCIDRs[0].String() != "198.18.0.0/15" {
		t.Fatalf("cidrs: %v", c.NetAllowCIDRs)
	}
	c, err = Parse([]string{"--net-allow-cidrs="})
	if err != nil || len(c.NetAllowCIDRs) != 0 {
		t.Fatalf("empty flag: %v %+v", err, c.NetAllowCIDRs)
	}
	if _, err = Parse([]string{"--net-allow-cidrs", "bogus"}); err == nil {
		t.Fatal("expected invalid CIDR error")
	}
}

func TestParseAdminCredentials(t *testing.T) {
	t.Setenv("CALCSIDE_ADMIN_USERNAME", "")
	t.Setenv("CALCSIDE_ADMIN_PASSWORD", "")
	for _, tc := range []struct {
		name  string
		args  []string
		valid bool
	}{
		{"disabled", nil, true},
		{"enabled", []string{"--admin-username=admin", "--admin-password=test-password"}, true},
		{"username-only", []string{"--admin-username=admin"}, false},
		{"password-only", []string{"--admin-password=test-password"}, false},
		{"invalid-username", []string{"--admin-username=admin:root", "--admin-password=test-password"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(tc.args)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%t, err=%v", tc.valid, err)
			}
		})
	}
	t.Setenv("CALCSIDE_ADMIN_USERNAME", "env-user")
	t.Setenv("CALCSIDE_ADMIN_PASSWORD", "env-password")
	cfg, err := Parse(nil)
	if err != nil || cfg.AdminUsername != "env-user" || cfg.AdminPassword != "env-password" {
		t.Fatalf("environment not applied: %v", err)
	}
	cfg, err = Parse([]string{"--admin-username=flag-user", "--admin-password=flag-password"})
	if err != nil || cfg.AdminUsername != "flag-user" || cfg.AdminPassword != "flag-password" {
		t.Fatalf("flags did not override environment: %v", err)
	}
}

func TestAdminPasswordNotInHelp(t *testing.T) {
	t.Setenv("CALCSIDE_ADMIN_USERNAME", "admin")
	t.Setenv("CALCSIDE_ADMIN_PASSWORD", "test-password-not-for-help")
	out, err := os.CreateTemp(t.TempDir(), "help")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	stderr := os.Stderr
	os.Stderr = out
	defer func() { os.Stderr = stderr }()
	_, err = Parse([]string{"--help"})
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("expected help, got %v", err)
	}
	data, err := os.ReadFile(out.Name())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "test-password-not-for-help") {
		t.Fatal("Administrator password is exposed by --help")
	}
}

func TestPublicOriginFlags(t *testing.T) {
	_, err := Parse([]string{"--console-origin=https://console.example.com", "--artifact-preview-base-url=https://reports.example.net"})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRemovedPublicOriginFlags(t *testing.T) {
	for _, name := range []string{"base-url", "artifact-console-origin"} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]string{"--" + name + "=https://console.example.com"}); err == nil {
				t.Fatal("obsolete origin flag is still accepted")
			}
		})
	}
}

func TestPublicOriginConfiguration(t *testing.T) {
	for _, key := range []string{"CALCSIDE_CONSOLE_ORIGIN", "CALCSIDE_ARTIFACT_PREVIEW_BASE_URL", "CALCSIDE_BASE_URL", "CALCSIDE_ARTIFACT_CONSOLE_ORIGIN"} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := Parse(nil)
	if err != nil || cfg.ConsoleOrigin != "http://localhost:8080" || cfg.ArtifactPreviewBaseURL != "" {
		t.Fatalf("unexpected public origin defaults: %v", err)
	}
	t.Setenv("CALCSIDE_CONSOLE_ORIGIN", "https://console.example.com")
	t.Setenv("CALCSIDE_ARTIFACT_PREVIEW_BASE_URL", "https://reports.example.net")
	cfg, err = Parse(nil)
	if err != nil || cfg.ConsoleOrigin != "https://console.example.com" || cfg.ArtifactPreviewBaseURL != "https://reports.example.net" {
		t.Fatalf("public origin environment not applied: %v", err)
	}
	cfg, err = Parse([]string{"--console-origin=https://OTHER.example.com:8443/", "--artifact-preview-base-url=https://other.example.net"})
	if err != nil || cfg.ConsoleOrigin != "https://other.example.com:8443" || cfg.ArtifactPreviewBaseURL != "https://other.example.net" {
		t.Fatalf("public origin flags not applied: %v", err)
	}
}

func TestRemovedPublicOriginEnvironment(t *testing.T) {
	for _, name := range []string{"CALCSIDE_BASE_URL", "CALCSIDE_ARTIFACT_CONSOLE_ORIGIN"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, "https://obsolete.example.com")
			_, err := Parse([]string{"--console-origin=https://console.example.com"})
			if err == nil || !strings.Contains(err.Error(), name) || !strings.Contains(err.Error(), "CALCSIDE_CONSOLE_ORIGIN") {
				t.Fatalf("expected explicit migration error: %v", err)
			}
		})
	}
}

func TestConsoleOriginValidation(t *testing.T) {
	for _, origin := range []string{"", "console.example.com", "ftp://console.example.com", "https://user:password@console.example.com", "https://console.example.com/path", "https://console.example.com?", "https://console.example.com?q=1", "https://console.example.com#fragment", "https://console.example.com:99999"} {
		if _, err := Parse([]string{"--console-origin=" + origin}); err == nil {
			t.Errorf("accepted invalid console origin %q", origin)
		}
	}
}
