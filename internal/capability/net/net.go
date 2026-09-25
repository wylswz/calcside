// Package net implements the "net" capability: HTTP requests gated by a
// host allowlist, with DNS resolution done in-process and connections
// pinned to vetted IPs to resist SSRF and DNS-rebinding.
package net

import (
	"context"
	gocrypto "crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	gonet "net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go.starlark.net/starlark"

	"calcside/internal/capability"
)

// Config configures one instance's net capability.
type Config struct {
	AllowHosts       []string `json:"allow_hosts"`
	Methods          []string `json:"methods"`
	MaxResponseBytes int64    `json:"max_response_bytes"`
	TimeoutMs        int64    `json:"timeout_ms"`
}

const (
	defaultMaxResponse = 10 << 20
	defaultTimeoutMs   = 10000
	maxRedirects       = 5
)

// hostRule is one parsed allowlist entry: exact host or "*.suffix",
// optionally restricted/extended to a specific port.
type hostRule struct {
	host     string // exact, "*.example.com", or IP literal
	port     string // "" means default ports only (80/443)
	wildcard bool
	isIP     bool
}

type client struct {
	cfg          Config
	rules        []hostRule
	allowPrivate bool
	hc           *http.Client
}

func parseRule(entry string) (hostRule, error) {
	r := hostRule{}
	host := entry
	if h, p, err := gonet.SplitHostPort(entry); err == nil {
		host, r.port = h, p
		if p == "" {
			return r, fmt.Errorf("net config: bad port in %q", entry)
		}
	} else if strings.Count(entry, ":") > 1 {
		// bare IPv6 literal
		host = entry
	}
	r.host = strings.ToLower(strings.TrimSpace(host))
	if r.host == "" {
		return r, fmt.Errorf("net config: empty allow_hosts entry")
	}
	if strings.HasPrefix(r.host, "*.") {
		r.wildcard = true
	}
	if gonet.ParseIP(strings.Trim(r.host, "[]")) != nil {
		r.isIP = true
	}
	return r, nil
}

func (r hostRule) matches(host string) bool {
	host = strings.ToLower(host)
	if r.wildcard {
		suffix := strings.TrimPrefix(r.host, "*.")
		// "*.example.com" matches sub.example.com but NOT example.com.
		return strings.HasSuffix(host, "."+suffix)
	}
	return host == r.host
}

// authorize checks method + host + port against config.
func (c *client) authorize(method, rawURL string) (host, port string, rule *hostRule, err error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", "", nil, fmt.Errorf("net: invalid URL %q: %w", rawURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", "", nil, fmt.Errorf("net: unsupported scheme in URL %q", rawURL)
	}
	host = u.Hostname()
	if host == "" {
		return "", "", nil, fmt.Errorf("net: empty host in URL %q", rawURL)
	}
	port = u.Port()

	ok := false
	for _, m := range c.cfg.Methods {
		if strings.EqualFold(m, method) {
			ok = true
			break
		}
	}
	if !ok {
		return "", "", nil, fmt.Errorf("net: method %s not in methods %v", method, c.cfg.Methods)
	}
	if len(c.rules) == 0 {
		return "", "", nil, fmt.Errorf("net: no hosts permitted (empty allow_hosts)")
	}
	for i := range c.rules {
		if c.rules[i].matches(host) {
			rule = &c.rules[i]
			break
		}
	}
	if rule == nil {
		return "", "", nil, fmt.Errorf("net: host %q not in allow_hosts", host)
	}
	effPort := port
	if effPort == "" {
		if u.Scheme == "https" {
			effPort = "443"
		} else {
			effPort = "80"
		}
	}
	if rule.port != "" {
		if effPort != rule.port {
			return "", "", nil, fmt.Errorf("net: port %s not permitted for %q (allowlist entry pins port %s)", effPort, host, rule.port)
		}
	} else if effPort != "80" && effPort != "443" {
		return "", "", nil, fmt.Errorf("net: non-default port %s not permitted for %q", effPort, host)
	}
	return host, port, rule, nil
}

