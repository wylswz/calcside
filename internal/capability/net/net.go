// Package net implements the "net" capability: HTTP requests gated by a
// host allowlist, with DNS resolution done in-process and connections
// pinned to vetted IPs to resist SSRF and DNS-rebinding. It also
// substitutes {{secrets.NAME}} placeholders at send time; plaintext
// secret values never reach the script, audit, or policy input.
package net

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	gonet "net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"go.starlark.net/starlark"

	"calcside/internal/capability"
	"calcside/internal/hostmatch"
	"calcside/internal/secrets"
	"calcside/internal/types"
)

// Config configures one instance's net capability.
type Config struct {
	AllowHosts       []string           `json:"allow_hosts"`
	Methods          []types.HTTPMethod `json:"methods"`
	MaxResponseBytes int64              `json:"max_response_bytes"`
	TimeoutMs        int64              `json:"timeout_ms"`
	// RootCAs optionally overrides the TLS trust store (tests only).
	RootCAs *x509.CertPool `json:"-"`
}

const (
	defaultMaxResponse = 10 << 20
	defaultTimeoutMs   = 10000
	maxRedirects       = 5
)

var placeholderRe = regexp.MustCompile(`\{\{\s*secrets\.[A-Z_][A-Z0-9_]{0,63}\s*\}\}`)

type client struct {
	cfg          Config
	rules        []hostmatch.Rule
	allowPrivate bool
	allowHTTP    bool
	secrets      *secrets.Set
	hc           *http.Client
}

// injectedKey carries the names of secrets injected into a request so the
// redirect checker can verify the next hop still satisfies their rules.
type injectedKeyT struct{}

var injectedKey injectedKeyT

