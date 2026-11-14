package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"calcside/cmd/calcside/internal/artifact"
	"calcside/internal/runtime"
)

func TestArtifactZIP(t *testing.T) {
	e := newEnv(t)
	cookies := login(e, "artifacts@example.com")
	headers := map[string]string{"X-Requested-With": "calcside"}
	code, m, _ := e.req("POST", "/api/v1/instances", `{"capabilities":{"fs":{}}}`, headers, cookies)
	if code != 201 {
		t.Fatalf("create: %d %v", code, m)
	}
	id := m["instance"].(map[string]any)["id"].(string)
	code, m, _ = e.req("POST", "/api/v1/instances/"+id+"/exec", `{"code":"fs.write('report.csv', 'id,value\\r\\nA,001\\r\\n')"}`, headers, cookies)
	if code != 200 || m["error"] != nil {
		t.Fatalf("exec: %d %v", code, m)
	}
	req, err := http.NewRequest("POST", e.srv.URL+"/api/v1/instances/"+id+"/artifacts/export", strings.NewReader(`{"format":"zip","paths":["/work/report.csv"]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "calcside")
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	resp, err := e.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("export: %d %s", resp.StatusCode, data)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if len(zr.File) != 1 || zr.File[0].Name != "report.csv" {
		t.Fatalf("files: %v", zr.File)
	}
	r, err := zr.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	content, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "id,value\r\nA,001\r\n" {
		t.Fatalf("content changed: %q", content)
	}
}

func artifactInstance(t *testing.T, e *env, cookies []*http.Cookie, code string) string {
	t.Helper()
	headers := map[string]string{"X-Requested-With": "calcside"}
	status, body, _ := e.req("POST", "/api/v1/instances", `{"capabilities":{"fs":{}}}`, headers, cookies)
	if status != 201 {
		t.Fatalf("create: %d", status)
	}
	id := body["instance"].(map[string]any)["id"].(string)
	encoded, _ := json.Marshal(map[string]string{"code": code})
	status, body, _ = e.req("POST", "/api/v1/instances/"+id+"/exec", string(encoded), headers, cookies)
	if status != 200 || body["error"] != nil {
		t.Fatalf("write artifacts: %d %v", status, body["error"])
	}
	return id
}

func issuePreview(t *testing.T, e *env, cookies []*http.Cookie, id, path, mode string) (int, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"path": path, "mode": mode})
	status, out, _ := e.req("POST", "/api/v1/instances/"+id+"/artifacts/preview", string(body), map[string]string{"X-Requested-With": "calcside"}, cookies)
	return status, out
}

func fetchPreview(t *testing.T, e *env, rawURL string) (int, http.Header, string) {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest("GET", e.srv.URL+u.RequestURI(), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = u.Host
	resp, err := e.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header, string(body)
}

func TestArtifactHTMLIsolation(t *testing.T) {
	e := newArtifactEnv(t, nil, nil, artifact.Config{BaseURL: "https://preview.example.net", ConsoleOrigin: "https://console.example.com", AllowScripts: true})
	cookies := login(e, "owner@example.com")
	other := login(e, "other@example.com")
	id := artifactInstance(t, e, cookies, `fs.write("report.html", "<h1>Original report</h1><script>window.ran = true</script>")`)
	if status, _ := issuePreview(t, e, other, id, "report.html", "static"); status != 404 {
		t.Fatalf("cross-owner preview: %d", status)
	}
	status, body := issuePreview(t, e, cookies, id, "report.html", "static")
	if status != 200 || body["sanitized"] != true {
		t.Fatalf("static preview status: %d", status)
	}
	previewURL := body["preview_url"].(string)
	parsed, err := url.Parse(previewURL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(parsed.Hostname(), ".preview.example.net") || parsed.Scheme != "https" {
		t.Fatal("not an isolated random subdomain")
	}
	status, headers, html := fetchPreview(t, e, previewURL)
	if status != 200 || !strings.Contains(html, "Original report") || strings.Contains(html, "<script") {
		t.Fatal("static body was not sanitized")
	}
	csp := headers.Get("Content-Security-Policy")
	for _, directive := range []string{"sandbox;", "script-src 'none'", "connect-src 'none'", "frame-ancestors https://console.example.com"} {
		if !strings.Contains(csp, directive) {
			t.Errorf("missing CSP %s", directive)
		}
	}
	if headers.Get("Set-Cookie") != "" || headers.Get("Referrer-Policy") != "no-referrer" || !strings.Contains(headers.Get("Cache-Control"), "no-store") {
		t.Fatal("unsafe preview headers")
	}
	tampered := *parsed
	tampered.RawQuery = "ticket=" + strings.Repeat("x", 43)
	if status, _, _ := fetchPreview(t, e, tampered.String()); status != 404 {
		t.Fatal("wrong ticket accepted")
	}
	tampered = *parsed
	tampered.RawQuery = ""
	if status, _, _ := fetchPreview(t, e, tampered.String()); status != 404 {
		t.Fatal("missing ticket accepted")
	}
	for _, path := range []string{"/api/v1/me", "/healthz", "/auth/google/login", "/internal/v1/ext/tree", "/report.css"} {
		tampered = *parsed
		tampered.Path = path
		if status, _, _ := fetchPreview(t, e, tampered.String()); status != 404 {
			t.Fatalf("preview host exposed %s", path)
		}
	}
	encoded, _ := json.Marshal(map[string]string{"code": `fs.write("report.html", "<h1>Changed report</h1><script>window.ran = true</script>")`})
	e.req("POST", "/api/v1/instances/"+id+"/exec", string(encoded), map[string]string{"X-Requested-With": "calcside"}, cookies)
	if status, _, body := fetchPreview(t, e, previewURL); status != 200 || !strings.Contains(body, "Original report") {
		t.Fatal("snapshot was mutable")
	}
	status, body = issuePreview(t, e, cookies, id, "report.html", "interactive")
	if status != 200 || body["interactive"] != true {
		t.Fatal("explicit interactive mode failed")
	}
	interactiveURL := body["preview_url"].(string)
	if interactiveURL == previewURL {
		t.Fatal("reused preview authority")
	}
	if status, _, _ := fetchPreview(t, e, previewURL); status != 404 {
		t.Fatal("replaced snapshot still accessible")
	}
	status, headers, html = fetchPreview(t, e, interactiveURL)
	csp = headers.Get("Content-Security-Policy")
	if status != 200 || !strings.Contains(html, "<script>") || !strings.Contains(csp, "sandbox allow-scripts") || strings.Contains(csp, "allow-same-origin") {
		t.Fatal("incorrect interactive sandbox")
	}
	e.expire(id)
	if status, _, _ := fetchPreview(t, e, interactiveURL); status != 404 {
		t.Fatal("expired instance remained accessible")
	}
}

func TestArtifactPreviewFailClosed(t *testing.T) {
	e := newEnv(t)
	cookies := login(e, "viewer@example.com")
	id := artifactInstance(t, e, cookies, `fs.write("report.html", "<script>throw 'never'</script>")
fs.write("rows.csv", "id,value\nA,001\nB,\"x,y\"\n")`)
	if status, body := issuePreview(t, e, cookies, id, "report.html", "source"); status != 200 || body["preview_url"] != nil {
		t.Fatal("source unexpectedly became executable")
	}
	if status, _ := issuePreview(t, e, cookies, id, "report.html", "static"); status != 400 {
		t.Fatal("rendering fell back to console origin")
	}
	if status, body := issuePreview(t, e, cookies, id, "rows.csv", "source"); status != 200 || body["csv_total_rows"] != float64(3) {
		t.Fatalf("CSV status: %d", status)
	}
	enabled := newArtifactEnv(t, nil, nil, artifact.Config{BaseURL: "https://preview.example.net", ConsoleOrigin: "https://console.example.com"})
	ownerCookies := login(enabled, "viewer@example.com")
	iid := artifactInstance(t, enabled, ownerCookies, `fs.write("report.html", "<h1>Report</h1>")`)
	if status, _ := issuePreview(t, enabled, ownerCookies, iid, "report.html", "interactive"); status != 403 {
		t.Fatal("scripts enabled implicitly")
	}
	status, body := issuePreview(t, enabled, ownerCookies, iid, "report.html", "static")
	if status != 200 {
		t.Fatal("static preview failed")
	}
	in, err := enabled.st.GetInstance(context.Background(), iid)
	if err != nil {
		t.Fatal(err)
	}
	_, err = enabled.mgr.Delete(context.Background(), &runtime.DeleteRequest{InstanceID: iid, Owner: runtime.Owner{UserID: in.UserID}, Epoch: in.LeaseEpoch})
	if err != nil {
		t.Fatal(err)
	}
	if status, _, _ := fetchPreview(t, enabled, body["preview_url"].(string)); status != 404 {
		t.Fatal("lost worker still served preview")
	}
}

func TestArtifactZIPDeniedAudit(t *testing.T) {
	policy := `package calcside.hooks
deny contains "private" if { input.phase == "after"; input.capability == "fs"; input.op == "read"; input.args.path == "/work/denied.txt" }`
	e := newEnvWithPolicies(t, nil, map[string]string{"deny.rego": policy})
	cookies := login(e, "export@example.com")
	id := artifactInstance(t, e, cookies, `fs.write("allowed.txt", "allowed-content"); fs.write("denied.txt", "private-content")`)
	status, body, _ := e.req("POST", "/api/v1/instances/"+id+"/artifacts/export", `{"format":"zip","paths":["allowed.txt","denied.txt"]}`, map[string]string{"X-Requested-With": "calcside"}, cookies)
	if status != 403 || body["error"] == nil {
		t.Fatalf("denial: %d", status)
	}
	e.rec.Close()
	e.rec = nil
	status, body, _ = e.req("GET", "/api/v1/audit?instance_id="+id, "", nil, cookies)
	if status != 200 {
		t.Fatal("audit query failed")
	}
	denied := false
	for _, value := range body["events"].([]any) {
		event := value.(map[string]any)
		if event["op"] == "read" && event["decision"] == "deny" && event["phase"] == "after" {
			denied = true
		}
	}
	if !denied {
		t.Fatal("denial audit was not persisted")
	}
}

func TestArtifactPreviewExpiryAndBounds(t *testing.T) {
	p, err := NewArtifactPreviews(artifact.Config{BaseURL: "https://preview.example.net", ConsoleOrigin: "https://console.example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	now := time.Now()
	p.now = func() time.Time { return now }
	_, expires, err := p.create("u", "i", 0, []byte("report"), false, now.Add(30*time.Second))
	if err != nil || !expires.Equal(now.Add(30*time.Second)) {
		t.Fatal("preview outlived instance deadline")
	}
	for i := 0; i < 20; i++ {
		if _, _, err := p.create("u", "i", 0, []byte("replacement"), false, now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if len(p.snapshots) != 1 || p.bytes != len("replacement") {
		t.Fatal("replacement leaked snapshots")
	}
	now = now.Add(3 * time.Minute)
	if _, _, err := p.create("u", "next", 0, []byte("next"), false, now.Add(time.Hour)); err != nil || len(p.snapshots) != 1 {
		t.Fatal("expired snapshots not pruned")
	}
}

func TestArtifactPreviewAdmission(t *testing.T) {
	p, err := NewArtifactPreviews(artifact.Config{BaseURL: "https://preview.example.net", ConsoleOrigin: "https://console.example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	u, _, err := p.create("u", "i", 0, []byte("report"), false, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < cap(p.slots); i++ {
		p.slots <- struct{}{}
	}
	response := httptest.NewRecorder()
	p.serve(response, httptest.NewRequest("GET", u, nil))
	if response.Code != http.StatusTooManyRequests {
		t.Fatal("preview delivery bypassed admission limit")
	}
}

func TestArtifactInlinePreview(t *testing.T) {
	e := newArtifactEnv(t, nil, nil, artifact.Config{BaseURL: "https://preview.example.net", ConsoleOrigin: "https://console.example.com", AllowScripts: true})
	cookies := login(e, "inline@example.com")
	id := artifactInstance(t, e, cookies, `fs.write("report/index.html", '<link rel="stylesheet" href="assets/report.css"><h1 class="report">Report</h1><script src="assets/chart.js" defer></script>')
fs.write("report/assets/report.css", '@import "./base.css"; .report {color: rgb(1, 2, 3)}')
fs.write("report/assets/base.css", 'h1 {font-size: 25px}')
fs.write("report/assets/chart.js", 'window.chartValue = 42;')
fs.write("report/unrelated.png", "not an exportable file")`)
	status, body := issuePreview(t, e, cookies, id, "report/index.html", "static")
	if status != 200 {
		t.Fatalf("static: %d", status)
	}
	status, _, html := fetchPreview(t, e, body["preview_url"].(string))
	if status != 200 || !strings.Contains(html, "25px") || !strings.Contains(html, `class="report"`) || strings.Contains(html, "<script") || strings.Contains(html, "href=") {
		t.Fatal("local CSS was not inlined into static HTML")
	}
	status, body = issuePreview(t, e, cookies, id, "report/index.html", "interactive")
	if status != 200 {
		t.Fatalf("interactive: %d", status)
	}
	status, headers, html := fetchPreview(t, e, body["preview_url"].(string))
	if status != 200 || !strings.Contains(html, "data:text/javascript;charset=utf-8;base64,") || strings.Contains(html, `src="assets/`) || !strings.Contains(headers.Get("Content-Security-Policy"), "connect-src 'none'") {
		t.Fatal("local JS was not embedded safely")
	}
	status, source := issuePreview(t, e, cookies, id, "report/index.html", "source")
	if status != 200 || !strings.Contains(source["source"].(string), `src="assets/chart.js"`) {
		t.Fatal("conversion mutated the source file")
	}
}