// blockedCIDRs are IPv4 ranges never dialed unless explicitly allowed.
var blockedCIDRs = func() []*gonet.IPNet {
	var out []*gonet.IPNet
	for _, c := range []string{
		"0.0.0.0/8",     // "this host"
		"100.64.0.0/10", // CGNAT
		"192.0.0.0/24",  // IETF protocol assignments
		"198.18.0.0/15", // benchmarking
		"240.0.0.0/4",   // reserved incl. broadcast
		"64:ff9b::/96",  // NAT64
		"2002::/16",     // 6to4
	} {
		_, n, err := gonet.ParseCIDR(c)
		if err == nil {
			out = append(out, n)
		}
	}
	return out
}()

// isBlockedIP reports whether ip is in a range that must never be dialed
// unless the server opts in (SSRF/rebinding protection).
func isBlockedIP(ip gonet.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return true
	}
	// Normalize IPv4-mapped IPv6 before CIDR checks.
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	for _, n := range blockedCIDRs {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// pinningTransport resolves DNS itself, refuses disallowed addresses for
// domain-name hosts, and dials the vetted IP directly.
func (c *client) transport() *http.Transport {
	dialer := &gonet.Dialer{Timeout: c.timeout()}
	return &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (gonet.Conn, error) {
			hostPort, port, err := gonet.SplitHostPort(addr)
			if err != nil {
				return nil, fmt.Errorf("net: bad dial address %q: %w", addr, err)
			}
			if ip := gonet.ParseIP(hostPort); ip != nil {
				// IP-literal hosts must be explicitly opted in via allowlist
				// AND the server must permit private/reserved addresses.
				if !c.allowPrivate && isBlockedIP(ip) {
					return nil, fmt.Errorf("net: dialing disallowed IP %s", ip)
				}
				return dialer.DialContext(ctx, network, addr)
			}
			ips, err := gonet.DefaultResolver.LookupIPAddr(ctx, hostPort)
			if err != nil {
				return nil, fmt.Errorf("net: resolving %q: %w", hostPort, err)
			}
			var lastErr error
			dialed := false
			for _, ia := range ips {
				if isBlockedIP(ia.IP) {
					return nil, fmt.Errorf("net: host %q resolves to disallowed address %s", hostPort, ia.IP)
				}
				conn, derr := dialer.DialContext(ctx, network, gonet.JoinHostPort(ia.IP.String(), port))
				if derr == nil {
					return conn, nil
				}
				lastErr, dialed = derr, true
			}
			if !dialed {
				lastErr = fmt.Errorf("net: no dialable addresses for %q", hostPort)
			}
			return nil, lastErr
		},
		TLSClientConfig: &gocrypto.Config{MinVersion: gocrypto.VersionTLS12},
	}
}

func (c *client) timeout() time.Duration {
	ms := c.cfg.TimeoutMs
	if ms <= 0 {
		ms = defaultTimeoutMs
	}
	return time.Duration(ms) * time.Millisecond
}

// newHTTPClient builds the single client for the instance lifetime.
func (c *client) newHTTPClient() *http.Client {
	tr := c.transport()
	tr.IdleConnTimeout = 30 * time.Second
	tr.MaxIdleConnsPerHost = 4
	tr.ResponseHeaderTimeout = c.timeout()
	tr.Proxy = nil
	return &http.Client{
		Timeout:   c.timeout(),
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("net: too many redirects (max %d)", maxRedirects)
			}
			if _, _, _, err := c.authorize(req.Method, req.URL.String()); err != nil {
				return fmt.Errorf("net: redirect blocked: %w", err)
			}
			return nil
		},
	}
}

// response is what net.request returns.
type response struct {
	Status  int
	Headers map[string]string
	Body    string
}

func (c *client) do(ctx context.Context, method, rawURL, body string, headers map[string]string, contentType string) (*response, error) {
	if _, _, _, err := c.authorize(method, rawURL); err != nil {
		return nil, err
	}
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, rdr)
	if err != nil {
		return nil, fmt.Errorf("net: %w", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	capBytes := c.cfg.MaxResponseBytes
	if capBytes <= 0 {
		capBytes = defaultMaxResponse
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, capBytes+1))
	if err != nil {
		return nil, fmt.Errorf("net: reading response: %w", err)
	}
	if int64(len(data)) > capBytes {
		return nil, fmt.Errorf("net: response body exceeds %d bytes", capBytes)
	}
	hdrs := map[string]string{}
	for k := range resp.Header {
		hdrs[k] = resp.Header.Get(k)
	}
	return &response{Status: resp.StatusCode, Headers: hdrs, Body: string(data)}, nil
}

// --- factory + binding ---

type factory struct{}

