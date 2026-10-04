package runtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"calcside/internal/types"
)

// Every message must survive a round trip unchanged: an execution node
// reads nothing but what a request carries, so anything lost in
// transit is context the node will silently be missing.
func TestMessagesRoundTrip(t *testing.T) {
	ts := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	cases := []any{
		&CreateRequest{
			InstanceID: "ins_1",
			Owner:      Owner{UserID: "usr_1", Email: "u@x.com"},
			Labels:     map[string]string{"team": "core"},
			Spec:       json.RawMessage(`{"ttl_seconds":900,"env":null}`),
			Policies: PolicyBundle{
				Global: map[string]string{"deny.rego": "package calcside.hooks"},
				User:   map[string]string{"pol_1": "package calcside.hooks"},
			},
			Secrets: []Secret{{
				Name: "TOK", Value: "s3cr3t",
				AllowedDomains: []string{"api.example.com"}, Source: types.SecretVault,
			}},
			ExpiresAt: ts,
		},
		&CreateResponse{Capabilities: []types.CapabilityName{types.CapFS, types.CapIO}},
		&ExecRequest{
			InstanceID: "ins_1", Owner: Owner{UserID: "usr_1"},
			ExecID: "exe_1", Code: "print(1)", TimeoutMs: 1500,
			RenewedExpiresAt: ts,
		},
		&ExecResponse{
			Result: ExecResult{
				ExecID: "exe_1", Output: "1\n",
				Error:      &ExecError{Type: types.ErrRuntime, Message: "boom", Backtrace: "bt"},
				DurationMs: 12, Steps: 34,
			},
			Audit: AuditBatch{
				Events: []AuditEvent{{
					Ts: ts, UserID: "usr_1", InstanceID: "ins_1", ExecID: "exe_1",
					Capability: types.CapNet, Op: "get", Args: `{"host":"x"}`,
					Phase: types.PhaseBefore, Decision: types.DecisionDeny,
					Reason: "nope", Error: "denied", DurationMs: 3,
				}},
				Dropped: 2,
			},
		},
		&KeepaliveRequest{InstanceID: "ins_1", Owner: Owner{UserID: "usr_1"}, RenewedExpiresAt: ts},
		&DeleteRequest{InstanceID: "ins_1", Owner: Owner{UserID: "usr_1"}},
		&BrowseRequest{InstanceID: "ins_1", Owner: Owner{UserID: "usr_1"}, Path: "/work"},
		&BrowseResponse{
			IsDir:   true,
			Entries: []FileEntry{{Name: "a.txt", Path: "/work/a.txt", Size: 3, Mtime: 1}},
			Audit:   AuditBatch{Events: []AuditEvent{{Ts: ts, Op: "list", Decision: types.DecisionAllow}}},
		},
		&PromptResponse{
			Fragments:     []PromptFragment{{Capability: types.CapFS, Text: "fs docs"}},
			Env:           map[string]string{"REGION": "us-east-1"},
			Secrets:       []PromptSecret{{Name: "TOK", Domains: []string{"x.com"}}},
			ExecTimeoutMs: 30000, MaxSteps: 10, MaxOutputBytes: 1024,
			TTLSeconds: 900, NetHosts: []string{"x.com"},
		},
		&InspectRequest{InstanceID: "ins_1", Owner: Owner{UserID: "usr_1"}, Epoch: 3},
		&InspectResponse{
			Variables:      map[string]string{"x": "1", "items": "[1, 2]"},
			ResourceUsages: ResourceUsages{MemoryUsage: 3 << 20, MemoryPeak: 5 << 20, MemoryMax: 256 << 20},
		},
	}
	for _, want := range cases {
		b, err := json.Marshal(want)
		if err != nil {
			t.Fatalf("%T: marshal: %v", want, err)
		}
		got := reflect.New(reflect.TypeOf(want).Elem()).Interface()
		if err := json.Unmarshal(b, got); err != nil {
			t.Fatalf("%T: unmarshal: %v", want, err)
		}
		if !reflect.DeepEqual(want, got) {
			t.Errorf("%T round trip lost data:\n have %+v\n want %+v", want, got, want)
		}
	}
}

