package api

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// Module endpoints end-to-end: draft CRUD, validate, preview/publish
// CAS, version history, compare, restore — plus access isolation.
func TestModuleEndpoints(t *testing.T) {
	e := newEnv(t)
	cookies := login(e, "m@x.com")
	csrf := map[string]string{"X-Requested-With": "calcside", "Content-Type": "application/json"}

	code, m, _ := e.req("POST", "/api/v1/modules", `{"name":"strkit","files":{"u.star":"x = 1"}}`, csrf, cookies)
	if code != 200 {
		t.Fatalf("create: %d %v", code, m)
	}
	id := m["module"].(map[string]any)["id"].(string)
	dv := int64(m["module"].(map[string]any)["draft_version"].(float64))

	// Validate good + bad.
	code, m, _ = e.req("POST", "/api/v1/modules/validate", `{"files":{"u.star":"x = 1"}}`, csrf, cookies)
	if code != 200 || m["valid"] != true {
		t.Fatalf("validate: %d %v", code, m)
	}
	code, m, _ = e.req("POST", "/api/v1/modules/validate", `{"files":{"u.star":"load(\"nope.star\",\"x\")"}}`, csrf, cookies)
	if code != 200 || m["valid"] != false {
		t.Fatalf("validate bad: %d %v", code, m)
	}

	// Patch CAS.
	code, m, _ = e.req("PATCH", "/api/v1/modules/"+id, `{"draft_version":99,"files":{"u.star":"x = 2"}}`, csrf, cookies)
	if code != 409 {
		t.Fatalf("stale draft: %d %v", code, m)
	}
	code, m, _ = e.req("PATCH", "/api/v1/modules/"+id, `{"draft_version":`+itoa(dv)+`,"files":{"u.star":"x = 2"}}`, csrf, cookies)
	if code != 200 {
		t.Fatalf("patch: %d %v", code, m)
	}
	dv = int64(m["module"].(map[string]any)["draft_version"].(float64))

	// Preview.
	code, m, _ = e.req("POST", "/api/v1/modules/"+id+"/publish/preview", `{"draft_version":`+itoa(dv)+`}`, csrf, cookies)
	if code != 200 || m["candidate"] == nil {
		t.Fatalf("preview: %d %v", code, m)
	}
	digest := m["candidate"].(map[string]any)["digest"].(string)

	// Publish: missing digest/base → 400.
	code, m, _ = e.req("POST", "/api/v1/modules/"+id+"/publish", `{"draft_version":`+itoa(dv)+`,"expected_base":"","candidate":""}`, csrf, cookies)
	if code != 400 {
		t.Fatalf("missing candidate: %d %v", code, m)
	}
	code, m, _ = e.req("POST", "/api/v1/modules/"+id+"/publish", `{"draft_version":`+itoa(dv)+`,"candidate":"`+digest+`"}`, csrf, cookies)
	if code != 400 {
		t.Fatalf("missing base: %d %v", code, m)
	}
	code, m, _ = e.req("POST", "/api/v1/modules/"+id+"/publish",
		`{"draft_version":`+itoa(dv)+`,"expected_base":"","candidate":"`+digest+`","note":"first"}`, csrf, cookies)
	if code != 200 || m["version"] == nil {
		t.Fatalf("publish: %d %v", code, m)
	}
	if m["version"].(map[string]any)["version"].(float64) != 1 {
		t.Fatalf("version: %v", m)
	}

	// No-op identical publish.
	code, m, _ = e.req("POST", "/api/v1/modules/"+id+"/publish",
		`{"draft_version":`+itoa(dv)+`,"expected_base":"`+digest+`","candidate":"`+digest+`"}`, csrf, cookies)
	if code != 200 {
		t.Fatalf("noop publish: %d %v", code, m)
	}

	// History + compare.
	code, m, _ = e.req("GET", "/api/v1/modules/"+id+"/versions", "", nil, cookies)
	if code != 200 || len(m["versions"].([]any)) != 1 {
		t.Fatalf("versions: %d %v", code, m)
	}
	code, m, _ = e.req("GET", "/api/v1/modules/"+id+"/versions/compare?base=0&head=1", "", nil, cookies)
	if code != 200 || m["candidate"] == nil {
		t.Fatalf("compare: %d %v", code, m)
	}

	// Restore CAS.
	code, m, _ = e.req("PATCH", "/api/v1/modules/"+id, `{"draft_version":`+itoa(dv)+`,"files":{"u.star":"x = 9"}}`, csrf, cookies)
	if code != 200 {
		t.Fatalf("patch2: %d %v", code, m)
	}
	dv2 := int64(m["module"].(map[string]any)["draft_version"].(float64))
	code, m, _ = e.req("POST", "/api/v1/modules/"+id+"/versions/1/restore", `{"draft_version":`+itoa(dv2)+`}`, csrf, cookies)
	if code != 200 || m["module"].(map[string]any)["files"].(map[string]any)["u.star"] != "x = 2" {
		t.Fatalf("restore: %d %v", code, m)
	}

	// Delete.
	code, _, _ = e.req("DELETE", "/api/v1/modules/"+id, "", csrf, cookies)
	if code != 204 {
		t.Fatalf("delete: %d", code)
	}
	code, _, _ = e.req("GET", "/api/v1/modules/"+id, "", nil, cookies)
	if code != 404 {
		t.Fatalf("deleted visible: %d", code)
	}
}

