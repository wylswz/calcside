package net

import (
	"context"
	"encoding/json"
	"fmt"
	gonet "net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"calcside/internal/capability"
	"calcside/internal/hostmatch"
	"calcside/internal/types"
)

func newClient(t *testing.T, cfg Config) *client {
	f := factory{}
	v, err := f.Validate(mustJSON(t, cfg), capability.ServerLimits{NetAllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	c := &client{cfg: v.(validated).Config, rules: mustRules(t, v.(validated).Config), allowPrivate: true}
	c.hc = c.newHTTPClient()
	return c
}

func mustJSON(t *testing.T, cfg Config) []byte {
	t.Helper()
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func mustRules(t *testing.T, cfg Config) []hostmatch.Rule {
	t.Helper()
	out, err := hostmatch.ParseAll(cfg.AllowHosts)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func hostPort(ts *httptest.Server) (host, port string) {
	// ts.URL like http://127.0.0.1:PORT or https://...
	s := strings.TrimPrefix(strings.TrimPrefix(ts.URL, "http://"), "https://")
	i := strings.LastIndex(s, ":")
	return s[:i], s[i+1:]
}

func TestIPLiteralAllowlistWithPort(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hello")
	}))
	defer ts.Close()
	host, port := hostPort(ts)
	c := newClient(t, Config{AllowHosts: []string{host + ":" + port}})
	r, err := c.do(context.Background(), "GET", ts.URL+"/x", "", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != 200 || r.Body != "hello" {
		t.Fatalf("bad response %+v", r)
	}
}

func TestIPLiteralWithoutPortDeniedNonDefaultPort(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer ts.Close()
	host, _ := hostPort(ts)
	c := newClient(t, Config{AllowHosts: []string{host}}) // no port => only 80/443
	_, err := c.do(context.Background(), "GET", ts.URL, "", nil, "")
	if err == nil || !strings.Contains(err.Error(), "port") {
		t.Fatalf("expected port denial, got %v", err)
	}
}

func TestLocalhostResolvingToLoopbackBlocked(t *testing.T) {
	// "localhost" is a domain name resolving to 127.0.0.1 — must be refused
	// by the pinning dialer even though allowlisted.
	c := newClient(t, Config{AllowHosts: []string{"localhost:80"}})
	_, err := c.do(context.Background(), "GET", "http://localhost/", "", nil, "")
	if err == nil {
		t.Fatal("expected SSRF block for localhost")
	}
}

func TestHostNotInAllowlistDenied(t *testing.T) {
	c := newClient(t, Config{AllowHosts: []string{"example.com"}})
	_, err := c.do(context.Background(), "GET", "http://other.com/", "", nil, "")
	if err == nil || !strings.Contains(err.Error(), "allow_hosts") {
		t.Fatalf("expected allowlist denial, got %v", err)
	}
}

func TestWildcardMatch(t *testing.T) {
	c := newClient(t, Config{AllowHosts: []string{"*.example.com"}})
	rule := c.rules[0]
	if !rule.MatchesHost("api.example.com") || !rule.MatchesHost("a.b.example.com") {
		t.Fatal("wildcard should match subdomains")
	}
	if rule.MatchesHost("example.com") {
		t.Fatal("wildcard must not match bare domain")
	}
	if rule.MatchesHost("notexample.com") {
		t.Fatal("wildcard must not match unrelated domain")
	}
}

func TestRedirectToNonAllowlistedBlocked(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redir" {
			http.Redirect(w, r, "http://example.org/", http.StatusFound)
			return
		}
		fmt.Fprint(w, "ok")
	}))
	defer ts.Close()
	host, port := hostPort(ts)
	c := newClient(t, Config{AllowHosts: []string{host + ":" + port}})
	_, err := c.do(context.Background(), "GET", ts.URL+"/redir", "", nil, "")
	if err == nil || !strings.Contains(err.Error(), "redirect") && !strings.Contains(err.Error(), "allow_hosts") {
		t.Fatalf("expected redirect block, got %v", err)
	}
}

func TestResponseCap(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(strings.Repeat("x", 100)))
	}))
	defer ts.Close()
	host, port := hostPort(ts)
	c := newClient(t, Config{AllowHosts: []string{host + ":" + port}, MaxResponseBytes: 10})
	_, err := c.do(context.Background(), "GET", ts.URL, "", nil, "")
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected response cap error, got %v", err)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer ts.Close()
	host, port := hostPort(ts)
	c := newClient(t, Config{AllowHosts: []string{host + ":" + port}, Methods: []types.HTTPMethod{types.MethodGet}})
	_, err := c.do(context.Background(), "DELETE", ts.URL, "", nil, "")
	if err == nil || !strings.Contains(err.Error(), "method") {
		t.Fatalf("expected method denial, got %v", err)
	}
}

func TestEmptyAllowlistDeniesAll(t *testing.T) {
	c := newClient(t, Config{})
	_, err := c.do(context.Background(), "GET", "http://example.com/", "", nil, "")
	if err == nil {
		t.Fatal("expected denial with empty allowlist")
	}
}

func TestIPLiteralBlockedWhenPrivateNotAllowed(t *testing.T) {
	f := factory{}
	for _, host := range []string{"127.0.0.1:8080", "169.254.169.254", "10.0.0.1", "[::1]"} {
		_, err := f.Validate(mustJSON(t, Config{AllowHosts: []string{host}}), capability.ServerLimits{})
		if err == nil {
			t.Errorf("expected rejection of %q without NetAllowPrivate", host)
		}
	}
	// and accepted when allowed
	_, err := f.Validate(mustJSON(t, Config{AllowHosts: []string{"127.0.0.1:8080"}}), capability.ServerLimits{NetAllowPrivate: true})
	if err != nil {
		t.Fatalf("127.0.0.1 should be allowed with NetAllowPrivate: %v", err)
	}
}

func TestBlockedIPRanges(t *testing.T) {
	for _, s := range []string{"0.0.0.1", "192.0.0.1", "198.18.0.1", "255.255.255.255", "240.0.0.1", "100.64.0.1", "::ffff:10.0.0.1", "64:ff9b::8.8.8.8", "2002::1"} {
		ip := gonet.ParseIP(s)
		if ip == nil {
			t.Fatalf("bad ip %s", s)
		}
		if !isBlockedIP(ip) {
			t.Errorf("expected %s blocked", s)
		}
	}
	if isBlockedIP(gonet.ParseIP("8.8.8.8")) {
		t.Error("8.8.8.8 should not be blocked")
	}
}
