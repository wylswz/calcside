package net

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	gonet "net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"calcside/internal/capability"
	"calcside/internal/hostmatch"
	"calcside/internal/runtime"
	"calcside/internal/types"
)

func newClient(t *testing.T, cfg Config) *client {
	f := factory{}
	v, err := f.Validate(mustJSON(t, cfg), capability.ServerLimits{})
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
	c.allowPrivate = false
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

func TestEmptyAllowlistAllowsAny(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hello")
	}))
	defer ts.Close()
	c := newClient(t, Config{}) // empty allow_hosts = any host
	r, err := c.do(context.Background(), "GET", ts.URL+"/x", "", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != 200 || r.Body != "hello" {
		t.Fatalf("bad response %+v", r)
	}
}

func TestEmptyAllowlistStillBlocksPrivateIPs(t *testing.T) {
	// Empty allow_hosts opens the host check but the SSRF dialer still
	// refuses private/reserved addresses when the built-in policy is selected.
	c := &client{cfg: Config{Methods: []types.HTTPMethod{types.MethodGet}}, allowPrivate: false}
	c.hc = c.newHTTPClient()
	for _, u := range []string{"http://127.0.0.1:8080/", "http://localhost/"} {
		_, err := c.do(context.Background(), "GET", u, "", nil, "")
		if err == nil || !strings.Contains(err.Error(), "disallowed") {
			t.Fatalf("expected SSRF block for %s, got %v", u, err)
		}
	}
}

func TestEmptyAllowlistRedirectFollowed(t *testing.T) {
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "B")
	}))
	defer b.Close()
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, b.URL, http.StatusFound)
	}))
	defer a.Close()
	c := newClient(t, Config{})
	r, err := c.do(context.Background(), "GET", a.URL, "", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Body != "B" {
		t.Fatalf("redirect to another host not followed: %q", r.Body)
	}
}

// --net-allow-cidrs exempts addresses from private/reserved blocking in
// both the IP-literal and resolved-address dialer paths.
func TestAllowCIDRs(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hi")
	}))
	defer ts.Close()
	_, v4cidr, err := gonet.ParseCIDR("127.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	_, v6cidr, err := gonet.ParseCIDR("::1/128")
	if err != nil {
		t.Fatal(err)
	}
	cidrs := []*gonet.IPNet{v4cidr, v6cidr}
	cfg := Config{Methods: []types.HTTPMethod{types.MethodGet}}
	_, port := hostPort(ts)

	mk := func(cidrs []*gonet.IPNet) *client {
		c := &client{cfg: cfg, allowPrivate: false, allowCIDRs: cidrs}
		c.hc = c.newHTTPClient()
		return c
	}

	// Without the exemption, both literal and resolved loopback are blocked.
	for _, u := range []string{ts.URL, "http://localhost:" + port + "/"} {
		if _, err := mk(nil).do(context.Background(), "GET", u, "", nil, ""); err == nil ||
			!strings.Contains(err.Error(), "disallowed") {
			t.Fatalf("expected SSRF block for %s, got %v", u, err)
		}
	}
	// With the exemption both paths dial fine.
	for _, u := range []string{ts.URL, "http://localhost:" + port + "/"} {
		r, err := mk(cidrs).do(context.Background(), "GET", u, "", nil, "")
		if err != nil {
			t.Fatalf("%s: %v", u, err)
		}
		if r.Body != "hi" {
			t.Fatalf("%s: bad body %q", u, r.Body)
		}
	}

	// The IP-literal allow_hosts Validate check defers address policy to dialing.
	f := factory{}
	b, _ := json.Marshal(Config{AllowHosts: []string{"127.0.0.1:" + port}})
	if _, err := f.Validate(b, capability.ServerLimits{NetAllowCIDRs: cidrs}); err != nil {
		t.Fatalf("literal in allowed CIDR should validate: %v", err)
	}
	if _, err := f.Validate(b, capability.ServerLimits{}); err != nil {
		t.Fatalf("address policy should run at dial time: %v", err)
	}
}