// Module names are workspace-unique: duplicate create and colliding
// rename return a named 409 without touching the existing draft;
// the same name in another workspace is allowed.
func TestModuleNameUniqueness(t *testing.T) {
	e := newEnv(t)
	cookies := login(e, "dup@x.com")
	csrf := map[string]string{"X-Requested-With": "calcside", "Content-Type": "application/json"}

	code, m, _ := e.req("POST", "/api/v1/modules", `{"name":"dup","files":{"u.star":"x = 1"}}`, csrf, cookies)
	if code != 200 {
		t.Fatalf("create: %d %v", code, m)
	}
	id := m["module"].(map[string]any)["id"].(string)
	dv := int64(m["module"].(map[string]any)["draft_version"].(float64))

	// Duplicate create: 409 with the name-specific message.
	code, m, _ = e.req("POST", "/api/v1/modules", `{"name":"dup","files":{"u.star":"x = 2"}}`, csrf, cookies)
	if code != 409 || !strings.Contains(fmt.Sprint(m), "already exists") {
		t.Fatalf("duplicate create: %d %v", code, m)
	}

	// Rename collision: 409, and the losing draft keeps all its data.
	code, m, _ = e.req("POST", "/api/v1/modules", `{"name":"other","files":{"u.star":"x = 9"}}`, csrf, cookies)
	if code != 200 {
		t.Fatalf("second create: %d %v", code, m)
	}
	id2 := m["module"].(map[string]any)["id"].(string)
	code, m, _ = e.req("PATCH", "/api/v1/modules/"+id2,
		`{"draft_version":1,"name":"dup","files":{"u.star":"x = 10"}}`, csrf, cookies)
	if code != 409 || !strings.Contains(fmt.Sprint(m), "already exists") {
		t.Fatalf("rename collision: %d %v", code, m)
	}
	code, m, _ = e.req("GET", "/api/v1/modules/"+id2, "", nil, cookies)
	mod := m["module"].(map[string]any)
	if code != 200 || mod["name"] != "other" || mod["files"].(map[string]any)["u.star"] != "x = 9" {
		t.Fatalf("rename collision wrote draft data: %d %v", code, mod)
	}
	// The original draft is untouched — same name and draft_version.
	code, m, _ = e.req("GET", "/api/v1/modules/"+id, "", nil, cookies)
	mod = m["module"].(map[string]any)
	if code != 200 || mod["name"] != "dup" || int64(mod["draft_version"].(float64)) != dv {
		t.Fatalf("collision touched the other draft: %d %v", code, mod)
	}

	// The same name in another workspace is fine.
	other := login(e, "dup2@x.com")
	code, m, _ = e.req("POST", "/api/v1/modules", `{"name":"dup","files":{"u.star":"x = 3"}}`, csrf, other)
	if code != 200 {
		t.Fatalf("cross-workspace duplicate rejected: %d %v", code, m)
	}
}

