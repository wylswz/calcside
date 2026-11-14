package runtime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"calcside/internal/types"
)

func servingFixture() *PrepareRequest {
	spec, _ := json.Marshal(Spec{
		TTLSeconds:   300,
		Labels:       map[string]string{"env": "prod"},
		Capabilities: map[string]json.RawMessage{"fs": json.RawMessage(`{"quota_bytes":1048576}`)},
		Limits:       Limits{ExecTimeoutMs: 5000, MaxSteps: 1e6, MaxOutputBytes: 1 << 20},
		Env:          map[string]string{"K": "v"},
		Secrets:      map[string]SecretSpec{"k": {Value: "supers3kr3t"}},
		Policies:     []string{"builtin.block_private_network"},
	})
	return &PrepareRequest{
		CreateRequest: CreateRequest{
			InstanceID: "rep-1",
			Owner:      Owner{UserID: "u1", Email: "u@x"},
			Labels:     map[string]string{"env": "prod"},
			Spec:       spec,
			Policies:   PolicyBundle{Global: map[string]string{"g": "package g"}, User: map[string]string{"p": "package p"}},
			Secrets:    []Secret{{Name: "k", Value: "supers3kr3t", Source: types.SecretInline}},
			ExpiresAt:  time.UnixMilli(1e13),
			Epoch:      7,
		},
		ReleaseID:  "rel-9",
		Generation: 3,
		PrepareID:  "prep-11",
		TimeoutMs:  4000,
		Program: ProgramBundle{
			Entrypoint:   "main.star",
			Sources:      map[string][]byte{"main.star": []byte("def main(input):\n    return {}")},
			Assets:       map[string][]byte{"d.bin": {0x00, 0xff, 0x10, 0x80}},
			InputSchema:  json.RawMessage(`{"type":"object","properties":{"n":{"type":"integer"}}}`),
			OutputSchema: json.RawMessage(`{}`),
		},
	}
}

