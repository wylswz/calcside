package api

import (
	"fmt"
	"testing"
)

func TestApplicationReleaseEP26(t *testing.T) {
	e := newEnv(t)
	cookiesA := login(e, "a@x.com")
	cookiesB := login(e, "b@x.com")
	csrf := map[string]string{"X-Requested-With": "calcside"}

	// Create application with a cycle + invalid main signature to verify readiness blockers.
	code, m, raw := e.req("POST", "/api/v1/applications", `{"name":"OrderSvc","sections":[{"id":"s1","code":"x = y"},{"id":"main","code":"y = x\ndef main(): return 1"}]}`, csrf, cookiesA)
	if code != 200 {
		t.Fatalf("create app: %d %s", code, raw)
	}
	appID := m["application"].(map[string]any)["id"].(string)

	// Cross-user isolation on all 5 EP-0026 routes.
	for _, tc := range []struct{ name, method, path, body string }{
		{"check", "POST", "/api/v1/applications/" + appID + "/release/check", `{"version":1}`},
		{"releases", "GET", "/api/v1/applications/" + appID + "/releases", ""},
		{"compare", "GET", "/api/v1/applications/" + appID + "/releases/compare?base=0&head=draft", ""},
		{"restore", "POST", "/api/v1/applications/" + appID + "/releases/1/restore", `{"version":1}`},
		{"rollback", "POST", "/api/v1/applications/" + appID + "/releases/1/rollback", `{"expected_base":"rel_0000000000000000000000000000000000000000000000000000000000000000"}`},
	} {
		c, _, _ := e.req(tc.method, tc.path, tc.body, csrf, cookiesB)
		if c != 404 {
			t.Fatalf("%s foreign workspace: got %d, want 404", tc.name, c)
		}
	}

	// 1. Check reports structural blockers (invalid_main_signature + graph_cycle).
	code, m, raw = e.req("POST", "/api/v1/applications/"+appID+"/release/check", `{"version":1}`, csrf, cookiesA)
	if code != 200 || m["status"] != "blocked" {
		t.Fatalf("check blocked: %d %s", code, raw)
	}

	// 2. Fix structure -> check reports ready -> preview & publish v1.
	code, _, raw = e.req("PUT", "/api/v1/applications/"+appID,
		`{"version":1,"name":"OrderSvc","sections":[{"id":"main","code":"def main(inp):\n    return {\"hello\": inp.get(\"who\", \"world\")}"}]}`, csrf, cookiesA)
	if code != 200 {
		t.Fatalf("update v2: %d %s", code, raw)
	}
	code, cm, raw := e.req("POST", "/api/v1/applications/"+appID+"/release/check", `{"version":2}`, csrf, cookiesA)
	if code != 200 || cm["status"] != "ready" {
		t.Fatalf("check ready: %d %s", code, raw)
	}
	cand := cm["candidate"].(string)

	code, _, raw = e.req("POST", "/api/v1/applications/"+appID+"/publish",
		fmt.Sprintf(`{"version":2,"expected_base":"","candidate":%q,"note":"initial release"}`, cand), csrf, cookiesA)
	if code != 202 {
		t.Fatalf("publish v1: %d %s", code, raw)
	}

	// 3. Edit + publish v2, then compare v1..v2, rollback to v1, and restore v1 into draft.
	code, _, raw = e.req("PUT", "/api/v1/applications/"+appID,
		`{"version":2,"name":"OrderSvc","sections":[{"id":"main","code":"def main(inp):\n    return {\"v\": 2}"}]}`, csrf, cookiesA)
	if code != 200 {
		t.Fatalf("update v3: %d %s", code, raw)
	}
	code, pm, raw := e.req("POST", "/api/v1/applications/"+appID+"/publish/preview", `{"version":3}`, csrf, cookiesA)
	if code != 200 {
		t.Fatalf("preview v2: %d %s", code, raw)
	}
	cand2 := pm["candidate"].(map[string]any)["digest"].(string)
	code, _, raw = e.req("POST", "/api/v1/applications/"+appID+"/publish",
		fmt.Sprintf(`{"version":3,"expected_base":%q,"candidate":%q,"note":"bad v2"}`, cand, cand2), csrf, cookiesA)
	if code != 202 {
		t.Fatalf("publish v2: %d %s", code, raw)
	}

	code, cmp, raw := e.req("GET", "/api/v1/applications/"+appID+"/releases/compare?base=1&head=2", "", csrf, cookiesA)
	if code != 200 || cmp["identical"] == true {
		t.Fatalf("compare 1..2: %d %s", code, raw)
	}

	// Rollback live to seq 1 -> creates seq 3 with rollback_of_seq=1, live_release_id=cand.
	code, _, raw = e.req("POST", "/api/v1/applications/"+appID+"/releases/1/rollback",
		fmt.Sprintf(`{"expected_base":%q,"note":"incident rollback"}`, cand2), csrf, cookiesA)
	if code != 202 {
		t.Fatalf("rollback to v1: %d %s", code, raw)
	}

	code, lm, raw := e.req("GET", "/api/v1/applications/"+appID+"/releases", "", csrf, cookiesA)
	if code != 200 || lm["live_release_id"] != cand {
		t.Fatalf("releases after rollback: %d %s", code, raw)
	}
	rels := lm["releases"].([]any)
	if len(rels) != 3 {
		t.Fatalf("expected 3 release rows, got %d: %s", len(rels), raw)
	}
	top := rels[0].(map[string]any)
	if int(top["rollback_of_seq"].(float64)) != 1 || top["is_live"] != true {
		t.Fatalf("unexpected rollback head row: %+v", top)
	}

	// Restore seq 1 into draft (draft was at version 3 -> advances to version 4).
	code, rm, raw := e.req("POST", "/api/v1/applications/"+appID+"/releases/1/restore", `{"version":3}`, csrf, cookiesA)
	if code != 200 {
		t.Fatalf("restore seq 1: %d %s", code, raw)
	}
	restoredApp := rm["application"].(map[string]any)
	if int(restoredApp["version"].(float64)) != 4 {
		t.Fatalf("expected draft version 4 after restore, got %v", restoredApp["version"])
	}
}