func TestIPLiteralPolicyRunsAtDialTime(t *testing.T) {
	f := factory{}
	for _, host := range []string{"127.0.0.1:8080", "169.254.169.254", "10.0.0.1", "[::1]"} {
		_, err := f.Validate(mustJSON(t, Config{AllowHosts: []string{host}}), capability.ServerLimits{})
		if err != nil {
			t.Fatalf("validate %q: %v", host, err)
		}
		c := &client{cfg: Config{Methods: []types.HTTPMethod{types.MethodGet}}}
		c.hc = c.newHTTPClient()
		_, err = c.do(context.Background(), "GET", "http://"+host, "", nil, "")
		var denied *capability.DeniedError
		if !errors.As(err, &denied) {
			t.Fatalf("expected policy denial for %q, got %v", host, err)
		}
	}
	// and accepted when allowed
	_, err := f.Validate(mustJSON(t, Config{AllowHosts: []string{"127.0.0.1:8080"}}), capability.ServerLimits{})
	if err != nil {
		t.Fatalf("127.0.0.1 should validate regardless of selected policies: %v", err)
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

func TestPrompt(t *testing.T) {
	cfg, err := factory{}.Validate(json.RawMessage(`{"allow_hosts":["a.com","b.com"],"methods":["GET","POST"]}`), capability.ServerLimits{})
	if err != nil {
		t.Fatal(err)
	}
	p := factory{}.Prompt(cfg)
	for _, want := range []string{"a.com, b.com", "GET, POST", "{status, headers, body}", "{{secrets.NAME}}", "[REDACTED:NAME]",
		"Accept-Encoding", "net.get(", "net.post(", "net.request("} {
		if !strings.Contains(p, want) {
			t.Fatalf("missing %q in:\n%s", want, p)
		}
	}
}

func TestUnselectedNetworkPolicyAllowsDNSLoopback(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") }))
	defer ts.Close()
	_, port := hostPort(ts)
	c := newClient(t, Config{})
	defer c.hc.CloseIdleConnections()
	r, err := c.do(context.Background(), "GET", "http://localhost:"+port, "", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Body != "ok" {
		t.Fatalf("body: %q", r.Body)
	}
}

func TestNetworkPolicyChecksAllDNSResults(t *testing.T) {
	for _, ips := range [][]gonet.IPAddr{
		{{IP: gonet.ParseIP("8.8.8.8")}, {IP: gonet.ParseIP("127.0.0.1")}},
		{{IP: gonet.ParseIP("::1")}, {IP: gonet.ParseIP("8.8.8.8")}},
		{{IP: gonet.ParseIP("::ffff:10.0.0.1")}},
	} {
		c := &client{}
		var denied *capability.DeniedError
		if err := c.checkAddresses("test.example", ips); !errors.As(err, &denied) || !strings.Contains(denied.Reason, runtime.BlockPrivateNetworkPolicy) {
			t.Fatalf("DNS results %v: %v", ips, err)
		}
		c.allowPrivate = true
		if err := c.checkAddresses("test.example", ips); err != nil {
			t.Fatal(err)
		}
	}
	if err := (&client{}).checkAddresses("public.example", []gonet.IPAddr{{IP: gonet.ParseIP("8.8.8.8")}}); err != nil {
		t.Fatal(err)
	}
}

func TestNetworkPolicyChecksRedirectAddresses(t *testing.T) {
	var hits int
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++; fmt.Fprint(w, "ok") }))
	defer target.Close()
	_, port := hostPort(target)
	_, exempt, _ := gonet.ParseCIDR("127.0.0.1/32")
	for _, destination := range []string{"http://127.0.0.2:" + port, "http://redirect.invalid:" + port} {
		start := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination, http.StatusFound) }))
		c := newClient(t, Config{})
		c.allowPrivate, c.allowCIDRs = false, []*gonet.IPNet{exempt}
		c.resolve = func(context.Context, string) ([]gonet.IPAddr, error) {
			return []gonet.IPAddr{{IP: gonet.ParseIP("127.0.0.2")}}, nil
		}
		c.hc = c.newHTTPClient()
		_, err := c.do(context.Background(), "GET", start.URL, "", nil, "")
		var denied *capability.DeniedError
		if !errors.As(err, &denied) {
			t.Fatalf("redirect %s: want policy denial, got %v", destination, err)
		}
		c.hc.CloseIdleConnections()
		start.Close()
	}
	if hits != 0 {
		t.Fatalf("blocked redirect reached target %d times", hits)
	}
}

func TestNetworkPolicyRechecksDNSAndPinsConnections(t *testing.T) {
	var hits, lookups atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Connection", "close")
		fmt.Fprint(w, "ok")
	}))
	defer target.Close()
	_, port := hostPort(target)
	_, exempt, _ := gonet.ParseCIDR("127.0.0.1/32")
	c := &client{cfg: Config{Methods: []types.HTTPMethod{types.MethodGet}}, allowCIDRs: []*gonet.IPNet{exempt}}
	c.resolve = func(context.Context, string) ([]gonet.IPAddr, error) {
		ip := "127.0.0.1"
		if lookups.Add(1) > 1 {
			ip = "10.0.0.1"
		}
		return []gonet.IPAddr{{IP: gonet.ParseIP(ip)}}, nil
	}
	c.hc = c.newHTTPClient()
	defer c.hc.CloseIdleConnections()
	url := "http://rebind.invalid:" + port
	if _, err := c.do(context.Background(), "GET", url, "", nil, ""); err != nil {
		t.Fatal(err)
	}
	_, err := c.do(context.Background(), "GET", url, "", nil, "")
	var denied *capability.DeniedError
	if !errors.As(err, &denied) || hits.Load() != 1 || lookups.Load() != 2 {
		t.Fatalf("rebinding: hits=%d lookups=%d err=%v", hits.Load(), lookups.Load(), err)
	}
}