// Factory returns the capability.Factory for "net".
func Factory() capability.Factory { return factory{} }

func (factory) Name() string { return "net" }

func (factory) Ops() []capability.OpInfo {
	return []capability.OpInfo{
		{Name: "get", Doc: "HTTP GET; returns {status,headers,body}", Params: []string{"url", "headers"}},
		{Name: "post", Doc: "HTTP POST; returns {status,headers,body}", Params: []string{"url", "body", "headers", "content_type"}},
		{Name: "request", Doc: "HTTP request with arbitrary method", Params: []string{"method", "url", "body", "headers"}},
	}
}

func (factory) ConfigFields() []capability.FieldDoc {
	return []capability.FieldDoc{
		{Name: "allow_hosts", Type: "[]string", Doc: "exact host, *.suffix wildcard, or host:port; IP literals allowed for testing", Default: []string{}},
		{Name: "methods", Type: "[]string", Doc: "permitted HTTP methods", Default: []string{"GET", "POST"}},
		{Name: "max_response_bytes", Type: "int", Doc: "response body cap", Default: defaultMaxResponse},
		{Name: "timeout_ms", Type: "int", Doc: "request timeout", Default: defaultTimeoutMs},
	}
}

func (factory) Validate(raw json.RawMessage, limits capability.ServerLimits) (any, error) {
	cfg := Config{Methods: []string{"GET", "POST"}, MaxResponseBytes: defaultMaxResponse, TimeoutMs: defaultTimeoutMs}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("net config: %w", err)
		}
	}
	for _, h := range cfg.AllowHosts {
		r, err := parseRule(h)
		if err != nil {
			return nil, err
		}
		// IP-literal entries are only meaningful when private addresses are
		// allowed (tests, internal services); reject blocked literals.
		if r.isIP && !limits.NetAllowPrivate {
			if ip := gonet.ParseIP(strings.Trim(r.host, "[]")); ip != nil && isBlockedIP(ip) {
				return nil, fmt.Errorf("net config: IP-literal allow_hosts entry %q in a blocked range (set --net-allow-private to permit)", h)
			}
		}
	}
	if len(cfg.Methods) == 0 {
		cfg.Methods = []string{"GET", "POST"}
	}
	for i := range cfg.Methods {
		cfg.Methods[i] = strings.ToUpper(cfg.Methods[i])
	}
	if cfg.MaxResponseBytes <= 0 {
		cfg.MaxResponseBytes = defaultMaxResponse
	}
	if limits.MaxNetResponseBytes > 0 && cfg.MaxResponseBytes > limits.MaxNetResponseBytes {
		cfg.MaxResponseBytes = limits.MaxNetResponseBytes
	}
	if cfg.TimeoutMs <= 0 {
		cfg.TimeoutMs = defaultTimeoutMs
	}
	if limits.MaxExecTimeout > 0 && cfg.TimeoutMs > limits.MaxExecTimeout.Milliseconds() {
		cfg.TimeoutMs = limits.MaxExecTimeout.Milliseconds()
	}
	return validated{Config: cfg, allowPrivate: limits.NetAllowPrivate}, nil
}

// validated carries the parsed config plus server policy the binding needs.
type validated struct {
	Config
	allowPrivate bool
}

func (factory) New(cfgAny any, gate *capability.Gate) (starlark.Value, io.Closer, error) {
	v, ok := cfgAny.(validated)
	if !ok {
		return nil, nil, fmt.Errorf("net: config must come from Validate")
	}
	c := &client{cfg: v.Config, allowPrivate: v.allowPrivate}
	for _, h := range v.AllowHosts {
		r, err := parseRule(h)
		if err != nil {
			return nil, nil, err
		}
		c.rules = append(c.rules, r)
	}
	c.hc = c.newHTTPClient()
	return bind(c, gate), closer{c}, nil
}

type closer struct{ c *client }

func (cl closer) Close() error {
	cl.c.hc.CloseIdleConnections()
	return nil
}

func unpackStringDict(v starlark.Value) (map[string]string, error) {
	if v == nil || v == starlark.None {
		return nil, nil
	}
	d, ok := v.(*starlark.Dict)
	if !ok {
		return nil, fmt.Errorf("headers must be a dict, got %s", v.Type())
	}
	out := map[string]string{}
	for _, item := range d.Items() {
		k, ok1 := item[0].(starlark.String)
		vv, ok2 := item[1].(starlark.String)
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("headers must map strings to strings")
		}
		out[string(k)] = string(vv)
	}
	return out, nil
}

