package remote_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"calcside/internal/runtime"
	"calcside/internal/runtime/remote"
	"calcside/internal/types"
)

func TestServingRequestRejectionStatus(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		size       int
		body       string
		status     int
	}{
		{"prepare size", "/runtime/v1/prepare", (16 << 20) + 1, "", http.StatusRequestEntityTooLarge},
		{"exec size", "/runtime/v1/exec", (4 << 20) + 1, "", http.StatusRequestEntityTooLarge},
		{"malformed input", "/runtime/v1/invoke", 0, `{"input":"PRIVATE_BODY_MARKER`, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := testManager(t)
			t.Cleanup(m.StopReaper)
			body := []byte(tc.body)
			if tc.size > 0 {
				body = bytes.Repeat([]byte(" "), tc.size)
			}
			req := httptest.NewRequest(http.MethodPost, tc.path, bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			remote.SignRequest(testKey, "test", req, body, time.Now())
			w := httptest.NewRecorder()
			remote.NewHandler(m, testKey).ServeHTTP(w, req)
			if w.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", w.Code, tc.status, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "PRIVATE_BODY_MARKER") {
				t.Fatal("rejected request leaked its payload")
			}
			if tc.status == http.StatusBadRequest && !strings.Contains(w.Body.String(), "request body invalid") {
				t.Fatalf("binding failure is not generic: %s", w.Body.String())
			}
		})
	}
}

func TestServingPolicyAndRedaction(t *testing.T) {
	m := testManager(t)
	t.Cleanup(m.StopReaper)
	srv := httptest.NewServer(remote.NewHandler(m, testKey))
	defer srv.Close()
	cl, err := remote.NewDirect(srv.URL, srv.Client(), testKey, "test")
	if err != nil {
		t.Fatal(err)
	}
	prep := prepareReq("policy-denied-main", nil)
	prep.Policies.Global = map[string]string{"deny.rego": `package calcside.hooks
deny contains "blocked" if {
    input.phase == "before"
    input.capability == "fs"
    input.op == "write"
}`}
	prep.Program.Sources["main.star"] = []byte("def main(i):\n    fs.write(\"x\", \"private\")\n    return {}")
	prep.Program.OutputSchema = json.RawMessage(`{}`)
	ready, err := cl.Prepare(context.Background(), prep)
	if err != nil {
		t.Fatal(err)
	}
	if ready.Result.Error != nil {
		t.Fatalf("prepare: %+v", ready.Result.Error)
	}
	inv := &runtime.InvokeRequest{
		InstanceID: prep.InstanceID, Owner: prep.Owner, Actor: runtime.Owner{UserID: "actor-policy"},
		ReleaseID: prep.ReleaseID, Generation: prep.Generation, InvokeID: "policy-call", Input: json.RawMessage(`{"name":"x"}`),
	}
	out, err := cl.Invoke(context.Background(), inv)
	if err != nil {
		t.Fatal(err)
	}
	if out.Result.Error == nil || out.Result.Error.Code != "policy_denied" || out.Result.Output != nil {
		t.Fatalf("policy failure lost: %+v", out.Result)
	}
	found := false
	for _, event := range out.Audit.Events {
		if event.UserID == inv.Actor.UserID && event.Decision == types.DecisionDeny && event.Op == "write" {
			found = true
		}
	}
	if !found {
		t.Fatal("policy denial or actor audit lost")
	}

	prep = prepareReq("redacted-return", nil)
	prep.Secrets = []runtime.Secret{{Name: "TOKEN", Value: "credential-secret-marker", Source: types.SecretInline}}
	prep.Program.Sources["main.star"] = []byte(`def main(i): return "credential-secret-marker"`)
	prep.Program.OutputSchema = json.RawMessage(`{"type":"string"}`)
	ready, err = cl.Prepare(context.Background(), prep)
	if err != nil {
		t.Fatal(err)
	}
	if ready.Result.Error != nil {
		t.Fatalf("prepare redaction: %+v", ready.Result.Error)
	}
	inv.InstanceID, inv.ReleaseID, inv.Generation, inv.InvokeID = prep.InstanceID, prep.ReleaseID, prep.Generation, "redact-call"
	out, err = cl.Invoke(context.Background(), inv)
	if err != nil {
		t.Fatal(err)
	}
	if out.Result.Error != nil || !strings.Contains(string(out.Result.Output), "[REDACTED:TOKEN]") ||
		strings.Contains(string(out.Result.Output), "credential-secret-marker") {
		t.Fatalf("unsafe output: %+v", out.Result.Error)
	}
}
