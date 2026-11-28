package api

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestApplicationTestCasesAndPublishGate(t *testing.T) {
	e := newEnv(t)
	cookiesA := login(e, "a@x.com")
	cookiesB := login(e, "b@x.com")
	csrf := map[string]string{"X-Requested-With": "calcside"}

	// Multi-turn application with 2 chained interrupts + on_expire handler.
	appCode := `def main(inp):
    amt = inp.get("amount", 0)
    if amt <= 0:
        return {"status": "rejected", "reason": "non_positive"}
    s = interrupt.new(expires_in_s=60)
    interrupt.wait(s["id"], handler="on_manager", on_expire="on_timeout", context={"stage": "manager", "amount": amt})

def on_manager(ctx):
    if not ctx["event"].get("approved"):
        return {"status": "denied", "stage": "manager"}
    s = interrupt.new(expires_in_s=60)
    interrupt.wait(s["id"], handler="on_finance", context={"stage": "finance", "amount": ctx["context"]["amount"], "manager": ctx["event"].get("by", "")})

def on_finance(ctx):
    return {
        "status": "approved" if ctx["event"].get("paid") else "held",
        "amount": ctx["context"]["amount"],
        "manager": ctx["context"]["manager"],
    }

def on_timeout(ctx):
    return {"status": "expired", "amount": ctx["context"]["amount"]}
`
	createBody, _ := json.Marshal(map[string]any{
		"name":     "ApprovalFlow",
		"sections": []map[string]string{{"id": "main", "code": appCode}},
	})
	code, m, raw := e.req("POST", "/api/v1/applications", string(createBody), csrf, cookiesA)
	if code != 200 {
		t.Fatalf("create app: %d %s", code, raw)
	}
	appID := m["application"].(map[string]any)["id"].(string)

	// 1. Create a multi-turn interrupt test case (2 interrupts -> exact strong output).
	tc1Body := `{
		"name": "Two-stage approval",
		"description": "Manager approves then finance pays",
		"input": {"amount": 500},
		"interrupts": [
			{
				"expect_handler": "on_manager",
				"expect_context": {"stage": "manager", "amount": 500},
				"context_match_mode": "exact",
				"resolution": "resolved",
				"event": {"approved": true, "by": "alice"}
			},
			{
				"expect_handler": "on_finance",
				"expect_context": {"stage": "finance", "manager": "alice"},
				"context_match_mode": "subset",
				"resolution": "resolved",
				"event": {"paid": true}
			}
		],
		"expect_status": "succeeded",
		"expected_output": {"status": "approved", "amount": 500, "manager": "alice"},
		"output_match_mode": "exact"
	}`
	code, c1m, raw := e.req("POST", "/api/v1/applications/"+appID+"/test-cases", tc1Body, csrf, cookiesA)
	if code != 201 {
		t.Fatalf("create tc1: %d %s", code, raw)
	}
	tc1ID := c1m["test_case"].(map[string]any)["id"].(string)

	// 2. Create an expire-simulation test case.
	tc2Body := `{
		"name": "Manager timeout",
		"input": {"amount": 250},
		"interrupts": [
			{
				"expect_handler": "on_manager",
				"resolution": "expired"
			}
		],
		"expect_status": "succeeded",
		"expected_output": {"status": "expired", "amount": 250}
	}`
	code, c2m, raw := e.req("POST", "/api/v1/applications/"+appID+"/test-cases", tc2Body, csrf, cookiesA)
	if code != 201 {
		t.Fatalf("create tc2: %d %s", code, raw)
	}
	tc2ID := c2m["test_case"].(map[string]any)["id"].(string)

	// Cross-user isolation on all 5 test-case endpoints.
	for _, tc := range []struct{ name, method, path, body string }{
		{"list", "GET", "/api/v1/applications/" + appID + "/test-cases", ""},
		{"create", "POST", "/api/v1/applications/" + appID + "/test-cases", `{"name":"x"}`},
		{"update", "PUT", "/api/v1/applications/" + appID + "/test-cases/" + tc1ID, `{"name":"x"}`},
		{"delete", "DELETE", "/api/v1/applications/" + appID + "/test-cases/" + tc1ID, ""},
		{"run", "POST", "/api/v1/applications/" + appID + "/test-cases/run", `{"version":1}`},
	} {
		c, _, _ := e.req(tc.method, tc.path, tc.body, csrf, cookiesB)
		if c != 404 {
			t.Fatalf("%s foreign workspace: got %d, want 404", tc.name, c)
		}
	}

	// 3. Publish BEFORE running test cases must be rejected (400).
	code, ck, raw := e.req("POST", "/api/v1/applications/"+appID+"/release/check", `{"version":1}`, csrf, cookiesA)
	if code != 200 || int(ck["test_case_count"].(float64)) != 2 {
		t.Fatalf("check with 2 test cases: %d %s", code, raw)
	}
	cand := ck["candidate"].(string)
	code, _, raw = e.req("POST", "/api/v1/applications/"+appID+"/publish",
		fmt.Sprintf(`{"version":1,"expected_base":"","candidate":%q}`, cand), csrf, cookiesA)
	if code != 400 {
		t.Fatalf("publish before running tests: got %d (%s), want 400", code, raw)
	}

	// 4. Add a failing test case (wrong expected output) -> suite fails -> publish still rejected.
	tc3Body := `{
		"name": "Wrong output expectation",
		"input": {"amount": 0},
		"expected_output": {"status": "approved"}
	}`
	code, c3m, raw := e.req("POST", "/api/v1/applications/"+appID+"/test-cases", tc3Body, csrf, cookiesA)
	if code != 201 {
		t.Fatalf("create tc3: %d %s", code, raw)
	}
	tc3ID := c3m["test_case"].(map[string]any)["id"].(string)

	code, runFail, raw := e.req("POST", "/api/v1/applications/"+appID+"/test-cases/run", `{"version":1}`, csrf, cookiesA)
	if code != 200 || runFail["status"] != "failed" || int(runFail["passed"].(float64)) != 2 || int(runFail["failed"].(float64)) != 1 {
		t.Fatalf("expected 2 passed / 1 failed: %d %s", code, raw)
	}
	code, _, raw = e.req("POST", "/api/v1/applications/"+appID+"/publish",
		fmt.Sprintf(`{"version":1,"expected_base":"","candidate":%q}`, cand), csrf, cookiesA)
	if code != 400 {
		t.Fatalf("publish with failing test suite: got %d (%s), want 400", code, raw)
	}

	// 5. Fix tc3 -> re-run full suite -> all 3 pass -> publish succeeds!
	tc3Fix := `{
		"name": "Reject non-positive",
		"input": {"amount": 0},
		"expected_output": {"status": "rejected", "reason": "non_positive"}
	}`
	code, _, raw = e.req("PUT", "/api/v1/applications/"+appID+"/test-cases/"+tc3ID, tc3Fix, csrf, cookiesA)
	if code != 200 {
		t.Fatalf("update tc3: %d %s", code, raw)
	}
	code, runPass, raw := e.req("POST", "/api/v1/applications/"+appID+"/test-cases/run", `{"version":1}`, csrf, cookiesA)
	if code != 200 || runPass["status"] != "passed" || int(runPass["passed"].(float64)) != 3 || int(runPass["failed"].(float64)) != 0 {
		t.Fatalf("expected 3 passed: %d %s", code, raw)
	}

	// Check now includes latest_test_run for this exact candidate.
	code, ck2, raw := e.req("POST", "/api/v1/applications/"+appID+"/release/check", `{"version":1}`, csrf, cookiesA)
	if code != 200 || ck2["latest_test_run"] == nil {
		t.Fatalf("check after passing suite: %d %s", code, raw)
	}

	code, _, raw = e.req("POST", "/api/v1/applications/"+appID+"/publish",
		fmt.Sprintf(`{"version":1,"expected_base":"","candidate":%q,"note":"tested release"}`, cand), csrf, cookiesA)
	if code != 202 {
		t.Fatalf("publish after passing suite: %d %s", code, raw)
	}

	// Cleanup delete check.
	code, _, raw = e.req("DELETE", "/api/v1/applications/"+appID+"/test-cases/"+tc2ID, "", csrf, cookiesA)
	if code != 200 {
		t.Fatalf("delete tc2: %d %s", code, raw)
	}
}
