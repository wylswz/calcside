package api

import (
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// Golden coverage for the exact wire shape of every endpoint. Golden
// files live in testdata/golden/<case>.json and hold
// {"status": <int>, "body": <normalized JSON>}; regenerate with
// `go test ./internal/api -run TestGolden -update`.
var goldenUpdate = flag.Bool("update", false, "regenerate golden response files")

var goldenDir = filepath.Join("testdata", "golden")

// idRe matches generated resource ids (usr_/key_/ins_/exe_/pol_/sec_/aud_
// + base62). Collected from every raw response body so ids embedded in
// free text (prompt, audit args) are found too.
//
// Known blind spot: all ids of one kind collapse to the same placeholder
// (every instance is "<id:ins>"), so golden cannot catch id-swap bugs —
// e.g. an endpoint returning a *different* execution's id passes. Golden
// covers wire format, not business correctness; behavior tests cover ids.
var idRe = regexp.MustCompile(`\b(usr|key|ins|exe|pol|sec|aud)_[A-Za-z0-9]{10,}`)

// numKeys are nondeterministic numeric fields normalized to "<num>".
// size, output_bytes and code_sha256 are deterministic and stay real.
var numKeys = map[string]bool{"duration_ms": true, "steps": true, "mtime": true}

type goldenRun struct {
	t   *testing.T
	ids map[string]string // real id -> "<id:xxx>"
}

func newGoldenRun(t *testing.T) *goldenRun {
	return &goldenRun{t: t, ids: map[string]string{}}
}

// req performs the request, records any ids found in the raw body, and
// stores/checks the normalized {"status","body"} against the golden.
func (g *goldenRun) req(e *env, name, method, path, body string, hdrs map[string]string, cookies []*http.Cookie) {
	g.t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	r, err := http.NewRequest(method, e.srv.URL+path, rdr)
	if err != nil {
		g.t.Fatal(err)
	}
	for k, v := range hdrs {
		r.Header.Set(k, v)
	}
	for _, c := range cookies {
		r.AddCookie(c)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		g.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	for _, m := range idRe.FindAllString(string(data), -1) {
		g.ids[m] = "<id:" + m[:3] + ">"
	}
	var parsed any
	if err := json.Unmarshal(data, &parsed); err != nil {
		parsed = string(data)
	}
	doc := map[string]any{"status": resp.StatusCode, "body": g.norm(parsed, "")}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		g.t.Fatal(err)
	}
	g.check(name, out)
}

func (g *goldenRun) check(name string, got []byte) {
	g.t.Helper()
	p := filepath.Join(goldenDir, name+".json")
	if *goldenUpdate {
		if err := os.MkdirAll(goldenDir, 0o755); err != nil {
			g.t.Fatal(err)
		}
		if err := os.WriteFile(p, append(got, '\n'), 0o644); err != nil {
			g.t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(p)
	if err != nil {
		g.t.Fatalf("%s: %v (run with -update)", name, err)
	}
	if string(want) != string(append(got, '\n')) {
		g.t.Fatalf("%s: response drifted\nwant:\n%s\ngot:\n%s", name, want, append(got, '\n'))
	}
}

// norm rewrites leaf values only — keys are never added or dropped.
func (g *goldenRun) norm(v any, key string) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, vv := range t {
			out[k] = g.norm(vv, k)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, vv := range t {
			out[i] = g.norm(vv, key)
		}
		return out
	case string:
		s := t
		// longest first so overlapping ids can't partially mask
		ids := make([]string, 0, len(g.ids))
		for id := range g.ids {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return len(ids[i]) > len(ids[j]) })
		for _, id := range ids {
			s = strings.ReplaceAll(s, id, g.ids[id])
		}
		if strings.HasPrefix(s, "cs_") {
			return "<secret>"
		}
		if _, err := time.Parse(time.RFC3339, s); err == nil {
			return "<ts>"
		}
		return s
	case float64:
		if numKeys[key] {
			return "<num>"
		}
		return v
	default:
		return v
	}
}

// TestGolden drives every response-bearing endpoint through a fully
// featured env (vault + net on) plus a cipher-less env for the 503
// family, snapshotting normalized status+body per case.
func TestGolden(t *testing.T) {
	e := newEnvWith(t, apiCipher(t))
	g := newGoldenRun(t)

	cookies := login(e, "a@x.com")
	csrf := map[string]string{"X-Requested-With": "calcside"}

	g.req(e, "healthz", "GET", "/healthz", "", nil, nil)
	g.req(e, "auth_config", "GET", "/api/v1/auth/config", "", nil, nil)
	g.req(e, "unauth_401", "GET", "/api/v1/me", "", nil, nil)
	g.req(e, "me", "GET", "/api/v1/me", "", nil, cookies)
	g.req(e, "capabilities", "GET", "/api/v1/capabilities", "", nil, cookies)
	g.req(e, "extensions", "GET", "/api/v1/extensions", "", nil, cookies)

	// api keys (session only)
	g.req(e, "csrf_403", "POST", "/api/v1/keys", `{"name":"k"}`, nil, cookies)
	g.req(e, "keys_create", "POST", "/api/v1/keys", `{"name":"golden","expires_in_seconds":3600}`, csrf, cookies)
	g.req(e, "keys_list", "GET", "/api/v1/keys", "", nil, cookies)

	// bearer for instance APIs; created via a second key so the first
	// can be deleted below
	var m map[string]any
	_, m, _ = e.req("POST", "/api/v1/keys", `{"name":"bearer"}`, csrf, cookies)
	bearer := map[string]string{"Authorization": "Bearer " + m["secret"].(string)}
	g.req(e, "keys_session_403", "GET", "/api/v1/keys", "", bearer, nil)
	g.req(e, "secrets_session_403", "GET", "/api/v1/secrets", "", bearer, nil)

	// instances
	spec := `{"ttl_seconds":900,"labels":{"team":"golden"},"capabilities":{"fs":{},"io":{}},"env":{"REGION":"us-east-1"},"limits":{"exec_timeout_ms":30000,"max_steps":1000000}}`
	g.req(e, "instance_create", "POST", "/api/v1/instances", spec, bearer, nil)
	_, m, _ = e.req("GET", "/api/v1/instances", "", bearer, nil)
	instID := m["instances"].([]any)[0].(map[string]any)["id"].(string)
	g.req(e, "instance_list", "GET", "/api/v1/instances", "", bearer, nil)
	g.req(e, "instance_get", "GET", "/api/v1/instances/"+instID, "", bearer, nil)
	g.req(e, "instance_keepalive", "POST", "/api/v1/instances/"+instID+"/keepalive", "", bearer, nil)
	g.req(e, "instance_list_status_bad", "GET", "/api/v1/instances?status=bogus", "", bearer, nil)
	g.req(e, "instance_get_missing", "GET", "/api/v1/instances/ins_000000000000000000000000", "", bearer, nil)

	// files on an instance without the fs capability -> 400 no_fs
	_, m, _ = e.req("POST", "/api/v1/instances", `{"capabilities":{"io":{}}}`, bearer, nil)
	noFSID := m["instance"].(map[string]any)["id"].(string)
	g.req(e, "files_no_fs", "GET", "/api/v1/instances/"+noFSID+"/files", "", bearer, nil)

	// exec: success, script error, too large
	g.req(e, "exec_ok", "POST", "/api/v1/instances/"+instID+"/exec",
		`{"code":"fs.write('hello.txt','world')\nprint(fs.read('hello.txt'))"}`, bearer, nil)
	g.req(e, "exec_err", "POST", "/api/v1/instances/"+instID+"/exec",
		`{"code":"print(undefined_name)"}`, bearer, nil)
	g.req(e, "exec_too_large", "POST", "/api/v1/instances/"+instID+"/exec",
		`{"code":"`+strings.Repeat("x", 256<<10+1)+`"}`, bearer, nil)

	// files: dir, file, missing
	g.req(e, "files_dir", "GET", "/api/v1/instances/"+instID+"/files?path=/work", "", bearer, nil)
	g.req(e, "files_file", "GET", "/api/v1/instances/"+instID+"/files?path=/work/hello.txt", "", bearer, nil)
	g.req(e, "files_missing", "GET", "/api/v1/instances/"+instID+"/files?path=/work/nope.txt", "", bearer, nil)

	// prompt (default + custom prefix)
	g.req(e, "prompt", "GET", "/api/v1/instances/"+instID+"/prompt", "", bearer, nil)
	g.req(e, "prompt_prefix", "GET", "/api/v1/instances/"+instID+"/prompt?tool_prefix=sb_", "", bearer, nil)
	g.req(e, "prompt_bad_prefix", "GET", "/api/v1/instances/"+instID+"/prompt?tool_prefix=bad%20prefix!", "", bearer, nil)

	// executions
	g.req(e, "executions_list", "GET", "/api/v1/instances/"+instID+"/executions", "", bearer, nil)
	_, m, _ = e.req("GET", "/api/v1/instances/"+instID+"/executions", "", bearer, nil)
	execID := m["executions"].([]any)[0].(map[string]any)["id"].(string)
	g.req(e, "execution_get", "GET", "/api/v1/executions/"+execID, "", bearer, nil)

	// audit (flush recorder first); doubles as audit-record equivalence
	// evidence for the fs accessor refactor
	e.rec.Close()
	e.rec = nil
	g.req(e, "audit_list", "GET", "/api/v1/audit?instance_id="+instID, "", bearer, nil)

	// policies
	valid := `package calcside.hooks
deny contains "x" if { input.op == "read" }`
	g.req(e, "policy_validate_ok", "POST", "/api/v1/policies/validate", `{"rego":`+jsonStr(valid)+`}`, csrf, cookies)
	g.req(e, "policy_validate_bad", "POST", "/api/v1/policies/validate", `{"rego":"not rego at all"}`, csrf, cookies)
	g.req(e, "policy_create", "POST", "/api/v1/policies", `{"name":"p","rego":`+jsonStr(valid)+`}`, csrf, cookies)
	_, m, _ = e.req("GET", "/api/v1/policies", "", nil, cookies)
	polID := m["policies"].([]any)[0].(map[string]any)["id"].(string)
	g.req(e, "policy_list", "GET", "/api/v1/policies", "", nil, cookies)
	g.req(e, "policy_get", "GET", "/api/v1/policies/"+polID, "", nil, cookies)
	g.req(e, "policy_update", "PUT", "/api/v1/policies/"+polID, `{"enabled":false}`, csrf, cookies)
	g.req(e, "policy_create_bad_rego", "POST", "/api/v1/policies", `{"name":"bad","rego":"nope"}`, csrf, cookies)
	g.req(e, "policy_delete", "DELETE", "/api/v1/policies/"+polID, "", csrf, cookies)

	// secrets
	g.req(e, "secret_create", "POST", "/api/v1/secrets",
		`{"name":"T","value":"s3cr3t","allowed_domains":["api.x.com"]}`, csrf, cookies)
	_, m, _ = e.req("GET", "/api/v1/secrets", "", nil, cookies)
	secID := m["secrets"].([]any)[0].(map[string]any)["id"].(string)
	g.req(e, "secret_list", "GET", "/api/v1/secrets", "", nil, cookies)
	g.req(e, "secret_update", "PUT", "/api/v1/secrets/"+secID, `{"value":"rotated-value"}`, csrf, cookies)
	g.req(e, "secret_delete", "DELETE", "/api/v1/secrets/"+secID, "", csrf, cookies)

	// cross-user 404
	cookiesB := login(e, "b@x.com")
	_, m, _ = e.req("POST", "/api/v1/keys", `{"name":"k"}`, csrf, cookiesB)
	bearerB := map[string]string{"Authorization": "Bearer " + m["secret"].(string)}
	g.req(e, "instance_get_other_user", "GET", "/api/v1/instances/"+instID, "", bearerB, nil)

	// delete then exec -> 409 not_running (tombstone)
	g.req(e, "instance_delete", "DELETE", "/api/v1/instances/"+instID, "", bearer, nil)
	g.req(e, "exec_not_running", "POST", "/api/v1/instances/"+instID+"/exec", `{"code":"print(1)"}`, bearer, nil)

	// key revoke (own key id)
	_, m, _ = e.req("GET", "/api/v1/keys", "", nil, cookies)
	keyID := m["keys"].([]any)[0].(map[string]any)["id"].(string)
	g.req(e, "key_delete", "DELETE", "/api/v1/keys/"+keyID, "", csrf, cookies)

	// 503 family: vault disabled
	e2 := newEnvWith(t, nil)
	cookies2 := login(e2, "a@x.com")
	g.req(e2, "secrets_disabled_503", "GET", "/api/v1/secrets", "", nil, cookies2)
}
