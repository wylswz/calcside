package net

import (
	"compress/gzip"
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.starlark.net/starlark"

	"calcside/internal/capability"
	"calcside/internal/hostmatch"
	"calcside/internal/secrets"
)

// tlsClient builds a client trusting the test server's CA, with secrets
// and optional http allowance.
func tlsClient(t *testing.T, ts *httptest.Server, cfg Config, set *secrets.Set, allowHTTP bool) *client {
	t.Helper()
	var pool *x509.CertPool
	if ts != nil && ts.Certificate() != nil {
		pool = x509.NewCertPool()
		pool.AddCert(ts.Certificate())
	}
	f := factory{}
	v, err := f.Validate(mustJSON(t, cfg), capability.ServerLimits{SecretsAllowHTTP: allowHTTP})
	if err != nil {
		t.Fatal(err)
	}
	vv := v.(validated)
	vv.Config.RootCAs = pool // RootCAs is json:"-"; survives only via Go
	c := &client{cfg: vv.Config, allowPrivate: true, allowHTTP: allowHTTP, secrets: set}
	c.rules = mustRules(t, vv.Config)
	c.hc = c.newHTTPClient()
	return c
}

func secretSet(t *testing.T, name, value string, domains ...string) *secrets.Set {
	t.Helper()
	s := secrets.NewSet()
	rs, err := hostmatch.ParseAll(domains)
	if err != nil {
		t.Fatal(err)
	}
	s.Add(name, []byte(value), rs)
	return s
}

// echoSrv reflects the X-Token header back in body and a response header.
func echoSrv(t *testing.T) *httptest.Server {
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := r.Header.Get("X-Token")
		w.Header().Set("X-Echo", tok)
		w.Header().Set("X-Echo64", base64.StdEncoding.EncodeToString([]byte(tok)))
		fmt.Fprintf(w, "tok=%s", tok)
	}))
}

