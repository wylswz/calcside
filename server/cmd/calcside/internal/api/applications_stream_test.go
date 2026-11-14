package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"calcside/internal/runtime"
	"calcside/internal/store"
)

func rawPost(e *env, path, body string, hdrs map[string]string, cookies []*http.Cookie) (int, http.Header, string) {
	e.t.Helper()
	r, err := http.NewRequest("POST", e.srv.URL+path, strings.NewReader(body))
	if err != nil {
		e.t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/json")
	for k, v := range hdrs {
		r.Header.Set(k, v)
	}
	for _, c := range cookies {
		r.AddCookie(c)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(data)
}

func delApp(e *env, cookies []*http.Cookie, id string) {
	e.t.Helper()
	csrf := map[string]string{"X-Requested-With": "calcside"}
	e.req("DELETE", "/api/v1/applications/"+id+"?version=1", "", csrf, cookies)
}

func mkStreamApp(e *env, cookies []*http.Cookie, code, outSchema string) string {
	e.t.Helper()
	csrf := map[string]string{"X-Requested-With": "calcside"}
	out := "null"
	if outSchema != "" {
		out = outSchema
	}
	payload, _ := json.Marshal(map[string]any{
		"name": "sapp", "code": code,
		"input_schema": `{"type":"object"}`, "output_schema": out,
		"spec": map[string]any{"capabilities": map[string]any{"io": map[string]any{}}},
	})
	code2, m, _ := e.req("POST", "/api/v1/applications", string(payload), csrf, cookies)
	if code2 != 200 {
		e.t.Fatalf("create app: %d %v", code2, m)
	}
	return m["application"].(map[string]any)["id"].(string)
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func publishAndWait(e *env, id string, cookies []*http.Cookie) {
	e.t.Helper()
	csrf := map[string]string{"X-Requested-With": "calcside"}
	code, m, _ := e.req("POST", "/api/v1/applications/"+id+"/publish/preview", `{"version":1}`, csrf, cookies)
	if code != 200 {
		e.t.Fatalf("preview: %d %v", code, m)
	}
	cand := m["candidate"].(map[string]any)["digest"].(string)
	code, m, _ = e.req("POST", "/api/v1/applications/"+id+"/publish",
		`{"version":1,"expected_base":"","candidate":"`+cand+`"}`, csrf, cookies)
	if code != 202 {
		e.t.Fatalf("publish: %d %v", code, m)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		_, g, _ := e.req("GET", "/api/v1/applications/"+id, "", nil, cookies)
		app := g["application"].(map[string]any)
		if pid, _ := app["persistent_instance_id"].(string); pid != "" {
			e.activateReplica()
			return
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("pin never created: %v", app)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (e *env) activateReplica() {
	e.t.Helper()
	ctx := context.Background()
	rows, err := e.st.ListPersistentInstances(ctx, "", 100)
	if err != nil || len(rows) == 0 {
		e.t.Fatalf("no pin: %v", err)
	}
	pin := rows[len(rows)-1]
	node, err := e.st.RegisterServingNode(ctx, &store.ServingNode{
		NodeID: "n1", BootID: "b1", Incarnation: 1, Capacity: 8, Addr: "127.0.0.1:1",
		HeartbeatUntil: time.Now().Add(time.Hour),
	}, time.Now())
	if err != nil {
		e.t.Fatal(err)
	}
	rel, err := e.st.GetServingRelease(ctx, pin.WorkspaceID, pin.ReleaseID)
	if err != nil {
		e.t.Fatal(err)
	}
	repID := store.NewID("rep_")
	claim, err := e.st.ClaimServingReplica(ctx, store.ServingReplicaClaim{
		InstanceID: pin.ID, ReleaseID: pin.ReleaseID,
		NodeID: node.NodeID, BootID: node.BootID, ReplicaID: repID,
		NodeIncarnation: node.Incarnation, Generation: pin.Generation,
		ExpectedEpoch: pin.LastEpoch, Now: time.Now(),
		LeaseUntil: time.Now().Add(30 * time.Second),
	})
	if err != nil {
		e.t.Fatal(err)
	}
	prep, err := e.mgr.Prepare(ctx, &runtime.PrepareRequest{
		CreateRequest: runtime.CreateRequest{
			InstanceID: repID,
			Owner:      runtime.Owner{UserID: pin.UserID},
			Spec:       rel.Spec,
			Policies:   rel.Policies,
			Modules:    rel.Modules,
			ExpiresAt:  time.Now().Add(time.Hour),
			Epoch:      claim.Epoch,
		},
		ReleaseID: pin.ReleaseID, Generation: pin.Generation, PrepareID: repID + "-p",
		Program: rel.Program,
	})
	if err != nil || (prep != nil && prep.Result.Error != nil) {
		e.t.Fatalf("replica prepare: %v %+v", err, prep)
	}
	ok, err := e.st.ReportServingReplica(ctx, store.ServingReplicaFence{
		ReplicaID: claim.ID, InstanceID: claim.InstanceID, ReleaseID: claim.ReleaseID,
		NodeID: claim.NodeID, BootID: claim.BootID, NodeIncarnation: claim.NodeIncarnation,
		Generation: claim.Generation, Epoch: claim.Epoch,
	}, store.ServingReplicaReady, "", time.Now())
	if err != nil || !ok {
		e.t.Fatalf("report ready: %v %v", ok, err)
	}
}

const twoItemCode = `def main(input):
    return stream.from_list([{"n": 1}, {"n": 2}])
`

func TestApplicationStreamTestSSE(t *testing.T) {
	e := newEnv(t)
	cookies := login(e, "s@x.com")
	csrf := map[string]string{"X-Requested-With": "calcside"}
	id := mkStreamApp(e, cookies, twoItemCode, `{"type":"object"}`)

	status, hdr, body := rawPost(e, "/api/v1/applications/"+id+"/test",
		`{"version":1,"input":{}}`, csrf, cookies)
	if status != 200 || !strings.Contains(hdr.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("status=%d ct=%s", status, hdr.Get("Content-Type"))
	}
	if !strings.Contains(body, "event: item\ndata: {\"n\":1}\n\n") ||
		!strings.Contains(body, "event: item\ndata: {\"n\":2}\n\n") ||
		!strings.Contains(body, "event: done\ndata: ") ||
		strings.Contains(body, "event: error") {
		t.Fatalf("body: %s", body)
	}
	if !strings.Contains(body, `"stream_items":2`) || !strings.Contains(body, `"streaming":true`) {
		t.Fatalf("stats missing: %s", body)
	}

	id2 := mkStreamApp(e, cookies, `def main(input): return {"n": 1}`, `{"type":"object"}`)
	status, hdr, body = rawPost(e, "/api/v1/applications/"+id2+"/test",
		`{"version":1,"input":{}}`, csrf, cookies)
	if status != 200 || strings.Contains(hdr.Get("Content-Type"), "event-stream") {
		t.Fatalf("json path changed: %d %s", status, hdr.Get("Content-Type"))
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil || m["output_json"] == nil {
		t.Fatalf("body %s", body)
	}
}

func TestApplicationStreamTestEdgeCases(t *testing.T) {
	e := newEnv(t)
	cookies := login(e, "s@x.com")
	csrf := map[string]string{"X-Requested-With": "calcside"}

	id := mkStreamApp(e, cookies, `def main(input): return stream.from_list([])`, `{"type":"object"}`)
	_, hdr, body := rawPost(e, "/api/v1/applications/"+id+"/test", `{"version":1,"input":{}}`, csrf, cookies)
	if !strings.Contains(hdr.Get("Content-Type"), "event-stream") ||
		!strings.Contains(body, "event: done") || strings.Contains(body, "event: item") ||
		!strings.Contains(body, `"streaming":true`) {
		t.Fatalf("empty stream: %s", body)
	}
	delApp(e, cookies, id)

	id = mkStreamApp(e, cookies, `def main(input): return stream.from_list([None])`, `{"type":"null"}`)
	_, _, body = rawPost(e, "/api/v1/applications/"+id+"/test", `{"version":1,"input":{}}`, csrf, cookies)
	if !strings.Contains(body, "data: null") || !strings.Contains(body, "event: done") {
		t.Fatalf("null item: %s", body)
	}
	delApp(e, cookies, id)

	id = mkStreamApp(e, cookies, `def main(input): return stream.from_list([{"n": 9007199254740993}])`, `{"type":"object"}`)
	_, _, body = rawPost(e, "/api/v1/applications/"+id+"/test", `{"version":1,"input":{}}`, csrf, cookies)
	if !strings.Contains(body, `{"n":9007199254740993}`) {
		t.Fatalf("precision lost: %s", body)
	}
	delApp(e, cookies, id)

	id = mkStreamApp(e, cookies, `def main(input): return stream.from_list([{"ok": True}, 7])`, `{"type":"object"}`)
	_, _, body = rawPost(e, "/api/v1/applications/"+id+"/test", `{"version":1,"input":{}}`, csrf, cookies)
	if !strings.Contains(body, "event: item\ndata: {\"ok\":true}") ||
		!strings.Contains(body, "event: error") || strings.Contains(body, "event: done") {
		t.Fatalf("schema failure: %s", body)
	}
	if !strings.Contains(body, `"stream_items":1`) {
		t.Fatalf("prefix counters: %s", body)
	}
	delApp(e, cookies, id)

	id = mkStreamApp(e, cookies, `def main(input):
    def nxt():
        fail("kaboom")
    return stream.new(nxt)
`, `{"type":"object"}`)
	_, _, body = rawPost(e, "/api/v1/applications/"+id+"/test", `{"version":1,"input":{}}`, csrf, cookies)
	if !strings.Contains(body, "event: error") || strings.Contains(body, "event: item") ||
		strings.Contains(body, "kaboom") {
		t.Fatalf("producer failure must be safe error: %s", body)
	}

	status, hdr, _ := rawPost(e, "/api/v1/applications/"+id+"/test", `{"version":1,"input":{}}`, csrf, nil)
	if status != 401 || strings.Contains(hdr.Get("Content-Type"), "event-stream") {
		t.Fatalf("unauth: %d %s", status, hdr.Get("Content-Type"))
	}
}

func TestApplicationStreamInvokeSSE(t *testing.T) {
	e := newEnv(t)
	cookies := login(e, "s@x.com")
	csrf := map[string]string{"X-Requested-With": "calcside"}
	id := mkStreamApp(e, cookies, twoItemCode, `{"type":"object"}`)
	publishAndWait(e, id, cookies)

	status, hdr, body := rawPost(e, "/api/v1/applications/"+id+"/invoke", `{}`, csrf, cookies)
	if status != 200 || !strings.Contains(hdr.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("invoke: %d %s", status, hdr.Get("Content-Type"))
	}
	if !strings.Contains(body, "event: item\ndata: {\"n\":1}") ||
		!strings.Contains(body, "event: done\ndata: ") {
		t.Fatalf("invoke body: %s", body)
	}

	status, hdr, _ = rawPost(e, "/api/v1/applications/"+id+"/invoke", `{}`, nil, nil)
	if status != 401 || strings.Contains(hdr.Get("Content-Type"), "event-stream") {
		t.Fatalf("unauth invoke: %d %s", status, hdr.Get("Content-Type"))
	}
}

func mkAppNoOutSchema(e *env, cookies []*http.Cookie, code string) string {
	e.t.Helper()
	csrf := map[string]string{"X-Requested-With": "calcside"}
	payload, _ := json.Marshal(map[string]any{
		"name": "sapp", "code": code,
		"input_schema": `{"type":"object"}`,
		"spec":         map[string]any{"capabilities": map[string]any{"io": map[string]any{}}},
	})
	code2, m, _ := e.req("POST", "/api/v1/applications", string(payload), csrf, cookies)
	if code2 != 200 {
		e.t.Fatalf("create app: %d %v", code2, m)
	}
	return m["application"].(map[string]any)["id"].(string)
}

func TestApplicationStreamDefaultOutputSchema(t *testing.T) {
	e := newEnv(t)
	cookies := login(e, "d@x.com")
	csrf := map[string]string{"X-Requested-With": "calcside"}

	id := mkAppNoOutSchema(e, cookies, `def main(input):
    return stream.from_list([1, "x", None, True, {"n": 1}, [2, 3]])
`)
	status, hdr, body := rawPost(e, "/api/v1/applications/"+id+"/test",
		`{"version":1,"input":{}}`, csrf, cookies)
	if status != 200 || !strings.Contains(hdr.Get("Content-Type"), "event-stream") {
		t.Fatalf("%d %s", status, hdr.Get("Content-Type"))
	}
	for _, item := range []string{"data: 1", `data: "x"`, "data: null", "data: true", `data: {"n":1}`, "data: [2,3]"} {
		if !strings.Contains(body, item) {
			t.Fatalf("missing %s in %s", item, body)
		}
	}
	if !strings.Contains(body, "event: done") || strings.Contains(body, "event: error") ||
		!strings.Contains(body, `"stream_items":6`) {
		t.Fatalf("default stream: %s", body)
	}

	id2 := mkAppNoOutSchema(e, cookies, `def main(input): return "scalar"`)
	status, hdr, body = rawPost(e, "/api/v1/applications/"+id2+"/test",
		`{"version":1,"input":{}}`, csrf, cookies)
	if status != 200 || strings.Contains(hdr.Get("Content-Type"), "event-stream") ||
		!strings.Contains(body, `"scalar"`) {
		t.Fatalf("scalar JSON: %d %s %s", status, hdr.Get("Content-Type"), body)
	}

	status, _, body = rawPost(e, "/api/v1/applications/"+id+"/test",
		`{"version":1,"input":42}`, csrf, cookies)
	if !strings.Contains(body, "invalid_input") {
		t.Fatalf("input default must still reject scalar: %d %s", status, body)
	}
	delApp(e, cookies, id)
	delApp(e, cookies, id2)

	payload, _ := json.Marshal(map[string]any{
		"name": "sapp", "code": `def main(i): return stream.from_list([1])`,
		"output_schema": nil,
	})
	code, m, _ := e.req("POST", "/api/v1/applications", string(payload), csrf, cookies)
	if code != 200 {
		t.Fatalf("explicit null output: %d %v", code, m)
	}
	id3 := m["application"].(map[string]any)["id"].(string)
	_, hdr, body = rawPost(e, "/api/v1/applications/"+id3+"/test",
		`{"version":1,"input":{}}`, csrf, cookies)
	if !strings.Contains(hdr.Get("Content-Type"), "event-stream") ||
		!strings.Contains(body, "data: 1") {
		t.Fatalf("null output schema: %s", body)
	}

	id4 := mkStreamApp(e, cookies, `def main(input): return stream.from_list([{"n":2}, 1])`, `{"type":"object"}`)
	_, _, body = rawPost(e, "/api/v1/applications/"+id4+"/test",
		`{"version":1,"input":{}}`, csrf, cookies)
	if !strings.Contains(body, `data: {"n":2}`) ||
		!strings.Contains(body, "event: error") || strings.Contains(body, "event: done") {
		t.Fatalf("explicit object must reject scalar item: %s", body)
	}
}

func TestApplicationStreamDefaultInvokeSSE(t *testing.T) {
	e := newEnv(t)
	cookies := login(e, "d@x.com")
	csrf := map[string]string{"X-Requested-With": "calcside"}
	id := mkAppNoOutSchema(e, cookies, `def main(input): return stream.from_list([1, "two", None])`)
	publishAndWait(e, id, cookies)
	status, hdr, body := rawPost(e, "/api/v1/applications/"+id+"/invoke", `{}`, csrf, cookies)
	if status != 200 || !strings.Contains(hdr.Get("Content-Type"), "event-stream") ||
		!strings.Contains(body, `data: "two"`) || !strings.Contains(body, "event: done") {
		t.Fatalf("invoke default stream: %d %s %s", status, hdr.Get("Content-Type"), body)
	}
}