func TestPrepareRequestJSONRoundtrip(t *testing.T) {
	req := servingFixture()
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	// CreateRequest is flattened: its fields sit at top level, not
	// under a "create_request" key.
	if strings.Contains(string(raw), "create_request") {
		t.Fatalf("embedded request not flattened: %s", raw[:200])
	}
	var back PrepareRequest
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.InstanceID != "rep-1" || back.PrepareID != "prep-11" || back.ReleaseID != "rel-9" || back.Generation != 3 || back.Epoch != 7 {
		t.Fatalf("ids lost: %+v", back.CreateRequest)
	}
	if !back.ExpiresAt.Equal(req.ExpiresAt) {
		t.Fatalf("expires_at lost: %v", back.ExpiresAt)
	}
	// Spec round-trips as raw bytes including the policies list.
	var spec Spec
	if err := json.Unmarshal(back.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	if len(spec.Policies) != 1 || spec.Policies[0] != "builtin.block_private_network" {
		t.Fatalf("spec policies lost: %v", spec.Policies)
	}
	if spec.Limits.MaxSteps != 1e6 {
		t.Fatalf("spec limits lost: %+v", spec.Limits)
	}
	// Arbitrary bytes survive through base64.
	got := back.Program.Assets["d.bin"]
	if !bytes.Equal(got, []byte{0x00, 0xff, 0x10, 0x80}) {
		t.Fatalf("asset bytes corrupted: %v", got)
	}
	// Schemas are raw objects, not quoted strings.
	var inSchema map[string]any
	if err := json.Unmarshal(back.Program.InputSchema, &inSchema); err != nil {
		t.Fatal(err)
	}
	if inSchema["type"] != "object" {
		t.Fatalf("schema not raw: %s", back.Program.InputSchema)
	}
}

func TestInvokeRequestJSONRoundtrip(t *testing.T) {
	req := &InvokeRequest{
		InstanceID: "rep-1",
		Owner:      Owner{UserID: "u1"},
		Actor:      Owner{UserID: "u2"},
		ReleaseID:  "rel-9",
		Generation: 3,
		Epoch:      7,
		InvokeID:   "inv-5",
		Input:      json.RawMessage(`{"big":9007199254740993}`),
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var back InvokeRequest
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.InvokeID != "inv-5" || back.Actor.UserID != "u2" || back.Epoch != 7 {
		t.Fatalf("invoke lost fields: %+v", back)
	}
	// Large integers stay exact through RawMessage.
	if string(back.Input) != `{"big":9007199254740993}` {
		t.Fatalf("input mutated: %s", back.Input)
	}
}

func TestPrepareResponseJSONRoundtrip(t *testing.T) {
	res := &PrepareResponse{
		Result: ProgramResult{ID: "prep-11", Error: &ProgramError{Code: "invalid_program", Message: "generic"}, DurationMs: 12, Steps: 55},
		Audit:  AuditBatch{Events: []AuditEvent{{ExecID: "e1", Decision: types.DecisionDeny}}, Dropped: 0},
		NodeID: "node-1",
		Epoch:  7,
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var back PrepareResponse
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.Result.Error == nil || back.Result.Error.Code != "invalid_program" || back.Result.Steps != 55 {
		t.Fatalf("result lost: %+v", back.Result)
	}
	if len(back.Audit.Events) != 1 || back.Audit.Events[0].ExecID != "e1" {
		t.Fatalf("audit not preserved on failure: %+v", back.Audit)
	}
	if back.Result.Output != nil {
		t.Fatal("failed result carried output")
	}
}

// Every request logging path must emit metadata only -- no program
// contents, schemas, inputs, spec JSON or secret values.
func TestRequestLogSafety(t *testing.T) {
	req := servingFixture()
	markers := []string{
		"supers3kr3t",           // resolved secret value
		"def main(input)",       // source contents
		"d.bin",                 // asset filename
		"block_private_network", // policy source/names in spec
		"quota_bytes",           // spec JSON
		"9007199254740993",      // big number
	}
	var buf bytes.Buffer
	h := slog.NewJSONHandler(&buf, nil)
	l := slog.New(h)

	inv := &InvokeRequest{
		InstanceID: "rep-1", Owner: Owner{UserID: "u1"}, ReleaseID: "rel-9",
		Generation: 3, Epoch: 7, InvokeID: "inv-5",
		Input: json.RawMessage(`{"big":9007199254740993}`),
	}
	// Pointer AND value forms of every formatter must stay safe: a
	// value-receiver method set covers fmt's %v/%+v and slog for both.
	l.Info("prepare", "req", req)
	l.Info("prepare-val", "req", *req)
	l.Info("bundle", "b", req.Program)
	l.Info("invoke", "req", inv)
	l.Info("invoke-val", "req", *inv)
	l.Info("sprint", "a", slog.String("v",
		fmt.Sprint(req)+fmt.Sprintf("%+v", req)+fmt.Sprint(*req)+fmt.Sprintf("%+v", *req)+
			fmt.Sprint(inv)+fmt.Sprint(*inv)+fmt.Sprint(req.Program)))

	out := buf.String()
	for _, m := range markers {
		if strings.Contains(out, m) {
			t.Fatalf("log output leaked %q:\n%s", m, out)
		}
	}
	// Metadata that SHOULD appear.
	for _, want := range []string{"prep-11", "rel-9", "inv-5", "source_bytes", "input_bytes"} {
		if !strings.Contains(out, want) {
			t.Fatalf("log missing metadata %q", want)
		}
	}
	// fmt.Sprint of the request itself stays metadata-only.
	str := fmt.Sprint(req)
	if strings.Contains(str, "supers3kr3t") || strings.Contains(str, "def main") {
		t.Fatalf("String leaked: %s", str)
	}
}

// An explicit empty policies list must survive the round trip: nil
// selects defaults, [] selects none -- the distinction matters.
func TestPrepareRequestExplicitEmptyPolicies(t *testing.T) {
	req := servingFixture()
	spec, _ := json.Marshal(Spec{Policies: []string{}})
	req.Spec = spec
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var back PrepareRequest
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	var spec2 Spec
	if err := json.Unmarshal(back.Spec, &spec2); err != nil {
		t.Fatal(err)
	}
	if spec2.Policies == nil || len(spec2.Policies) != 0 {
		t.Fatalf("explicit empty policies not preserved: %#v", spec2.Policies)
	}
}