func TestSecretHeaderInjectedAndRedacted(t *testing.T) {
	ts := echoSrv(t)
	defer ts.Close()
	host, port := hostPort(ts)
	set := secretSet(t, "T", "s3cr3t-value", host+":"+port)
	c := tlsClient(t, ts, Config{AllowHosts: []string{host + ":" + port}}, set, false)

	r, err := c.do(context.Background(), "GET", ts.URL+"/h", "",
		map[string]string{"X-Token": "{{secrets.T}}"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.Body, "[REDACTED:T]") || strings.Contains(r.Body, "s3cr3t-value") {
		t.Fatalf("body not redacted: %q", r.Body)
	}
	if r.Headers["X-Echo"] != "[REDACTED:T]" {
		t.Fatalf("header not redacted: %q", r.Headers["X-Echo"])
	}
	if !strings.Contains(r.Headers["X-Echo64"], "[REDACTED:T]") {
		t.Fatalf("base64 header not redacted: %q", r.Headers["X-Echo64"])
	}
}

func TestSecretHostNotAllowed(t *testing.T) {
	ts := echoSrv(t)
	defer ts.Close()
	host, port := hostPort(ts)
	set := secretSet(t, "T", "s3cr3t-value", "api.other.com")
	c := tlsClient(t, ts, Config{AllowHosts: []string{host + ":" + port}}, set, false)
	_, err := c.do(context.Background(), "GET", ts.URL, "",
		map[string]string{"X-Token": "{{secrets.T}}"}, "")
	if err == nil || !strings.Contains(err.Error(), "not allowed for host") {
		t.Fatalf("expected host denial, got %v", err)
	}
	if strings.Contains(err.Error(), "s3cr3t-value") {
		t.Fatal("error leaked secret value")
	}
}

// Unrestricted secrets (no domains) inject into any host the net
// allow_hosts already permits; net still rejects unlisted hosts.
func TestSecretUnrestricted(t *testing.T) {
	ts := echoSrv(t)
	defer ts.Close()
	host, port := hostPort(ts)
	set := secretSet(t, "T", "s3cr3t-value") // no domains = unrestricted
	c := tlsClient(t, ts, Config{AllowHosts: []string{host + ":" + port}}, set, false)

	r, err := c.do(context.Background(), "GET", ts.URL+"/h", "",
		map[string]string{"X-Token": "{{secrets.T}}"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Headers["X-Echo"] != "[REDACTED:T]" || strings.Contains(r.Body, "s3cr3t-value") {
		t.Fatalf("injection/redaction failed: %v %q", r.Headers, r.Body)
	}

	// A host outside net allow_hosts is still rejected (net, not the
	// secret, is the outer bound).
	ts2 := echoSrv(t)
	defer ts2.Close()
	_, err = c.do(context.Background(), "GET", ts2.URL+"/h", "",
		map[string]string{"X-Token": "{{secrets.T}}"}, "")
	if err == nil || !strings.Contains(err.Error(), "not in allow_hosts") {
		t.Fatalf("expected allow_hosts denial, got %v", err)
	}
	if strings.Contains(err.Error(), "s3cr3t-value") {
		t.Fatal("error leaked secret value")
	}
}

// A domain-restricted secret is still denied on a non-matching host even
// when net allow_hosts is empty (unrestricted at the host layer).
func TestSecretDeniedOnEmptyAllowlist(t *testing.T) {
	ts := echoSrv(t)
	defer ts.Close()
	set := secretSet(t, "T", "s3cr3t-value", "api.other.com")
	c := tlsClient(t, ts, Config{}, set, false)
	_, err := c.do(context.Background(), "GET", ts.URL, "",
		map[string]string{"X-Token": "{{secrets.T}}"}, "")
	if err == nil || !strings.Contains(err.Error(), "not allowed for host") {
		t.Fatalf("expected secret host denial, got %v", err)
	}
	if strings.Contains(err.Error(), "s3cr3t-value") {
		t.Fatal("error leaked secret value")
	}
}

func TestSecretInHostRejected(t *testing.T) {
	c := tlsClient(t, nil, Config{AllowHosts: []string{"*.x.com"}},
		secretSet(t, "T", "v", "x.com"), false)
	_, err := c.do(context.Background(), "GET", "https://{{secrets.T}}.x.com/", "", nil, "")
	if err == nil || !strings.Contains(err.Error(), "scheme/host/port") {
		t.Fatalf("expected host placeholder rejection, got %v", err)
	}
}

func TestSecretHTTPDenied(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer ts.Close()
	host, port := hostPort(ts)
	set := secretSet(t, "T", "v", host+":"+port)
	cfg := Config{AllowHosts: []string{host + ":" + port}}
	c := tlsClient(t, ts, cfg, set, false)
	_, err := c.do(context.Background(), "GET", ts.URL+"/?t={{secrets.T}}", "", nil, "")
	if err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("expected https requirement, got %v", err)
	}
	// allowed with SecretsAllowHTTP
	c2 := tlsClient(t, ts, cfg, set, true)
	if _, err := c2.do(context.Background(), "GET", ts.URL+"/?t={{secrets.T}}", "", nil, ""); err != nil {
		t.Fatalf("http with SecretsAllowHTTP: %v", err)
	}
}

func TestSecretRedirect(t *testing.T) {
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer other.Close()
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/out":
			http.Redirect(w, r, other.URL+"/leak", http.StatusFound)
		case "/in":
			http.Redirect(w, r, "/landed", http.StatusFound)
		default:
			fmt.Fprint(w, "ok")
		}
	}))
	defer ts.Close()
	host, port := hostPort(ts)
	ohost, _ := hostPort(other)
	set := secretSet(t, "T", "v", host+":"+port)
	// allowlist permits both hosts so only the secret rule blocks it
	c := tlsClient(t, ts, Config{AllowHosts: []string{host + ":" + port, ohost}}, set, false)
	// need other host's port pinned too
	_, oport := hostPort(other)
	c2 := tlsClient(t, ts, Config{AllowHosts: []string{host + ":" + port, ohost + ":" + oport}}, set, false)
	_, err := c2.do(context.Background(), "GET", ts.URL+"/out?t={{secrets.T}}", "", nil, "")
	if err == nil || !strings.Contains(err.Error(), "forward secrets") {
		t.Fatalf("expected redirect block, got %v", err)
	}
	// redirect within allowed host is fine
	r, err := c.do(context.Background(), "GET", ts.URL+"/in?t={{secrets.T}}", "", nil, "")
	_ = r
	if err != nil && strings.Contains(err.Error(), "forward secrets") {
		t.Fatalf("same-host redirect wrongly blocked: %v", err)
	}
}

func TestTransportErrorRedacted(t *testing.T) {
	// nothing listens on this port
	set := secretSet(t, "T", "s3cr3t-value", "127.0.0.1:1")
	c := tlsClient(t, nil, Config{AllowHosts: []string{"127.0.0.1:1"}, TimeoutMs: 500}, set, false)
	_, err := c.do(context.Background(), "GET", "https://127.0.0.1:1/x?t={{secrets.T}}", "", nil, "")
	if err == nil {
		t.Fatal("expected transport error")
	}
	if strings.Contains(err.Error(), "s3cr3t-value") {
		t.Fatalf("transport error leaked secret: %v", err)
	}
}

func TestUnknownSecret(t *testing.T) {
	ts := echoSrv(t)
	defer ts.Close()
	host, port := hostPort(ts)
	c := tlsClient(t, ts, Config{AllowHosts: []string{host + ":" + port}}, secretSet(t, "T", "v", host+":"+port), false)
	_, err := c.do(context.Background(), "GET", ts.URL+"/?t={{secrets.MISSING}}", "", nil, "")
	if err == nil || !strings.Contains(err.Error(), `unknown secret "MISSING"`) {
		t.Fatalf("expected unknown secret error, got %v", err)
	}
}