// A create request carries plaintext secrets, so the obvious ways of
// printing one must not spill them.
func TestSecretNeverFormatsItsValue(t *testing.T) {
	s := Secret{Name: "TOK", Value: "s3cr3t", AllowedDomains: []string{"x.com"}}
	for _, format := range []string{"%v", "%s", "%+v"} {
		if out := fmt.Sprintf(format, s); strings.Contains(out, "s3cr3t") {
			t.Errorf("Sprintf(%q) leaked the value: %s", format, out)
		}
	}
	req := &CreateRequest{InstanceID: "ins_1", Secrets: []Secret{s}}
	if out := fmt.Sprintf("%v", req.Secrets); strings.Contains(out, "s3cr3t") {
		t.Errorf("formatting the slice leaked the value: %s", out)
	}
	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, nil)).Info("create", "req", req, "secret", s)
	if strings.Contains(buf.String(), "s3cr3t") {
		t.Errorf("slog leaked the value: %s", buf.String())
	}
	// It must still serialize, or the node would get nothing.
	b, err := json.Marshal(s)
	if err != nil || !strings.Contains(string(b), "s3cr3t") {
		t.Fatalf("secret must still marshal for the wire: %s %v", b, err)
	}
}

// Kinds route, messages inform: wrapping must not corrupt the text the
// API tier hands back to the caller.
func TestErrorKeepsMessageAndKind(t *testing.T) {
	err := Errf(ErrNoSuchPath, "fs: file does not exist: %s", "/work/nope.txt")
	if !errors.Is(err, ErrNoSuchPath) {
		t.Fatal("kind not matchable with errors.Is")
	}
	if got := err.Error(); got != "fs: file does not exist: /work/nope.txt" {
		t.Fatalf("message picked up a wrapper prefix: %q", got)
	}
}

func TestNormalizeIsIdempotent(t *testing.T) {
	b := Bounds{
		DefaultTTL: 15 * time.Minute, MaxTTL: 24 * time.Hour,
		MaxExecTimeout: 5 * time.Minute, MaxSteps: 1000, MaxOutputBytes: 4096,
	}
	var spec Spec
	if err := json.Unmarshal([]byte(`{"capabilities":{}}`), &spec); err != nil {
		t.Fatal(err)
	}
	if err := spec.Normalize(b, nil); err != nil {
		t.Fatal(err)
	}
	first := spec
	// The API tier normalizes and persists; the node normalizes what it
	// receives. The second pass has to be a no-op or the two tiers
	// would disagree about the instance's own limits.
	if err := spec.Normalize(b, nil); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, spec) {
		t.Fatalf("second normalize changed the spec:\n have %+v\n want %+v", spec, first)
	}
	if spec.TTLSeconds != 900 || spec.Limits.MaxSteps != 1000 || spec.Limits.MaxOutputBytes != 4096 {
		t.Fatalf("defaults/clamps wrong: %+v", spec)
	}
}

func TestNormalizeRejects(t *testing.T) {
	b := Bounds{DefaultTTL: time.Minute, MaxTTL: time.Hour, MaxExecTimeout: time.Minute}
	cases := []struct{ name, spec, want string }{
		{"ttl too long", `{"ttl_seconds":999999}`, "ttl_seconds exceeds"},
		{"timeout too long", `{"limits":{"exec_timeout_ms":999999999}}`, "exec_timeout_ms exceeds"},
		{"bad env name", `{"env":{"lower":"x"}}`, "invalid name"},
		{"bad secret name", `{"secrets":{"lowercase":{"value":"v"}}}`, "invalid name"},
		{"env/secret overlap", `{"env":{"T":"x"},"secrets":{"T":{"value":"v"}}}`, "also used by env"},
	}
	for _, tc := range cases {
		var spec Spec
		if err := json.Unmarshal([]byte(tc.spec), &spec); err != nil {
			t.Fatal(err)
		}
		err := spec.Normalize(b, nil)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: want error containing %q, got %v", tc.name, tc.want, err)
		}
		if err != nil && !errors.Is(err, ErrBadSpec) {
			t.Errorf("%s: error is not ErrBadSpec: %v", tc.name, err)
		}
	}
}
