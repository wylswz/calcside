package policy

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"calcside/internal/capability"
)

func call(host string) *capability.Call {
	return &capability.Call{
		Capability: "net", Op: "get",
		Args:   map[string]any{"method": "GET", "url": "http://" + host + "/", "host": host, "scheme": "http", "port": "80"},
		UserID: "u1", UserEmail: "u@x.com", InstanceID: "i1", ExecID: "e1",
	}
}

func writeDir(t *testing.T, files map[string]string) string {
	t.Helper()
	d := t.TempDir()
	for name, src := range files {
		if err := os.WriteFile(filepath.Join(d, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func TestBeforeDenyOnHost(t *testing.T) {
	dir := writeDir(t, map[string]string{
		"deny.rego": `package calcside.hooks
deny contains msg if {
	input.phase == "before"
	input.capability == "net"
	input.args.host == "bad.com"
	msg := "bad host"
}`,
	})
	h, err := NewHook(dir, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Before(context.Background(), call("bad.com")); err == nil || err.Error() == "" {
		t.Fatal("expected deny")
	}
	if err := h.Before(context.Background(), call("good.com")); err != nil {
		t.Fatalf("unexpected deny: %v", err)
	}
}

func TestAfterDenyOnResultBytes(t *testing.T) {
	dir := writeDir(t, map[string]string{
		"big.rego": `package calcside.hooks
deny contains msg if {
	input.phase == "after"
	input.capability == "net"
	input.result.meta.bytes > 5
	msg := "too big"
}`,
	})
	h, err := NewHook(dir, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	res := &capability.Result{Meta: map[string]any{"bytes": 100}}
	if err := h.After(context.Background(), call("x.com"), res); err == nil {
		t.Fatal("expected after deny")
	}
	res.Meta["bytes"] = 1
	if err := h.After(context.Background(), call("x.com"), res); err != nil {
		t.Fatalf("unexpected deny: %v", err)
	}
}

func TestEvalErrorDenies(t *testing.T) {
	// Strict builtin errors turn this json.unmarshal failure into an eval
	// error, which must deny (fail closed).
	dir := writeDir(t, map[string]string{
		"boom.rego": `package calcside.hooks
deny contains "unreachable" if {
	x := json.unmarshal("not json")
	x == "never"
}`,
	})
	h, err := NewHook(dir, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Before(context.Background(), call("x.com")); err == nil {
		t.Fatal("eval error must deny (fail closed)")
	}
}

func TestUserPolicyIsolation(t *testing.T) {
	dir := writeDir(t, map[string]string{
		"global.rego": `package calcside.hooks
deny contains "global rule" if {
	input.args.host == "globalbad.com"
}`,
	})
	userA := `package calcside.hooks
deny contains "userA rule" if {
	input.args.host == "userabad.com"
}`
	h, err := NewHook(dir, map[string]string{"pol_a": userA}, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Both rules live in their own namespaces; no collisions.
	if err := h.Before(context.Background(), call("globalbad.com")); err == nil {
		t.Fatal("global deny expected")
	}
	if err := h.Before(context.Background(), call("userabad.com")); err == nil {
		t.Fatal("user deny expected")
	}
	if err := h.Before(context.Background(), call("fine.com")); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	// A second hook for another user doesn't see user A's rules.
	h2, err := NewHook(dir, map[string]string{
		"pol_b": `package calcside.hooks
deny contains "b" if { input.args.host == "bbad.com" }`,
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := h2.Before(context.Background(), call("userabad.com")); err != nil {
		t.Fatal("user B hook must not contain user A rules")
	}
}

func TestEvalTimeoutDenies(t *testing.T) {
	// A 1ns eval budget means the eval context is dead on arrival.
	dir := writeDir(t, map[string]string{
		"slow.rego": `package calcside.hooks
deny contains "never" if { input.op == "get" }`,
	})
	h, err := NewHook(dir, nil, time.Nanosecond)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Before(context.Background(), call("x.com")); err == nil {
		t.Fatal("eval timeout must deny (fail closed)")
	}
}

func TestValidate(t *testing.T) {
	if err := Validate(`package calcside.hooks
deny contains "x" if { input.op == "read" }`); err != nil {
		t.Fatalf("valid policy rejected: %v", err)
	}
	if err := Validate(`package other.pkg
x := 1`); err == nil {
		t.Fatal("wrong package accepted")
	}
	if err := Validate(`package calcside.hooks
deny contains if {`); err == nil {
		t.Fatal("syntax error accepted")
	}
}

func TestUserPolicySandbox(t *testing.T) {
	for _, src := range []string{
		`package calcside.hooks
deny contains sprintf("%v", [opa.runtime()]) if { input.op == "read" }`,
		`package calcside.hooks
deny contains "x" if { http.send({"method": "get", "url": "http://169.254.169.254/"}) }`,
		`package calcside.hooks
deny contains "x" if { net.lookup_ip_addr("example.com") }`,
		`package calcside.hooks
deny contains "x" if { print("hi") }`,
	} {
		if err := Validate(src); err == nil {
			t.Fatalf("Validate accepted dangerous builtin: %s", src)
		}
		if _, err := CompileUserPolicy("p", src); err == nil {
			t.Fatalf("CompileUserPolicy accepted dangerous builtin: %s", src)
		}
		if _, err := NewHook("", map[string]string{"p": src}, 0); err == nil {
			t.Fatalf("NewHook accepted dangerous builtin: %s", src)
		}
	}
}

func TestPackageClauseParsing(t *testing.T) {
	if err := Validate("package calcside.hooks # trailing comment\n"); err != nil {
		t.Fatalf("commented package rejected: %v", err)
	}
	if err := Validate("package calcside.hooksx\ndeny contains \"x\" if { true }"); err == nil {
		t.Fatal("calcside.hooksx accepted")
	}
}

func TestWrongPackageRejectedAtLoad(t *testing.T) {
	dir := writeDir(t, map[string]string{
		"bad.rego": `package wrong.name
x := 1`,
	})
	if _, err := NewHook(dir, nil, 0); err == nil {
		t.Fatal("expected package rejection")
	}
}