// TestAuditArgsHaveTemplateNotValue verifies Call.Args carries the URL
// template + secret names, never expanded values.
func TestAuditArgsHaveTemplateNotValue(t *testing.T) {
	ts := echoSrv(t)
	defer ts.Close()
	host, port := hostPort(ts)
	set := secretSet(t, "T", "s3cr3t-value", host+":"+port)
	c := tlsClient(t, ts, Config{AllowHosts: []string{host + ":" + port}}, set, false)

	var recs []capability.Record
	obs := recordObs{&recs}
	gate := capability.NewGate(capability.GateOwner{InstanceID: "ins_1"}, nil, obs)
	gate.Arm(capability.ExecContext{ExecID: "e1", InstanceID: "ins_1"})

	val := bind(c, gate)
	mod := val.(starlark.HasAttrs)
	fn, _ := mod.Attr("get")
	thread := &starlark.Thread{Name: "t"}
	thread.SetLocal(capability.ContextKey, context.Background())
	url := ts.URL + "/x?t={{secrets.T}}"
	hdr := starlark.NewDict(1)
	_ = hdr.SetKey(starlark.String("X-Token"), starlark.String("{{secrets.T}}"))
	_, err := starlark.Call(thread, fn, starlark.Tuple{starlark.String(url)}, []starlark.Tuple{{starlark.String("headers"), hdr}})
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("expected 1 record, got %d", len(recs))
	}
	argsJSON, _ := json.Marshal(recs[0].Call.Args)
	if strings.Contains(string(argsJSON), "s3cr3t-value") {
		t.Fatalf("audit args leaked value: %s", argsJSON)
	}
	if !strings.Contains(string(argsJSON), "{{secrets.T}}") {
		t.Fatalf("args missing template: %s", argsJSON)
	}
	if names, _ := recs[0].Call.Args["secrets"].([]string); len(names) != 1 || names[0] != "T" {
		t.Fatalf("args.secrets wrong: %v", recs[0].Call.Args["secrets"])
	}
}

func TestForbiddenRequestHeaders(t *testing.T) {
	ts := echoSrv(t)
	defer ts.Close()
	host, port := hostPort(ts)
	c := tlsClient(t, ts, Config{AllowHosts: []string{host + ":" + port}}, nil, false)
	gate := capability.NewGate(capability.GateOwner{InstanceID: "ins_1"}, nil, nil)
	gate.Arm(capability.ExecContext{ExecID: "e1", InstanceID: "ins_1"})
	mod := bind(c, gate).(starlark.HasAttrs)
	fn, _ := mod.Attr("get")
	thread := &starlark.Thread{Name: "t"}
	thread.SetLocal(capability.ContextKey, context.Background())

	get := func(hdrs starlark.Value) error {
		_, err := starlark.Call(thread, fn, starlark.Tuple{starlark.String(ts.URL + "/")},
			[]starlark.Tuple{{starlark.String("headers"), hdrs}})
		return err
	}
	for _, name := range []string{"Accept-Encoding", "accept-encoding", "RANGE", "If-Range", "te"} {
		d := starlark.NewDict(1)
		_ = d.SetKey(starlark.String(name), starlark.String("x"))
		err := get(d)
		if err == nil || !strings.Contains(err.Error(), "not allowed") {
			t.Fatalf("header %q not rejected: %v", name, err)
		}
	}
	d := starlark.NewDict(1)
	_ = d.SetKey(starlark.String("X-Token"), starlark.String("x"))
	if err := get(d); err != nil {
		t.Fatalf("normal header rejected: %v", err)
	}
}

// TestGzipEchoRedacted: Go's transparent gunzip must still let redaction
// see plaintext when the server compresses a reflected secret.
func TestGzipEchoRedacted(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(200)
		gz := gzip.NewWriter(w)
		fmt.Fprintf(gz, "tok=%s", r.Header.Get("X-Token"))
		gz.Close()
	}))
	defer ts.Close()
	host, port := hostPort(ts)
	set := secretSet(t, "T", "s3cr3t-value", host+":"+port)
	c := tlsClient(t, ts, Config{AllowHosts: []string{host + ":" + port}}, set, false)
	r, err := c.do(context.Background(), "GET", ts.URL+"/h", "",
		map[string]string{"X-Token": "{{secrets.T}}"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.Body, "[REDACTED:T]") || strings.Contains(r.Body, "s3cr3t-value") {
		t.Fatalf("gzip body not redacted: %q", r.Body)
	}
}

type recordObs struct{ recs *[]capability.Record }

func (o recordObs) Observe(r capability.Record) { *o.recs = append(*o.recs, r) }
