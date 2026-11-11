package api

import (
	"context"
	"encoding/json"
	"testing"

	"calcside/cmd/calcside/internal/auth"
	"calcside/internal/runtime"
	"calcside/internal/types"
)

// notebookEnv creates an instance through the API and returns its id
// plus bearer headers for follow-up calls.
func notebookEnv(t *testing.T) (*env, string, map[string]string) {
	t.Helper()
	e := newEnv(t)
	cookies := login(e, "nb@x.com")
	csrf := map[string]string{"X-Requested-With": "calcside"}
	code, m, _ := e.req("POST", "/api/v1/keys", `{"name":"k"}`, csrf, cookies)
	if code != 201 {
		t.Fatalf("create key: %d %v", code, m)
	}
	bearer := map[string]string{"Authorization": "Bearer " + m["secret"].(string)}
	code, m, _ = e.req("POST", "/api/v1/instances", `{"capabilities":{"io":{}},"ttl_seconds":600}`, bearer, nil)
	if code != 201 {
		t.Fatalf("create instance: %d %v", code, m)
	}
	return e, m["instance"].(map[string]any)["id"].(string), bearer
}

func TestNotebookEndpoints(t *testing.T) {
	e, id, bearer := notebookEnv(t)

	// Empty notebook outline.
	code, m, _ := e.req("GET", "/api/v1/instances/"+id+"/notebook", "", bearer, nil)
	if code != 200 {
		t.Fatalf("notebook: %d %v", code, m)
	}
	nb := m["notebook"].(map[string]any)
	if nb["instance_id"] != id || nb["revision"] == nil {
		t.Fatalf("notebook: %v", nb)
	}

	// Save a section: response is {section: meta, revision}.
	code, m, _ = e.req("PUT", "/api/v1/instances/"+id+"/sections/setup",
		`{"code":"x = 40 + 2","description":"the answer","revision":0}`, bearer, nil)
	if code != 200 {
		t.Fatalf("save section: %d %v", code, m)
	}
	sec := m["section"].(map[string]any)
	if sec["name"] != "setup" || sec["description"] != "the answer" || m["revision"] == nil {
		t.Fatalf("save response shape: %v", m)
	}
	rev := m["revision"].(float64)

	// New section without description → 400 (description required).
	code, _, _ = e.req("PUT", "/api/v1/instances/"+id+"/sections/nodoc",
		`{"code":"y = 1","revision":`+jsonNumber(rev)+`}`, bearer, nil)
	if code != 400 {
		t.Fatalf("missing description: expected 400, got %d", code)
	}
	// Invalid name → 400.
	code, _, _ = e.req("PUT", "/api/v1/instances/"+id+"/sections/Bad-Name",
		`{"code":"y = 1","description":"d","revision":`+jsonNumber(rev)+`}`, bearer, nil)
	if code != 400 {
		t.Fatalf("bad name: expected 400, got %d", code)
	}
	// Stale revision → 409.
	code, _, _ = e.req("PUT", "/api/v1/instances/"+id+"/sections/setup",
		`{"code":"x = 1","revision":0}`, bearer, nil)
	if code != 409 {
		t.Fatalf("stale revision: expected 409, got %d", code)
	}

	// Run setup so x exists in the process, then a dependent sectioned
	// exec reads it.
	code, m, _ = e.req("POST", "/api/v1/instances/"+id+"/notebook/run",
		`{"sections":"setup"}`, bearer, nil)
	if code != 200 {
		t.Fatalf("run setup: %d %v", code, m)
	}
	code, m, _ = e.req("POST", "/api/v1/instances/"+id+"/exec",
		`{"code":"print(x)","section":{"name":"show","description":"print x"}}`, bearer, nil)
	if code != 200 {
		t.Fatalf("sectioned exec: %d %v", code, m)
	}
	er := m["section_state"]
	if m["section"] != "show" || er == nil {
		t.Fatalf("exec notebook fields: %v", m)
	}

	code, m, _ = e.req("GET", "/api/v1/instances/"+id+"/sections/show?include_output=true", "", bearer, nil)
	if code != 200 {
		t.Fatalf("read section: %d %v", code, m)
	}
	sd := m["section"].(map[string]any)
	if sd["code"] != "print(x)" || sd["output"] != "42\n" {
		t.Fatalf("section detail: %v", sd)
	}
	if sd["section"].(map[string]any)["state"] == "" {
		t.Fatal("missing meta state")
	}
}

