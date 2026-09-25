package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// --- prompt endpoint ---

func TestPromptEndpoint(t *testing.T) {
	e := newEnvWith(t, apiCipher(t))
	cookies := login(e, "a@x.com")
	csrf := map[string]string{"X-Requested-With": "calcside"}
	_, m, _ := e.req("POST", "/api/v1/keys", `{"name":"k"}`, csrf, cookies)
	bearer := map[string]string{"Authorization": "Bearer " + m["secret"].(string)}

	spec := `{"ttl_seconds":900,"capabilities":{"fs":{},"net":{"allow_hosts":["example.com"]},"io":{}},"env":{"REGION":"us"},"secrets":{"T":{"value":"SUPERSECRET_XYZ","allowed_domains":["example.com"]}}}`
	code, m, _ := e.req("POST", "/api/v1/instances", spec, bearer, nil)
	if code != 201 {
		t.Fatalf("create instance: %d %v", code, m)
	}
	instID := m["instance"].(map[string]any)["id"].(string)

	code, m, _ = e.req("GET", "/api/v1/instances/"+instID+"/prompt", "", bearer, nil)
	if code != 200 {
		t.Fatalf("prompt: %d %v", code, m)
	}
	prompt := m["prompt"].(string)
	if m["instance_id"] != instID {
		t.Fatalf("instance_id: %v", m["instance_id"])
	}
	tools := m["tools"].(map[string]any)
	if tools["exec"] != "calcside_exec" || tools["read_file"] != "calcside_read_file" {
		t.Fatalf("tools: %v", tools)
	}
	caps := m["capabilities"].([]any)
	if len(caps) != 3 {
		t.Fatalf("capabilities: %v", caps)
	}
	for _, want := range []string{"calcside_exec", "#### `fs`", "#### `net`", "example.com", "`T`", "REGION"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q", want)
		}
	}
	// secret value must never leak
	if strings.Contains(prompt, "SUPERSECRET_XYZ") {
		t.Fatal("prompt leaked secret value")
	}

	// custom prefix
	code, m, _ = e.req("GET", "/api/v1/instances/"+instID+"/prompt?tool_prefix=sb_", "", bearer, nil)
	if code != 200 || m["tools"].(map[string]any)["exec"] != "sb_exec" {
		t.Fatalf("prefix: %d %v", code, m)
	}
	if !strings.Contains(m["prompt"].(string), "`sb_exec`") {
		t.Fatal("prefix not applied to prompt text")
	}

	// bad prefix
	code, m, _ = e.req("GET", "/api/v1/instances/"+instID+"/prompt?tool_prefix=bad%20prefix!", "", bearer, nil)
	if code != 400 || m["error"].(map[string]any)["code"] != "bad_request" {
		t.Fatalf("bad prefix: %d %v", code, m)
	}
	// too long
	code, _, _ = e.req("GET", "/api/v1/instances/"+instID+"/prompt?tool_prefix=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "", bearer, nil)
	if code != 400 {
		t.Fatalf("long prefix: expected 400, got %d", code)
	}
}

func TestPromptOtherUser404(t *testing.T) {
	e := newEnv(t)
	cookiesA := login(e, "a@x.com")
	csrf := map[string]string{"X-Requested-With": "calcside"}
	_, m, _ := e.req("POST", "/api/v1/keys", `{"name":"k"}`, csrf, cookiesA)
	bearerA := map[string]string{"Authorization": "Bearer " + m["secret"].(string)}
	_, m, _ = e.req("POST", "/api/v1/instances", `{}`, bearerA, nil)
	instID := m["instance"].(map[string]any)["id"].(string)

	cookiesB := login(e, "b@x.com")
	_, m, _ = e.req("POST", "/api/v1/keys", `{"name":"k"}`, csrf, cookiesB)
	bearerB := map[string]string{"Authorization": "Bearer " + m["secret"].(string)}
	code, _, _ := e.req("GET", "/api/v1/instances/"+instID+"/prompt", "", bearerB, nil)
	if code != 404 {
		t.Fatalf("other user's prompt: expected 404, got %d", code)
	}
}

func TestPromptNonRunning(t *testing.T) {
	e := newEnv(t)
	cookies := login(e, "a@x.com")
	csrf := map[string]string{"X-Requested-With": "calcside"}
	_, m, _ := e.req("POST", "/api/v1/keys", `{"name":"k"}`, csrf, cookies)
	bearer := map[string]string{"Authorization": "Bearer " + m["secret"].(string)}
	_, m, _ = e.req("POST", "/api/v1/instances", `{"capabilities":{"io":{}}}`, bearer, nil)
	instID := m["instance"].(map[string]any)["id"].(string)

	// deleted → tombstone status deleted → 409 not_running
	code, _, _ := e.req("DELETE", "/api/v1/instances/"+instID, "", bearer, nil)
	if code != 200 {
		t.Fatalf("delete: %d", code)
	}
	code, m, _ = e.req("GET", "/api/v1/instances/"+instID+"/prompt", "", bearer, nil)
	if code != 409 || m["error"].(map[string]any)["code"] != "not_running" {
		t.Fatalf("prompt after delete: expected 409 not_running, got %d %v", code, m)
	}

	// expired but still known → 409 not_running
	_, m, _ = e.req("POST", "/api/v1/instances", `{"capabilities":{"io":{}}}`, bearer, nil)
	instID2 := m["instance"].(map[string]any)["id"].(string)
	in, _ := e.mgr.Get(context.Background(), instID2)
	in.ExpiresAt = time.Now().Add(-time.Second)
	e.mgr.Reap(context.Background())
	code, m, _ = e.req("GET", "/api/v1/instances/"+instID2+"/prompt", "", bearer, nil)
	if code != http.StatusConflict || m["error"].(map[string]any)["code"] != "not_running" {
		t.Fatalf("expired prompt: expected 409 not_running, got %d %v", code, m)
	}
}
