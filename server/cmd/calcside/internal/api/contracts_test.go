package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

var csrf = map[string]string{"X-Requested-With": "calcside"}

func TestInferContract(t *testing.T) {
	e := newEnv(t)
	cookies := login(e, "u@x.com")

	code, m, _ := e.req("POST", "/api/v1/editor/contracts/infer",
		`{"sample":"{\"n\":1,\"ok\":true,\"name\":\"x\",\"big\":9007199254740993,\"arr\":[1,2.5],\"obj\":{\"k\":null},\"tags\":[]}"}`,
		csrf, cookies)
	if code != http.StatusOK {
		t.Fatalf("infer: %d %v", code, m)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(m["schema"].(string)), &out); err != nil {
		t.Fatal(err)
	}
	if out["type"] != "object" || out["additionalProperties"] != true {
		t.Fatalf("root: %v", out)
	}
	props := out["properties"].(map[string]any)
	if props["n"].(map[string]any)["type"] != "integer" ||
		props["big"].(map[string]any)["type"] != "integer" ||
		props["arr"].(map[string]any)["type"] != "array" ||
		props["obj"].(map[string]any)["type"] != "object" {
		t.Fatalf("props: %v", props)
	}
	if arr := props["arr"].(map[string]any); arr["items"].(map[string]any)["anyOf"] == nil {
		t.Fatalf("array anyOf: %v", arr)
	}
	if len(out["required"].([]any)) != 7 {
		t.Fatalf("required: %v", out["required"])
	}
	// Errors are generic 400 — no echo of sample keys/values.
	for _, body := range []string{
		`{"sample":"[1,2]"}`,
		`{"sample":"{bad"}`,
		`{"sample":"hello"}`,
		`{"sample":"` + strings.Repeat(" ", 70000) + `{}"}`,
	} {
		code, m, _ = e.req("POST", "/api/v1/editor/contracts/infer", body, csrf, cookies)
		if code != http.StatusBadRequest {
			t.Fatalf("reject %q: %d %v", body[:min(40, len(body))], code, m)
		}
	}
	// Unauthenticated -> 401.
	code, _, _ = e.req("POST", "/api/v1/editor/contracts/infer",
		`{"sample":"{}"}`, csrf, nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("unauth: %d", code)
	}
}

func TestValidateContract(t *testing.T) {
	e := newEnv(t)
	cookies := login(e, "u@x.com")

	code, m, _ := e.req("POST", "/api/v1/editor/contracts/validate",
		`{"schema":"{\"type\":\"object\",\"properties\":{\"a\":{\"type\":\"integer\"}}}"}`,
		csrf, cookies)
	if code != http.StatusOK || !strings.Contains(m["schema"].(string), `"type":"object"`) {
		t.Fatalf("validate: %d %v", code, m)
	}
	for _, body := range []string{
		`{"schema":"{bad"}`,
		`{"schema":"{\"$ref\":\"x\"}"}`,
		`{"schema":"[1]"}`,
		`{"schema":"{\"type\":\"` + strings.Repeat("a", 70000) + `\"}"}`,
	} {
		code, _, _ = e.req("POST", "/api/v1/editor/contracts/validate", body, csrf, cookies)
		if code != http.StatusBadRequest {
			t.Fatalf("reject %q: %d", body[:min(40, len(body))], code)
		}
	}
	code, _, _ = e.req("POST", "/api/v1/editor/contracts/validate",
		`{"schema":"{}"}`, csrf, nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("unauth: %d", code)
	}
	// CSRF: session-cookie request without the header marker -> 403.
	code, _, _ = e.req("POST", "/api/v1/editor/contracts/validate",
		`{"schema":"{}"}`, nil, cookies)
	if code != http.StatusForbidden {
		t.Fatalf("csrf: %d", code)
	}
	// HTTP body over the global 1 MiB cap -> 413.
	code, _, _ = e.req("POST", "/api/v1/editor/contracts/validate",
		`{"schema":"`+strings.Repeat(" ", 1<<20)+`{}"}`, csrf, cookies)
	if code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize body: %d", code)
	}
	// Numeric literal precision survives canonicalization untouched.
	code, m, _ = e.req("POST", "/api/v1/editor/contracts/validate",
		`{"schema":"{\"type\":\"integer\",\"minimum\":9007199254740993}"}`, csrf, cookies)
	if code != http.StatusOK || !strings.Contains(m["schema"].(string), "9007199254740993") {
		t.Fatalf("precision: %d %v", code, m)
	}
}
