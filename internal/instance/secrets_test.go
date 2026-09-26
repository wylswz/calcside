package instance

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"calcside/internal/capability"
	capfs "calcside/internal/capability/fs"
	capio "calcside/internal/capability/io"
	capnet "calcside/internal/capability/net"
)

// Vault reference resolution — decryption and allowlist narrowing —
// belongs to the API tier and is tested in internal/service/vault. By
// the time a spec reaches a node, its secrets are already plaintext
// values with an effective allowlist, which is what these cover.

func netNode(t *testing.T) *node {
	t.Helper()
	reg := capability.NewRegistry()
	reg.Register(capfs.Factory())
	reg.Register(capio.Factory())
	reg.Register(capnet.Factory())
	limits := defaultLimits()
	limits.NetAllowPrivate = true
	limits.SecretsAllowHTTP = true
	return newNode(t, Options{Registry: reg, Limits: limits}, nil)
}

func TestSecretWipeOnDelete(t *testing.T) {
	n := netNode(t)
	id := n.mustCreate(`{"secrets":{"INL":{"value":"wipe-me","allowed_domains":["x.com"]}}}`)
	in, err := n.m.live(id, n.owner)
	if err != nil || in.secrets.Lookup("INL") == nil {
		t.Fatalf("secret not resolved: %v", err)
	}
	stored := in.secrets.Lookup("INL").Value()
	if err := n.delete(id); err != nil {
		t.Fatal(err)
	}
	if string(stored) != strings.Repeat("\x00", len("wipe-me")) {
		t.Fatalf("secret not wiped: %q", stored)
	}
}

// The node injects a secret only into hosts its effective allowlist
// permits, and never leaks the value into the error when it refuses.
func TestSecretInjectionRespectsAllowlist(t *testing.T) {
	var echoTok string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		echoTok = r.Header.Get("X-Token")
	}))
	defer srv.Close()
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer other.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	host2 := strings.TrimPrefix(other.URL, "http://")

	n := netNode(t)
	id := n.mustCreate(fmt.Sprintf(
		`{"capabilities":{"net":{"allow_hosts":[%q,%q]}},
		"secrets":{"T":{"value":"vault-secret-1","allowed_domains":[%q]}}}`, host, host2, host))

	res := n.mustExec(id, fmt.Sprintf(
		`net.get(url=%q, headers={"X-Token":"{{secrets.T}}"})`, srv.URL))
	_ = res
	if echoTok != "vault-secret-1" {
		t.Fatalf("header: %q", echoTok)
	}

	res, err := n.exec(id, fmt.Sprintf(
		`net.get(url=%q, headers={"X-Token":"{{secrets.T}}"})`, other.URL))
	if err != nil || res.Error == nil || !strings.Contains(res.Error.Message, "not allowed for host") {
		t.Fatalf("other host should be denied: %v %+v", err, res.Error)
	}
	if strings.Contains(res.Error.Message, "vault-secret-1") {
		t.Fatal("error leaked secret value")
	}
}

// An empty allowlist is unrestricted: the secret may go to any host
// the instance's net allow_hosts already permits.
func TestUnrestrictedSecret(t *testing.T) {
	var echoTok string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		echoTok = r.Header.Get("X-Token")
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")

	n := netNode(t)
	id := n.mustCreate(fmt.Sprintf(
		`{"capabilities":{"net":{"allow_hosts":[%q]}},"secrets":{"T":{"value":"wide-open"}}}`, host))
	n.mustExec(id, fmt.Sprintf(`net.get(url=%q, headers={"X-Token":"{{secrets.T}}"})`, srv.URL))
	if echoTok != "wide-open" {
		t.Fatalf("header: %q", echoTok)
	}
}

func TestEnvAndSecretsGlobals(t *testing.T) {
	n := netNode(t)
	id := n.mustCreate(`{"env":{"REGION":"us-east-1"},"secrets":{"TOK":{"value":"v","allowed_domains":["x.com"]}}}`)
	res := n.mustExec(id, `print(env.get("REGION")); print(env["REGION"]); print(sorted(env.keys())); print(secrets.names()); print("{{secrets.TOK}}")`)
	want := "us-east-1\nus-east-1\n[\"REGION\"]\n[\"TOK\"]\n{{secrets.TOK}}\n"
	if res.Output != want {
		t.Fatalf("output %q want %q", res.Output, want)
	}
}