// authorize checks method + host + port against config.
func (c *client) authorize(method, rawURL string) (host, effPort string, err error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", "", fmt.Errorf("net: invalid URL %q: %w", rawURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", "", fmt.Errorf("net: unsupported scheme in URL %q", rawURL)
	}
	host = u.Hostname()
	if host == "" {
		return "", "", fmt.Errorf("net: empty host in URL %q", rawURL)
	}
	effPort = u.Port()
	if effPort == "" {
		if u.Scheme == "https" {
			effPort = "443"
		} else {
			effPort = "80"
		}
	}

	m, err := types.ParseHTTPMethod(method)
	if err != nil {
		return "", "", fmt.Errorf("net: %w", err)
	}
	ok := false
	for _, cm := range c.cfg.Methods {
		if cm == m {
			ok = true
			break
		}
	}
	if !ok {
		return "", "", fmt.Errorf("net: method %s not in methods %v", m, c.cfg.Methods)
	}
	if len(c.rules) == 0 {
		return "", "", fmt.Errorf("net: no hosts permitted (empty allow_hosts)")
	}
	for i := range c.rules {
		if c.rules[i].Matches(host, effPort) {
			return host, effPort, nil
		}
	}
	return "", "", fmt.Errorf("net: host %q (port %s) not in allow_hosts", host, effPort)
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
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: c.cfg.RootCAs},
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
			if _, _, err := c.authorize(req.Method, req.URL.String()); err != nil {
				return fmt.Errorf("net: redirect blocked: %w", err)
			}
			// If any secret was injected into the original request, the
			// redirect target must satisfy every secret's rules or we
			// would leak it to an arbitrary host.
			if names, ok := via[0].Context().Value(injectedKey).([]string); ok && len(names) > 0 {
				host := req.URL.Hostname()
				eff := req.URL.Port()
				if eff == "" {
					if req.URL.Scheme == "https" {
						eff = "443"
					} else {
						eff = "80"
					}
				}
				for _, n := range names {
					sec := c.secrets.Lookup(n)
					ok := sec != nil && sec.Allows(host, eff) &&
						(req.URL.Scheme == "https" || c.allowHTTP)
					if !ok {
						return fmt.Errorf("net: redirect blocked: would forward secrets to %s", host)
					}
				}
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

func (c *client) redact(s string) string {
	if c.secrets == nil {
		return s
	}
	return c.secrets.Redact(s)
}

// hasPlaceholder reports whether s contains any {{secrets.X}} template.
func hasPlaceholder(s string) bool { return placeholderRe.MatchString(s) }

// urlAuthority extracts the scheme+authority part of a raw URL template.
func urlAuthority(raw string) string {
	i := strings.Index(raw, "://")
	if i < 0 {
		return raw
	}
	rest := raw[i+3:]
	if j := strings.IndexAny(rest, "/?#"); j >= 0 {
		rest = rest[:j]
	}
	return raw[:i] + "://" + rest
}

// expand resolves secret placeholders in url/headers/body, validates the
// injection policy, and returns the expanded request parts plus the names
// injected. All returned errors are value-free.
func (c *client) expand(rawURL string, headers map[string]string, body string) (string, map[string]string, string, []string, error) {
	set := c.secrets
	if set == nil {
		set = secrets.NewSet()
	}
	needExpand := hasPlaceholder(rawURL) || hasPlaceholder(body)
	if !needExpand {
		for _, v := range headers {
			if hasPlaceholder(v) {
				needExpand = true
				break
			}
		}
	}
	if !needExpand {
		return rawURL, headers, body, nil, nil
	}

	for k := range headers {
		if hasPlaceholder(k) {
			return "", nil, "", nil, fmt.Errorf("net: secret placeholders are not allowed in header names")
		}
	}

	// Template URL: placeholders replaced by "x" for parsing.
	tmpl := placeholderRe.ReplaceAllString(rawURL, "x")
	if hasPlaceholder(urlAuthority(rawURL)) {
		return "", nil, "", nil, fmt.Errorf("net: secret placeholders are not allowed in scheme/host/port")
	}
	tu, err := url.Parse(tmpl)
	if err != nil {
		return "", nil, "", nil, fmt.Errorf("net: invalid URL template: %w", err)
	}

	expURL, _, err := set.Expand(rawURL)
	if err != nil {
		return "", nil, "", nil, fmt.Errorf("net: %w", err)
	}
	eu, err := url.Parse(expURL)
	if err != nil {
		return "", nil, "", nil, fmt.Errorf("net: invalid URL after secret expansion")
	}
	// Expansion must not change the endpoint.
	if eu.Scheme != tu.Scheme || eu.Hostname() != tu.Hostname() || eu.Port() != tu.Port() {
		return "", nil, "", nil, fmt.Errorf("net: secret expansion changed URL scheme/host/port")
	}

	expHeaders := make(map[string]string, len(headers))
	for k, v := range headers {
		ev, _, err := set.Expand(v)
		if err != nil {
			return "", nil, "", nil, fmt.Errorf("net: %w", err)
		}
		expHeaders[k] = ev
	}
	expBody, _, err := set.Expand(body)
	if err != nil {
		return "", nil, "", nil, fmt.Errorf("net: %w", err)
	}

	// Union of referenced names across URL + header values + body.
	nameSet := map[string]bool{}
	pieces := append([]string{rawURL, body}, mapVals(headers)...)
	for _, s := range pieces {
		for _, n := range secrets.Refs(s) {
			nameSet[n] = true
		}
	}
	names := make([]string, 0, len(nameSet))
	for n := range nameSet {
		names = append(names, n)
	}
	sort.Strings(names)
	// per-secret target check
	host := eu.Hostname()
	effPort := eu.Port()
	if effPort == "" {
		if eu.Scheme == "https" {
			effPort = "443"
		} else {
			effPort = "80"
		}
	}
	for _, n := range names {
		sec := set.Lookup(n)
		if sec == nil {
			return "", nil, "", nil, fmt.Errorf("net: unknown secret %q", n)
		}
		if !sec.Allows(host, effPort) {
			return "", nil, "", nil, fmt.Errorf("net: secret %s not allowed for host %s", n, host)
		}
		if eu.Scheme != "https" && !c.allowHTTP {
			return "", nil, "", nil, fmt.Errorf("net: secret %s requires https (http URL)", n)
		}
	}
	return expURL, expHeaders, expBody, names, nil
}

func mapVals(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

func (c *client) do(ctx context.Context, method, rawURL, body string, headers map[string]string, contentType string) (*response, error) {
	resp, err := c.doInner(ctx, method, rawURL, body, headers, contentType)
	if err != nil {
		// Every error path is scrubbed: transport errors embed the
		// expanded URL (which may contain an injected secret).
		return nil, fmt.Errorf("%s", c.redact(err.Error()))
	}
	return resp, nil
}

func (c *client) doInner(ctx context.Context, method, rawURL, body string, headers map[string]string, contentType string) (*response, error) {
	expURL, expHeaders, expBody, injected, err := c.expand(rawURL, headers, body)
	if err != nil {
		return nil, err
	}
	if _, _, err := c.authorize(method, expURL); err != nil {
		return nil, err
	}
	if len(injected) > 0 {
		ctx = context.WithValue(ctx, injectedKey, injected)
	}
	var rdr io.Reader
	if expBody != "" {
		rdr = strings.NewReader(expBody)
	}
	req, err := http.NewRequestWithContext(ctx, method, expURL, rdr)
	if err != nil {
		return nil, fmt.Errorf("net: %w", err)
	}
	for k, v := range expHeaders {
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
		hdrs[k] = c.redact(resp.Header.Get(k))
	}
	return &response{Status: resp.StatusCode, Headers: hdrs, Body: c.redact(string(data))}, nil
}

// --- factory + binding ---

type factory struct{}

// Factory returns the capability.Factory for "net".
func Factory() capability.Factory { return factory{} }

// Op consts for net.
const (
	OpGet     types.Op = "get"
	OpPost    types.Op = "post"
	OpRequest types.Op = "request"
)

func (factory) Name() types.CapabilityName { return types.CapNet }

func (factory) Ops() []capability.OpInfo {
	return []capability.OpInfo{
		{Name: OpGet, Doc: "HTTP GET; returns {status,headers,body}; supports {{secrets.NAME}} placeholders; Accept-Encoding/Range/If-Range/TE headers are rejected", Params: []string{"url", "headers"}},
		{Name: OpPost, Doc: "HTTP POST; returns {status,headers,body}", Params: []string{"url", "body", "headers", "content_type"}},
		{Name: OpRequest, Doc: "HTTP request with arbitrary method", Params: []string{"method", "url", "body", "headers"}},
	}
}

func (factory) ConfigFields() []capability.FieldDoc {
	return []capability.FieldDoc{
		{Name: "allow_hosts", Type: types.FieldStringList, Doc: "exact host, *.suffix wildcard, or host:port; IP literals allowed for testing", Default: []string{}},
		{Name: "methods", Type: types.FieldStringList, Doc: "permitted HTTP methods", Default: []string{"GET", "POST"}},
		{Name: "max_response_bytes", Type: types.FieldInt, Doc: "response body cap", Default: defaultMaxResponse},
		{Name: "timeout_ms", Type: types.FieldInt, Doc: "request timeout", Default: defaultTimeoutMs},
	}
}

func (factory) Validate(raw json.RawMessage, limits capability.ServerLimits) (any, error) {
	cfg := Config{Methods: []types.HTTPMethod{types.MethodGet, types.MethodPost}, MaxResponseBytes: defaultMaxResponse, TimeoutMs: defaultTimeoutMs}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("net config: %w", err)
		}
	}
	for _, h := range cfg.AllowHosts {
		r, err := hostmatch.Parse(h)
		if err != nil {
			return nil, err
		}
		// IP-literal entries are only meaningful when private addresses are
		// allowed (tests, internal services); reject blocked literals.
		if r.IsIP && !limits.NetAllowPrivate {
			if ip := gonet.ParseIP(r.Host); ip != nil && isBlockedIP(ip) {
				return nil, fmt.Errorf("net config: IP-literal allow_hosts entry %q in a blocked range (set --net-allow-private to permit)", h)
			}
		}
	}
	if len(cfg.Methods) == 0 {
		cfg.Methods = []types.HTTPMethod{types.MethodGet, types.MethodPost}
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
	return validated{Config: cfg, allowPrivate: limits.NetAllowPrivate, allowHTTP: limits.SecretsAllowHTTP}, nil
}

// validated carries the parsed config plus server policy the binding needs.
type validated struct {
	Config
	allowPrivate bool
	allowHTTP    bool
}

func (factory) New(cfgAny any, env capability.InstanceEnv) (starlark.Value, io.Closer, error) {
	v, ok := cfgAny.(validated)
	if !ok {
		return nil, nil, fmt.Errorf("net: config must come from Validate")
	}
	c := &client{cfg: v.Config, allowPrivate: v.allowPrivate, allowHTTP: v.allowHTTP, secrets: env.Secrets}
	rules, err := hostmatch.ParseAll(v.AllowHosts)
	if err != nil {
		return nil, nil, err
	}
	c.rules = rules
	c.hc = c.newHTTPClient()
	return bind(c, env.Gate), closer{c}, nil
}

type closer struct{ c *client }

func (cl closer) Close() error {
	cl.c.hc.CloseIdleConnections()
	return nil
}

// forbiddenHeaders are rejected in script-supplied request headers.
// Accept-Encoding would disable transparent gunzip and Range/If-Range/TE
// would let a script fetch a reflected secret in slices — both bypass
// whole-value redaction of echoed secrets.
var forbiddenHeaders = map[string]bool{
	"accept-encoding": true,
	"range":           true,
	"if-range":        true,
	"te":              true,
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
		if forbiddenHeaders[strings.ToLower(string(k))] {
			return nil, fmt.Errorf("net: header %q is not allowed", string(k))
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

func respMeta(r *response, nSecrets int) map[string]any {
	return map[string]any{"status": r.Status, "bytes": len(r.Body), "secrets_injected": nSecrets}
}

// secretRefs returns sorted secret names referenced by placeholders in
// url, header values, and body — for Call.Args (template is logged, never
// expanded values).
func secretRefs(rawURL string, headers map[string]string, body string) []string {
	set := map[string]bool{}
	for _, n := range secrets.Refs(rawURL) {
		set[n] = true
	}
	for _, n := range secrets.Refs(body) {
		set[n] = true
	}
	for _, v := range headers {
		for _, n := range secrets.Refs(v) {
			set[n] = true
		}
	}
	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// extractHostPort parses a URL enough to fill Call.Args; full allowlist
// authorization happens inside the gated op so denied calls are recorded.
func extractHostPort(rawURL string) (host, port string, err error) {
	u, err := url.Parse(placeholderRe.ReplaceAllString(rawURL, "x"))
	if err != nil {
		return "", "", fmt.Errorf("net: invalid URL %q: %w", rawURL, err)
	}
	return u.Hostname(), u.Port(), nil
}

func bind(c *client, gate *capability.Gate) starlark.Value {
	call := func(method, rawURL, body string, headers map[string]string, contentType string) (map[string]any, capability.OpBody, error) {
		host, port, err := extractHostPort(rawURL)
		if err != nil {
			return nil, nil, err
		}
		args := netCallArgs(method, rawURL, host, port)
		args["secrets"] = secretRefs(rawURL, headers, body)
		return args, func(ctx context.Context) (starlark.Value, map[string]any, error) {
			r, err := c.do(ctx, method, rawURL, body, headers, contentType)
			if err != nil {
				return nil, nil, err
			}
			return respValue(r), respMeta(r, len(secretRefs(rawURL, headers, body))), nil
		}, nil
	}
	return capability.Bind(types.CapNet, gate, map[types.Op]capability.Method{
		OpGet: func(args starlark.Tuple, kwargs []starlark.Tuple) (map[string]any, capability.OpBody, error) {
			var rawURL string
			var hdrs starlark.Value
			if err := starlark.UnpackArgs(string(OpGet), args, kwargs, "url", &rawURL, "headers?", &hdrs); err != nil {
				return nil, nil, err
			}
			headers, err := unpackStringDict(hdrs)
			if err != nil {
				return nil, nil, err
			}
			return call("GET", rawURL, "", headers, "")
		},
		OpPost: func(args starlark.Tuple, kwargs []starlark.Tuple) (map[string]any, capability.OpBody, error) {
			var rawURL, body, contentType string
			var hdrs starlark.Value
			contentType = "application/json"
			if err := starlark.UnpackArgs(string(OpPost), args, kwargs, "url", &rawURL, "body?", &body, "headers?", &hdrs, "content_type?", &contentType); err != nil {
				return nil, nil, err
			}
			headers, err := unpackStringDict(hdrs)
			if err != nil {
				return nil, nil, err
			}
			return call("POST", rawURL, body, headers, contentType)
		},
		OpRequest: func(args starlark.Tuple, kwargs []starlark.Tuple) (map[string]any, capability.OpBody, error) {
			var method, rawURL, body string
			var hdrs starlark.Value
			if err := starlark.UnpackArgs(string(OpRequest), args, kwargs, "method", &method, "url", &rawURL, "body?", &body, "headers?", &hdrs); err != nil {
				return nil, nil, err
			}
			if _, err := types.ParseHTTPMethod(method); err != nil {
				return nil, nil, fmt.Errorf("net: %w", err)
			}
			method = strings.ToUpper(method)
			headers, err := unpackStringDict(hdrs)
			if err != nil {
				return nil, nil, err
			}
			return call(method, rawURL, body, headers, "")
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
