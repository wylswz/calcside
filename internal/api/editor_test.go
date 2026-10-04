package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func editorSymbols(t *testing.T, value any) map[string]map[string]any {
	t.Helper()
	items, ok := value.([]any)
	if !ok {
		t.Fatalf("expected symbols array, got %T", value)
	}
	out := map[string]map[string]any{}
	for _, item := range items {
		s := item.(map[string]any)
		out[s["name"].(string)] = s
	}
	return out
}

func TestEditorMetadata(t *testing.T) {
	e := newEnv(t)
	if code, _, _ := e.req("GET", "/api/v1/editor/metadata", "", nil, nil); code != 401 {
		t.Fatalf("unauthenticated metadata: %d", code)
	}
	cookies := login(e, "editor@x.com")
	code, body, _ := e.req("GET", "/api/v1/editor/metadata", "", nil, cookies)
	if code != 200 {
		t.Fatalf("metadata: %d %v", code, body)
	}
	symbols := editorSymbols(t, body["rego"])
	for _, name := range []string{"input.phase", "input.user.id", "input.instance.labels", "input.result.meta", "startswith", "json.unmarshal"} {
		if symbols[name] == nil {
			t.Errorf("missing %s", name)
		}
	}
	for _, name := range []string{"http.send", "opa.runtime", "net.lookup_ip_addr", "trace", "print"} {
		if symbols[name] != nil {
			t.Errorf("blocked builtin offered: %s", name)
		}
	}
	if _, ok := body["capabilities"].([]any); !ok {
		t.Fatalf("missing capability docs: %v", body)
	}
}

func TestInstanceCompletions(t *testing.T) {
	e := newEnvWith(t, apiCipher(t))
	cookies := login(e, "editor@x.com")
	csrf := map[string]string{"X-Requested-With": "calcside"}
	code, body, _ := e.req("POST", "/api/v1/instances", `{"capabilities":{"fs":{}},"env":{"REGION":"ENV_VALUE_NOT_FOR_COMPLETION"},"secrets":{"TOKEN":{"value":"SECRET_NOT_FOR_COMPLETION","allowed_domains":["example.com"]}}}`, csrf, cookies)
	if code != 201 {
		t.Fatalf("create: %d %v", code, body)
	}
	id := body["instance"].(map[string]any)["id"].(string)
	path := "/api/v1/instances/" + id + "/completions"
	if code, _, _ := e.req("GET", path, "", nil, nil); code != 401 {
		t.Fatalf("unauthenticated completions: %d", code)
	}
	other := login(e, "other@x.com")
	if code, _, _ := e.req("GET", path, "", nil, other); code != 404 {
		t.Fatalf("foreign completions: %d", code)
	}
	program, _ := json.Marshal(map[string]string{"code": "answer = 42\nSECRET_NOT_FOR_COMPLETION = 1\ndef helper(path, suffix = 'SECRET_NOT_FOR_COMPLETION'):\n    return path + suffix\n"})
	if code, body, _ := e.req("POST", "/api/v1/instances/"+id+"/exec", string(program), csrf, cookies); code != 200 || body["error"] != nil {
		t.Fatalf("exec: %d %v", code, body)
	}
	code, body, _ = e.req("GET", path, "", nil, cookies)
	if code != 200 {
		t.Fatalf("completions: %d %v", code, body)
	}
	symbols := editorSymbols(t, body["symbols"])
	for _, name := range []string{"fs.read", "io.println", "json.encode", "math.sqrt", "len", "answer", "helper"} {
		if symbols[name] == nil {
			t.Errorf("missing %s", name)
		}
	}
	if symbols["net"] != nil || symbols["net.get"] != nil || symbols["open"] != nil {
		t.Fatal("unavailable bindings offered")
	}
	if symbols["fs.read"]["detail"] != "(path)" || symbols["helper"]["detail"] != "(path, suffix?)" {
		t.Fatalf("signatures: %v %v", symbols["fs.read"], symbols["helper"])
	}
	if keys, ok := body["env_keys"].([]any); !ok || len(keys) != 1 || keys[0] != "REGION" {
		t.Fatalf("env keys: %v", body["env_keys"])
	}
	encoded, _ := json.Marshal(body)
	for _, forbidden := range []string{"SECRET_NOT_FOR_COMPLETION", "ENV_VALUE_NOT_FOR_COMPLETION", `"variables"`} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("completion response exposes %s", forbidden)
		}
	}
	program, _ = json.Marshal(map[string]string{"code": "fs = 1\nenv = {}"})
	e.req("POST", "/api/v1/instances/"+id+"/exec", string(program), csrf, cookies)
	_, body, _ = e.req("GET", path, "", nil, cookies)
	if editorSymbols(t, body["symbols"])["fs.read"] == nil || len(body["env_keys"].([]any)) != 1 {
		t.Fatal("bindings reinjected by the next exec are missing")
	}
	e.req("DELETE", "/api/v1/instances/"+id, "", csrf, cookies)
	if code, _, _ := e.req("GET", path, "", nil, cookies); code != 409 {
		t.Fatalf("deleted completions: %d", code)
	}
}

func TestCompletionPathsNeverReadFiles(t *testing.T) {
	e := newDevEnv(t, nil)
	csrf := map[string]string{"X-Requested-With": "calcside"}
	_, body, _ := e.req("POST", "/api/v1/instances", `{"capabilities":{"fs":{}}}`, csrf, nil)
	id := body["instance"].(map[string]any)["id"].(string)
	e.req("POST", "/api/v1/instances/"+id+"/exec", `{"code":"fs.write('/work/a', 'FILE_CONTENT_NOT_FOR_COMPLETION')"}`, csrf, nil)
	code, body, _ := e.req("GET", "/api/v1/instances/"+id+"/files?path=/work/a&list_only=true", "", nil, nil)
	if code != 400 || body["content"] != nil {
		t.Fatalf("completion path read a file: %d %v", code, body)
	}
	code, body, _ = e.req("GET", "/api/v1/instances/"+id+"/files?path=/work&list_only=true", "", nil, nil)
	if code != 200 || len(body["entries"].([]any)) != 1 {
		t.Fatalf("completion listing: %d %v", code, body)
	}
	policy := `{"name":"deny_listing","rego":"package calcside.hooks\ndeny contains \"no list\" if { input.op == \"list\" }"}`
	if code, body, _ := e.req("POST", "/api/v1/policies", policy, csrf, nil); code != 201 {
		t.Fatalf("policy: %d %v", code, body)
	}
	_, body, _ = e.req("POST", "/api/v1/instances", `{"capabilities":{"fs":{}},"policies":["deny_listing"]}`, csrf, nil)
	id = body["instance"].(map[string]any)["id"].(string)
	if code, _, _ := e.req("GET", "/api/v1/instances/"+id+"/files?path=/work&list_only=true", "", nil, nil); code != 400 {
		t.Fatalf("completion bypassed list policy: %d", code)
	}
}