func TestNotebookRunSelectors(t *testing.T) {
	e, id, bearer := notebookEnv(t)

	// Two sections: provider + consumer. Track the CAS revision —
	// every save bumps it.
	rev := 0.0
	for _, s := range []struct{ name, code string }{
		{"alpha", "a = 1"}, {"beta", "b = a + 1"},
	} {
		code, m, _ := e.req("PUT", "/api/v1/instances/"+id+"/sections/"+s.name,
			`{"code":"`+s.code+`","description":"d","revision":`+jsonNumber(rev)+`}`, bearer, nil)
		if code != 200 {
			t.Fatalf("save %s: %d %v", s.name, code, m)
		}
		rev = m["revision"].(float64)
	}

	run := func(sel string) (int, map[string]any) {
		code, m, _ := e.req("POST", "/api/v1/instances/"+id+"/notebook/run",
			`{"sections":`+sel+`}`, bearer, nil)
		return code, m
	}
	statusOf := func(m map[string]any, name string) string {
		for _, r := range m["report"].(map[string]any)["results"].([]any) {
			rr := r.(map[string]any)
			if rr["name"] == name {
				return rr["status"].(string)
			}
		}
		return ""
	}

	// single name (string form — the case the enum union broke)
	code, m := run(`"alpha"`)
	if code != 200 || statusOf(m, "alpha") != "ok" || len(m["report"].(map[string]any)["results"].([]any)) != 1 {
		t.Fatalf("single selector: %d %v", code, m)
	}
	// all
	code, m = run(`"all"`)
	if code != 200 {
		t.Fatalf("all: %d %v", code, m)
	}
	if statusOf(m, "alpha") != "ok" || statusOf(m, "beta") != "ok" {
		t.Fatalf("all results: %v", m["report"])
	}
	// stale: edit alpha → alpha+beta stale
	code, m, _ = e.req("PUT", "/api/v1/instances/"+id+"/sections/alpha",
		`{"code":"a = 2","revision":`+jsonNumber(m["report"].(map[string]any)["revision"].(float64))+`}`, bearer, nil)
	if code != 200 {
		t.Fatalf("edit alpha: %d %v", code, m)
	}
	code, m = run(`"stale"`)
	if code != 200 {
		t.Fatalf("stale: %d %v", code, m)
	}
	results := m["report"].(map[string]any)["results"].([]any)
	if len(results) != 2 {
		t.Fatalf("stale should select both: %v", results)
	}
	// explicit array
	code, m = run(`["beta"]`)
	if code != 200 || len(m["report"].(map[string]any)["results"].([]any)) != 1 {
		t.Fatalf("array selector: %d %v", code, m)
	}
	// invalid selectors
	for _, sel := range []string{`123`, `{"x":1}`, `[]`, `true`, `""`} {
		code, _ = run(sel)
		if code != 400 {
			t.Fatalf("selector %s: expected 400, got %d", sel, code)
		}
	}
	// unknown name → 400
	code, _ = run(`"nosuch"`)
	if code != 400 {
		t.Fatalf("unknown name: expected 400, got %d", code)
	}
}