func respValue(r *response) starlark.Value {
	hd := starlark.NewDict(len(r.Headers))
	for k, v := range r.Headers {
		_ = hd.SetKey(starlark.String(k), starlark.String(v))
	}
	d := starlark.NewDict(3)
	_ = d.SetKey(starlark.String("status"), starlark.MakeInt(r.Status))
	_ = d.SetKey(starlark.String("headers"), hd)
	_ = d.SetKey(starlark.String("body"), starlark.String(r.Body))
	return d
}

func respMeta(r *response) map[string]any {
	return map[string]any{"status": r.Status, "bytes": len(r.Body)}
}

// extractHostPort parses a URL enough to fill Call.Args; full allowlist
// authorization happens inside the gated op so denied calls are recorded.
func extractHostPort(rawURL string) (host, port string, err error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", "", fmt.Errorf("net: invalid URL %q: %w", rawURL, err)
	}
	return u.Hostname(), u.Port(), nil
}

func bind(c *client, gate *capability.Gate) starlark.Value {
	return capability.Bind("net", gate, map[string]capability.Method{
		"get": func(args starlark.Tuple, kwargs []starlark.Tuple) (map[string]any, capability.OpBody, error) {
			var rawURL string
			var hdrs starlark.Value
			if err := starlark.UnpackArgs("get", args, kwargs, "url", &rawURL, "headers?", &hdrs); err != nil {
				return nil, nil, err
			}
			headers, err := unpackStringDict(hdrs)
			if err != nil {
				return nil, nil, err
			}
			host, port, err := extractHostPort(rawURL)
			if err != nil {
				return nil, nil, err
			}
			return netCallArgs("GET", rawURL, host, port), func(ctx context.Context) (starlark.Value, map[string]any, error) {
				r, err := c.do(ctx, "GET", rawURL, "", headers, "")
				if err != nil {
					return nil, nil, err
				}
				return respValue(r), respMeta(r), nil
			}, nil
		},
		"post": func(args starlark.Tuple, kwargs []starlark.Tuple) (map[string]any, capability.OpBody, error) {
			var rawURL, body, contentType string
			var hdrs starlark.Value
			contentType = "application/json"
			if err := starlark.UnpackArgs("post", args, kwargs, "url", &rawURL, "body?", &body, "headers?", &hdrs, "content_type?", &contentType); err != nil {
				return nil, nil, err
			}
			headers, err := unpackStringDict(hdrs)
			if err != nil {
				return nil, nil, err
			}
			host, port, err := extractHostPort(rawURL)
			if err != nil {
				return nil, nil, err
			}
			return netCallArgs("POST", rawURL, host, port), func(ctx context.Context) (starlark.Value, map[string]any, error) {
				r, err := c.do(ctx, "POST", rawURL, body, headers, contentType)
				if err != nil {
					return nil, nil, err
				}
				return respValue(r), respMeta(r), nil
			}, nil
		},
		"request": func(args starlark.Tuple, kwargs []starlark.Tuple) (map[string]any, capability.OpBody, error) {
			var method, rawURL, body string
			var hdrs starlark.Value
			if err := starlark.UnpackArgs("request", args, kwargs, "method", &method, "url", &rawURL, "body?", &body, "headers?", &hdrs); err != nil {
				return nil, nil, err
			}
			method = strings.ToUpper(method)
			headers, err := unpackStringDict(hdrs)
			if err != nil {
				return nil, nil, err
			}
			host, port, err := extractHostPort(rawURL)
			if err != nil {
				return nil, nil, err
			}
			return netCallArgs(method, rawURL, host, port), func(ctx context.Context) (starlark.Value, map[string]any, error) {
				r, err := c.do(ctx, method, rawURL, body, headers, "")
				if err != nil {
					return nil, nil, err
				}
				return respValue(r), respMeta(r), nil
			}, nil
		},
	})
}

func netCallArgs(method, rawURL, host, port string) map[string]any {
	scheme := ""
	if i := strings.Index(rawURL, "://"); i > 0 {
		scheme = rawURL[:i]
	}
	return map[string]any{
		"method": method,
		"url":    rawURL,
		"host":   host,
		"port":   port,
		"scheme": scheme,
	}
}