// Cross-workspace and key scoping: another user sees nothing; a
// restricted/bound key is invisible on list/create/validate.
func TestModuleIsolation(t *testing.T) {
	e := newEnv(t)
	cookies := login(e, "iso@x.com")
	csrf := map[string]string{"X-Requested-With": "calcside", "Content-Type": "application/json"}
	code, m, _ := e.req("POST", "/api/v1/modules", `{"name":"strkit","files":{"u.star":"x = 1"}}`, csrf, cookies)
	if code != 200 {
		t.Fatalf("create: %d %v", code, m)
	}
	id := m["module"].(map[string]any)["id"].(string)

	other := login(e, "other@x.com")
	code, _, _ = e.req("GET", "/api/v1/modules/"+id, "", nil, other)
	if code != 404 {
		t.Fatalf("cross-ws get: %d", code)
	}
	code, m, _ = e.req("GET", "/api/v1/modules", "", nil, other)
	if code != 200 || len(m["modules"].([]any)) != 0 {
		t.Fatalf("cross-ws list: %d %v", code, m)
	}

	// Restricted and resource-bound keys are invisible on all module
	// ops — proven at the service layer in
	// service/module.TestModuleBoundKeyInvisible.
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

func TestModuleIncompleteAutosaveHTTP(t *testing.T) {
	e := newEnv(t)
	cookies := login(e, "autosave@x.com")
	headers := map[string]string{"X-Requested-With": "calcside", "Content-Type": "application/json"}
	code, body, _ := e.req("POST", "/api/v1/modules",
		`{"name":"unfinished","files":{"u.star":"x = 1"}}`, headers, cookies)
	if code != 200 {
		t.Fatalf("create: %d %v", code, body)
	}
	id := body["module"].(map[string]any)["id"].(string)
	code, body, _ = e.req("PATCH", "/api/v1/modules/"+id,
		`{"draft_version":1,"files":{"u.star":"def unfinished("}}`, headers, cookies)
	if code != 200 || body["module"].(map[string]any)["files"].(map[string]any)["u.star"] != "def unfinished(" {
		t.Fatalf("incomplete draft was not saved: %d %v", code, body)
	}
	// Saving editor settings validates descriptors, not executable source.
	code, body, _ = e.req("PUT", "/api/v1/modules/"+id+"/debug",
		`{"version":0,"script":"print(\"caller also saved\")","spec":{}}`, headers, cookies)
	if code != 200 {
		t.Fatalf("debug autosave should work while source is incomplete: %d %v", code, body)
	}
	for _, tc := range []struct{ path, body string }{
		{"/publish/preview", `{"draft_version":2}`},
		{"/debug/run", `{"draft_version":2,"debug_version":1}`},
	} {
		code, body, _ = e.req("POST", "/api/v1/modules/"+id+tc.path, tc.body, headers, cookies)
		if code != 400 || !strings.Contains(strings.ToLower(fmt.Sprint(body)), "u.star") {
			t.Fatalf("execution/publish validation accepted incomplete source at %s: %d %v", tc.path, code, body)
		}
	}
}

// Debug end-to-end over HTTP: unpublished draft -> scaffold -> save ->
// run @self -> stale conflicts -> delete cleanup.
func TestModuleDebugHTTP(t *testing.T) {
	e := newEnv(t)
	cookies := login(e, "dbg@x.com")
	csrf := map[string]string{"X-Requested-With": "calcside", "Content-Type": "application/json"}

	code, m, _ := e.req("POST", "/api/v1/modules", `{"name":"dbgx","files":{"u.star":"x = 7"}}`, csrf, cookies)
	if code != 200 {
		t.Fatalf("create: %d %v", code, m)
	}
	id := m["module"].(map[string]any)["id"].(string)
	dv := int64(m["module"].(map[string]any)["draft_version"].(float64))

	code, m, _ = e.req("GET", "/api/v1/modules/"+id+"/debug", "", nil, cookies)
	if code != 200 || m["debug"] == nil {
		t.Fatalf("get debug: %d %v", code, m)
	}
	if !strings.Contains(m["debug"].(map[string]any)["script"].(string), "@self") {
		t.Fatalf("scaffold missing @self: %v", m["debug"])
	}

	code, m, _ = e.req("PUT", "/api/v1/modules/"+id+"/debug",
		`{"version":0,"script":"load(\"@self/u.star\",\"x\")\nprint(x)","spec":{}}`, csrf, cookies)
	if code != 200 || m["debug"].(map[string]any)["version"].(float64) != 1 {
		t.Fatalf("save: %d %v", code, m)
	}
	dbv := int64(m["debug"].(map[string]any)["version"].(float64))

	code, m, _ = e.req("POST", "/api/v1/modules/"+id+"/debug/run",
		`{"draft_version":`+itoa(dv)+`,"debug_version":`+itoa(dbv)+`}`, csrf, cookies)
	if code != 200 {
		t.Fatalf("run: %d %v", code, m)
	}
	res := m["result"].(map[string]any)
	if out, _ := res["output"].(string); !strings.Contains(out, "7") {
		t.Fatalf("output: %v", res)
	}
	instID := m["instance_id"].(string)
	code, m2, _ := e.req("GET", "/api/v1/instances/"+instID, "", nil, cookies)
	if code == 200 {
		st, _ := m2["instance"].(map[string]any)["status"].(string)
		if st != "deleted" && st != "expired" && st != "stopped" {
			t.Fatalf("scratch alive after run: status=%q", st)
		}
	} else if code != 404 && code != 409 {
		t.Fatalf("unexpected get: %d", code)
	}

	// Stale debug version conflicts.
	code, _, _ = e.req("POST", "/api/v1/modules/"+id+"/debug/run",
		`{"draft_version":`+itoa(dv)+`,"debug_version":99}`, csrf, cookies)
	if code != 409 {
		t.Fatalf("stale debug: %d", code)
	}
	// Stale draft conflicts.
	code, _, _ = e.req("POST", "/api/v1/modules/"+id+"/debug/run",
		`{"draft_version":99,"debug_version":`+itoa(dbv)+`}`, csrf, cookies)
	if code != 409 {
		t.Fatalf("stale draft: %d", code)
	}
	// Fresh globals: a script bumping a global counter prints 1 on
	// each separate run — state does not leak across scratch instances.
	code, m, _ = e.req("PUT", "/api/v1/modules/"+id+"/debug",
		`{"version":`+itoa(dbv)+`,"script":"C = [0]\ndef next():\n C[0] += 1\n return C[0]\nprint(next())","spec":{}}`, csrf, cookies)
	if code != 200 {
		t.Fatalf("save counter script: %d %v", code, m)
	}
	dbv = int64(m["debug"].(map[string]any)["version"].(float64))
	var runIDs []string
	for i := 0; i < 2; i++ {
		code, m, _ = e.req("POST", "/api/v1/modules/"+id+"/debug/run",
			`{"draft_version":`+itoa(dv)+`,"debug_version":`+itoa(dbv)+`}`, csrf, cookies)
		if code != 200 {
			t.Fatalf("fresh run %d: %d %v", i, code, m)
		}
		out, _ := m["result"].(map[string]any)["output"].(string)
		if !strings.Contains(out, "1") || strings.Contains(out, "2") {
			t.Fatalf("fresh run %d not fresh: %v", i, out)
		}
		runIDs = append(runIDs, m["instance_id"].(string))
	}
	if runIDs[0] == runIDs[1] {
		t.Fatal("debug runs shared a scratch instance")
	}

	// Runtime error: the run reports the failure and the scratch
	// instance is still cleaned up.
	code, m, _ = e.req("PUT", "/api/v1/modules/"+id+"/debug",
		`{"version":`+itoa(dbv)+`,"script":"fail(\"boom\")","spec":{}}`, csrf, cookies)
	if code != 200 {
		t.Fatalf("save failing script: %d %v", code, m)
	}
	dbv = int64(m["debug"].(map[string]any)["version"].(float64))
	code, m, _ = e.req("POST", "/api/v1/modules/"+id+"/debug/run",
		`{"draft_version":`+itoa(dv)+`,"debug_version":`+itoa(dbv)+`}`, csrf, cookies)
	if code != 200 {
		t.Fatalf("error run: %d %v", code, m)
	}
	if m["result"] == nil || m["result"].(map[string]any)["error"] == nil ||
		m["result"].(map[string]any)["error"] == "" {
		t.Fatalf("runtime error not reported: %v", m["result"])
	}
	instID2 := m["instance_id"].(string)
	code, m2, _ = e.req("GET", "/api/v1/instances/"+instID2, "", nil, cookies)
	if code == 200 {
		st, _ := m2["instance"].(map[string]any)["status"].(string)
		if st != "deleted" && st != "expired" && st != "stopped" {
			t.Fatalf("error-run scratch alive: %q", st)
		}
	}

	// Debug settings do not alter the publish digest or history.
	code, m, _ = e.req("POST", "/api/v1/modules/"+id+"/publish/preview", `{"draft_version":`+itoa(dv)+`}`, csrf, cookies)
	if code != 200 {
		t.Fatalf("preview: %d %v", code, m)
	}
	digest := m["candidate"].(map[string]any)["digest"].(string)
	code, m, _ = e.req("POST", "/api/v1/modules/"+id+"/publish",
		`{"draft_version":`+itoa(dv)+`,"expected_base":"","candidate":"`+digest+`"}`, csrf, cookies)
	if code != 200 || m["version"].(map[string]any)["sum"] != digest {
		t.Fatalf("publish after debug edits: %d %v", code, m)
	}
	code, m, _ = e.req("GET", "/api/v1/modules/"+id+"/versions", "", nil, cookies)
	if len(m["versions"].([]any)) != 1 {
		t.Fatalf("history polluted by debug: %v", m)
	}
	// Restore does not touch the debug version counter.
	code, m, _ = e.req("PATCH", "/api/v1/modules/"+id, `{"draft_version":`+itoa(dv)+`,"files":{"u.star":"x = 8"}}`, csrf, cookies)
	if code != 200 {
		t.Fatalf("patch for restore: %d %v", code, m)
	}
	dv2 := int64(m["module"].(map[string]any)["draft_version"].(float64))
	code, m, _ = e.req("POST", "/api/v1/modules/"+id+"/versions/1/restore", `{"draft_version":`+itoa(dv2)+`}`, csrf, cookies)
	if code != 200 {
		t.Fatalf("restore: %d %v", code, m)
	}
	code, m, _ = e.req("GET", "/api/v1/modules/"+id+"/debug", "", nil, cookies)
	if m["debug"].(map[string]any)["version"].(float64) != float64(dbv) {
		t.Fatalf("restore changed debug version: %v", m["debug"])
	}
	// Inline secrets in debug spec are rejected.
	code, _, _ = e.req("PUT", "/api/v1/modules/"+id+"/debug",
		`{"version":1,"script":"print(1)","spec":{"secrets":{"S":{"value":"shh"}}}}`, csrf, cookies)
	if code != 400 {
		t.Fatalf("inline secret accepted: %d", code)
	}
}

// Restricted/bound keys are invisible on every module route — real
// bearer principals minted by POST /keys.
func TestModuleKeysInvisible(t *testing.T) {
	e := newEnv(t)
	cookies := login(e, "kb@x.com")
	csrf := map[string]string{"X-Requested-With": "calcside", "Content-Type": "application/json"}
	code, m, _ := e.req("POST", "/api/v1/modules", `{"name":"kv","files":{"u.star":"x = 1"}}`, csrf, cookies)
	if code != 200 {
		t.Fatalf("create: %d %v", code, m)
	}
	id := m["module"].(map[string]any)["id"].(string)

	_, m, _ = e.req("POST", "/api/v1/keys", `{"name":"rk","spec":{"ttl_seconds":600},"max_instances":1}`, csrf, cookies)
	key := m["secret"].(string)
	bearer := map[string]string{"Authorization": "Bearer " + key, "Content-Type": "application/json"}
	// Key authenticates (proves it exists, not that routes leak).
	code, _, _ = e.req("GET", "/api/v1/me", "", bearer, nil)
	if code != 200 {
		t.Fatalf("key did not authenticate: %d", code)
	}
	for _, tc := range []struct{ method, path, body string }{
		{"POST", "/api/v1/modules", `{"name":"x","files":{"u.star":"x = 1"}}`},
		{"POST", "/api/v1/modules/validate", `{"files":{"u.star":"x = 1"}}`},
		{"GET", "/api/v1/modules/" + id, ""},
		{"GET", "/api/v1/modules/" + id + "/debug", ""},
		{"PUT", "/api/v1/modules/" + id + "/debug", `{"version":0,"script":"p","spec":{}}`},
		{"POST", "/api/v1/modules/" + id + "/debug/run", `{"draft_version":1,"debug_version":1}`},
		{"POST", "/api/v1/modules/" + id + "/publish", `{}`},
	} {
		if code, _, _ := e.req(tc.method, tc.path, tc.body, bearer, nil); code != 404 {
			t.Fatalf("%s %s: restricted key got %d", tc.method, tc.path, code)
		}
	}

	// A key bound to an instance resource: same invisibility on module
	// routes (bound_id ceiling hides everything else).
	_, m, _ = e.req("POST", "/api/v1/instances", `{"capabilities":{"io":{}}}`, csrf, cookies)
	instID := m["instance"].(map[string]any)["id"].(string)
	_, m, _ = e.req("POST", "/api/v1/keys", `{"name":"bk","instance_id":"`+instID+`"}`, csrf, cookies)
	if m["secret"] == nil {
		t.Fatalf("bound key create: %v", m)
	}
	bbearer := map[string]string{"Authorization": "Bearer " + m["secret"].(string)}
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/api/v1/modules", ""},
		{"POST", "/api/v1/modules/validate", `{"files":{"u.star":"x = 1"}}`},
		{"GET", "/api/v1/modules/" + id, ""},
		{"GET", "/api/v1/modules/" + id + "/debug", ""},
		{"POST", "/api/v1/modules/" + id + "/debug/run", `{"draft_version":1,"debug_version":1}`},
	} {
		if code, _, _ := e.req(tc.method, tc.path, tc.body, bbearer, nil); code != 404 {
			t.Fatalf("%s %s: bound key got %d", tc.method, tc.path, code)
		}
	}
}