func TestNotebookRestoreViaAPI(t *testing.T) {
	e, id, bearer := notebookEnv(t)
	ctx := context.Background()

	// Seed a section, run it, then exec a dependent — history exists.
	code, m, _ := e.req("PUT", "/api/v1/instances/"+id+"/sections/load",
		`{"code":"secret_val = 123","description":"load","revision":0}`, bearer, nil)
	if code != 200 {
		t.Fatalf("save: %d %v", code, m)
	}
	code, m, _ = e.req("POST", "/api/v1/instances/"+id+"/notebook/run",
		`{"sections":"load"}`, bearer, nil)
	if code != 200 {
		t.Fatalf("run load: %d %v", code, m)
	}
	code, m, _ = e.req("POST", "/api/v1/instances/"+id+"/exec",
		`{"code":"print(secret_val)","section":{"name":"show","description":"show"}}`, bearer, nil)
	if code != 200 || m["output"] != "123\n" {
		t.Fatalf("exec: %d %v", code, m)
	}

	// Snapshot spec/expiry/executions, then kill the runtime child.
	in, err := e.st.GetInstance(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	specBefore := string(in.Spec)
	expBefore := in.ExpiresAt
	execsBefore, _ := e.st.ListExecutions(ctx, id, 100)
	if _, err := e.mgr.Delete(ctx, &runtime.DeleteRequest{
		InstanceID: id, Owner: runtime.Owner{UserID: in.UserID}, Epoch: in.LeaseEpoch,
	}); err != nil {
		t.Fatal(err)
	}

	// The triggering exec restores and refuses: 409 instance_restored,
	// and NO execution is recorded for it.
	code, m, _ = e.req("POST", "/api/v1/instances/"+id+"/exec",
		`{"code":"print('nope')"}`, bearer, nil)
	if code != 409 || m["error"].(map[string]any)["code"] != "instance_restored" {
		t.Fatalf("restore exec: %d %v", code, m)
	}
	execsAfter, _ := e.st.ListExecutions(ctx, id, 100)
	if len(execsAfter) != len(execsBefore) {
		t.Fatalf("restoring call executed: %d -> %d executions", len(execsBefore), len(execsAfter))
	}

	// Stored spec and expiry are byte-for-byte unchanged.
	in2, _ := e.st.GetInstance(ctx, id)
	if string(in2.Spec) != specBefore || !in2.ExpiresAt.Equal(expBefore) {
		t.Fatal("restore rewrote spec or expiry")
	}
	if in2.Status != types.InstanceRunning || in2.Incarnation != 2 || in2.RestoredAt == nil {
		t.Fatalf("restored row: %+v", in2)
	}

	// Notebook read shows sections preserved but not_run, prior output
	// marked with the old incarnation.
	code, m, _ = e.req("GET", "/api/v1/instances/"+id+"/notebook", "", bearer, nil)
	if code != 200 {
		t.Fatalf("notebook: %d %v", code, m)
	}
	nb := m["notebook"].(map[string]any)
	if nb["incarnation"].(float64) != 2 || nb["restored_empty"] != true {
		t.Fatalf("notebook after restore: %v", nb)
	}
	for _, s := range nb["sections"].([]any) {
		sm := s.(map[string]any)
		if sm["state"] != "not_run" {
			t.Fatalf("section %v state: %v", sm["name"], sm["state"])
		}
		if sm["last_incarnation"].(float64) != 1 {
			t.Fatalf("section %v last_incarnation: %v", sm["name"], sm["last_incarnation"])
		}
	}

	// Historical output is retained and labeled previous-runtime.
	code, m, _ = e.req("GET", "/api/v1/instances/"+id+"/sections/show?include_output=true", "", bearer, nil)
	if code != 200 {
		t.Fatalf("read show: %d %v", code, m)
	}
	sd := m["section"].(map[string]any)
	if sd["output"] != "123\n" {
		t.Fatalf("historical output lost: %v", sd)
	}
	if sd["section"].(map[string]any)["last_incarnation"].(float64) != 1 {
		t.Fatal("last_incarnation not exposed")
	}

	// Explicit run works on the empty process; "load" succeeds, "show"
	// then succeeds too.
	code, m, _ = e.req("POST", "/api/v1/instances/"+id+"/notebook/run",
		`{"sections":"all"}`, bearer, nil)
	if code != 200 {
		t.Fatalf("run all: %d %v", code, m)
	}
	for _, r := range m["report"].(map[string]any)["results"].([]any) {
		if r.(map[string]any)["status"] != "ok" {
			t.Fatalf("run result: %v", r)
		}
	}
	// After successful reruns restored_empty is false — the outline no
	// longer claims the process is still empty.
	code, m, _ = e.req("GET", "/api/v1/instances/"+id+"/notebook", "", bearer, nil)
	if m["notebook"].(map[string]any)["restored_empty"] != false {
		t.Fatal("restored_empty should clear after reruns")
	}
}

func TestNotebookExpiredAndPermissions(t *testing.T) {
	e, id, bearer := notebookEnv(t)

	// View-only key owned by the instance's owner: reads yes,
	// writes/runs no (keys never follow shares — same user required).
	in, _ := e.st.GetInstance(context.Background(), id)
	voSecret, vo := auth.NewAPIKey(in.UserID, "vo", nil)
	vo.WorkspaceID = in.WorkspaceID
	vo.ViewOnly = true
	if err := e.st.CreateAPIKey(context.Background(), vo); err != nil {
		t.Fatal(err)
	}
	vob := map[string]string{"Authorization": "Bearer " + voSecret}
	code, _, _ := e.req("GET", "/api/v1/instances/"+id+"/notebook", "", vob, nil)
	if code != 200 {
		t.Fatalf("view notebook: %d", code)
	}
	code, _, _ = e.req("POST", "/api/v1/instances/"+id+"/notebook/run", `{"sections":"all"}`, vob, nil)
	if code != 403 {
		t.Fatalf("view run: expected 403, got %d", code)
	}
	code, _, _ = e.req("PUT", "/api/v1/instances/"+id+"/sections/x",
		`{"code":"x=1","description":"d","revision":0}`, vob, nil)
	if code != 403 {
		t.Fatalf("view save: expected 403, got %d", code)
	}

	// Expired: exec/run 410, notebook read still 200.
	e.expire(id)
	code, m, _ := e.req("POST", "/api/v1/instances/"+id+"/exec", `{"code":"print(1)"}`, bearer, nil)
	if code != 410 || m["error"].(map[string]any)["code"] != "instance_expired" {
		t.Fatalf("expired exec: %d %v", code, m)
	}
	code, _, _ = e.req("POST", "/api/v1/instances/"+id+"/notebook/run", `{"sections":"all"}`, bearer, nil)
	if code != 410 {
		t.Fatalf("expired run: expected 410, got %d", code)
	}
	code, _, _ = e.req("GET", "/api/v1/instances/"+id+"/notebook", "", bearer, nil)
	if code != 200 {
		t.Fatalf("expired notebook read: expected 200, got %d", code)
	}
}

func TestNotebookBoundKey(t *testing.T) {
	e, id, bearer := notebookEnv(t)
	ctx := context.Background()

	// A key bound to this instance reaches its notebook; another
	// instance's id is refused (bound keys only ever see their resource).
	in, _ := e.st.GetInstance(ctx, id)
	boundSecret, bound := auth.NewAPIKey(in.UserID, "bk", nil)
	bound.WorkspaceID = in.WorkspaceID
	bound.BoundID = id
	if err := e.st.CreateAPIKey(ctx, bound); err != nil {
		t.Fatal(err)
	}
	bb := map[string]string{"Authorization": "Bearer " + boundSecret}
	code, _, _ := e.req("GET", "/api/v1/instances/"+id+"/notebook", "", bb, nil)
	if code != 200 {
		t.Fatalf("bound notebook: %d", code)
	}
	// A second instance (created by the owner) is invisible to the
	// bound key — it only ever sees its bound resource.
	code, m2, _ := e.req("POST", "/api/v1/instances", `{"capabilities":{"io":{}},"ttl_seconds":600}`, bearer, nil)
	if code == 201 {
		other := m2["instance"].(map[string]any)["id"].(string)
		code, _, _ = e.req("GET", "/api/v1/instances/"+other+"/notebook", "", bb, nil)
		if code != 404 {
			t.Fatalf("bound other instance: expected 404, got %d", code)
		}
		e.req("DELETE", "/api/v1/instances/"+other, "", bearer, nil)
	} else {
		t.Fatalf("create second instance: %d %v", code, m2)
	}
}

func jsonNumber(f float64) string {
	b, _ := json.Marshal(int64(f))
	return string(b)
}