// Missing/null required fields 400; explicit version 0 + empty script
// + {} spec is a valid first save.
func TestModuleDebugRequired(t *testing.T) {
	e := newEnv(t)
	cookies := login(e, "req@x.com")
	csrf := map[string]string{"X-Requested-With": "calcside", "Content-Type": "application/json"}
	code, m, _ := e.req("POST", "/api/v1/modules", `{"name":"rq","files":{"u.star":"x = 1"}}`, csrf, cookies)
	if code != 200 {
		t.Fatalf("create: %d %v", code, m)
	}
	id := m["module"].(map[string]any)["id"].(string)

	for i, tc := range []string{
		`{"script":"p","spec":{}}`, // missing version
		`{"version":null,"script":"p","spec":{}}`,
		`{"version":0,"spec":{}}`, // missing script
		`{"version":0,"script":null,"spec":{}}`,
		`{"version":0,"script":"p"}`, // missing spec
		`{"version":0,"script":"p","spec":null}`,
	} {
		code, _, _ := e.req("PUT", "/api/v1/modules/"+id+"/debug", tc, csrf, cookies)
		if code != 400 {
			t.Fatalf("case %d: got %d", i, code)
		}
	}
	code, m, _ = e.req("PUT", "/api/v1/modules/"+id+"/debug",
		`{"version":0,"script":"","spec":{}}`, csrf, cookies)
	if code != 200 || m["debug"].(map[string]any)["version"].(float64) != 1 {
		t.Fatalf("explicit-zero save: %d %v", code, m)
	}
	for i, tc := range []string{
		`{"debug_version":1}`,
		`{"draft_version":null,"debug_version":1}`,
		`{"draft_version":1}`,
	} {
		code, _, _ := e.req("POST", "/api/v1/modules/"+id+"/debug/run", tc, csrf, cookies)
		if code != 400 {
			t.Fatalf("run case %d: got %d", i, code)
		}
	}
}
