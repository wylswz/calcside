package instance

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	capfs "calcside/internal/capability/fs"
	"calcside/internal/runtime"
	"calcside/internal/types"
)

func TestArtifactExportSnapshot(t *testing.T) {
	n := newNode(t, Options{Limits: defaultLimits()}, nil)
	t.Cleanup(n.m.StopReaper)
	id := n.mustCreate(`{"capabilities":{"fs":{}},"secrets":{"TOKEN":{"value":"private-secret"}}}`)
	n.mustExec(id, `fs.write("out/a.txt", "private-secret")
fs.write("out/b.csv", "id,value\r\nA,001\r\n")
fs.mkdir("out/empty")`)
	resp, err := n.m.Export(context.Background(), &runtime.ExportRequest{InstanceID: id, Owner: n.owner, Paths: []string{"out", "out/a.txt"}, Recursive: true})
	if err != nil || resp.Error != nil || len(resp.Files) != 4 {
		t.Fatalf("export: %v %+v", err, resp.Error)
	}
	for _, file := range resp.Files {
		if file.Path == "/work/out/a.txt" && (string(file.Content) != "[REDACTED:TOKEN]" || !file.Redacted) {
			t.Fatal("missing redaction")
		}
	}
	reads := 0
	for _, event := range resp.Audit.Events {
		if event.Op == "read" {
			reads++
		}
		if strings.Contains(event.Args, "private-secret") {
			t.Fatal("content leaked to audit")
		}
	}
	if reads != 2 {
		t.Fatalf("read audit count %d", reads)
	}
	_, err = n.m.Export(context.Background(), &runtime.ExportRequest{InstanceID: id, Owner: runtime.Owner{UserID: "other"}, Paths: []string{"out/a.txt"}})
	if !errors.Is(err, runtime.ErrNotOwner) {
		t.Fatal("ownership not enforced")
	}
	if _, err := n.m.Export(context.Background(), &runtime.ExportRequest{InstanceID: id, Paths: []string{"out/a.txt"}}); !errors.Is(err, runtime.ErrNotOwner) {
		t.Fatal("missing owner accepted")
	}
}

func TestArtifactExportRejectsPartialResults(t *testing.T) {
	for _, phase := range []string{"before", "after"} {
		t.Run(phase, func(t *testing.T) {
			n := newNode(t, Options{Limits: defaultLimits()}, nil)
			t.Cleanup(n.m.StopReaper)
			id := "ins_deny_" + phase
			policy := fmt.Sprintf(`package calcside.hooks
deny contains "blocked" if { input.phase == %q; input.capability == "fs"; input.op == "read"; input.args.path == "/work/b.txt" }`, phase)
			_, err := n.m.Create(context.Background(), &runtime.CreateRequest{InstanceID: id, Owner: n.owner, Spec: []byte(`{"capabilities":{"fs":{}}}`), ExpiresAt: time.Now().Add(time.Hour), Policies: runtime.PolicyBundle{Global: map[string]string{"deny.rego": policy}}})
			if err != nil {
				t.Fatal(err)
			}
			n.mustExec(id, `fs.write("a.txt", "first"); fs.write("b.txt", "denied")`)
			out, err := n.m.Export(context.Background(), &runtime.ExportRequest{InstanceID: id, Owner: n.owner, Paths: []string{"a.txt", "b.txt"}, Recursive: true})
			if err != nil || out.Error == nil || out.Error.Code != types.ErrCodeForbidden || len(out.Files) != 0 {
				t.Fatalf("not atomic: err=%v result=%+v", err, out)
			}
			denied := false
			for _, ev := range out.Audit.Events {
				if ev.Decision == types.DecisionDeny && string(ev.Phase) == phase {
					denied = true
				}
			}
			if !denied {
				t.Fatal("denial audit lost")
			}
		})
	}
}

func TestArtifactExportLimits(t *testing.T) {
	n := newNode(t, Options{Limits: defaultLimits()}, nil)
	t.Cleanup(n.m.StopReaper)
	id := n.mustCreate(`{"capabilities":{"fs":{}}}`)
	n.mustExec(id, `fs.write("ok.txt", "ok")
fs.write("binary.txt", "a\x00b")
fs.write("image.png", "not an image")
fs.write("big.txt", "a" * (1024 * 1024 + 1))
for i in range(101):
    fs.write("many/%d.txt" % i, "x")
for i in range(5):
    fs.write("large/%d.txt" % i, "x" * (1024 * 1024))
for i in range(257):
    fs.mkdir("wide/%d" % i)`)
	in, err := n.m.live(id, n.owner, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := in.sess.Closers[types.CapFS].(*capfs.Closer).V.Write("invalid.txt", string([]byte{255}), false); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"binary.txt", "invalid.txt", "image.png", "big.txt", "many", "large", "wide", "/outside", "/work/a\\b", "/work/C:/x", "missing.txt"} {
		out, err := n.m.Export(context.Background(), &runtime.ExportRequest{InstanceID: id, Owner: n.owner, Paths: []string{"ok.txt", p}, Recursive: true})
		if err != nil || out.Error == nil || len(out.Files) != 0 {
			t.Errorf("accepted %q: %v", p, err)
		}
	}
	in.sess.ExecMu.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = n.m.Export(ctx, &runtime.ExportRequest{InstanceID: id, Owner: n.owner, Paths: []string{"ok.txt"}})
	in.sess.ExecMu.Unlock()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting export ignored cancellation: %v", err)
	}
}

func TestArtifactAuditSerialization(t *testing.T) {
	n := newNode(t, Options{Limits: defaultLimits()}, nil)
	t.Cleanup(n.m.StopReaper)
	id := n.mustCreate(`{"capabilities":{"fs":{}}}`)
	n.mustExec(id, `fs.write("shared.txt", "seed")`)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				out, err := n.m.Export(context.Background(), &runtime.ExportRequest{InstanceID: id, Owner: n.owner, Paths: []string{"shared.txt"}})
				if err != nil || out.Error != nil || len(out.Audit.Events) != 2 {
					t.Errorf("export lost its own audit: %v", err)
					return
				}
				for _, event := range out.Audit.Events {
					if event.ExecID != "artifact" {
						t.Error("export captured another exec's audit")
					}
				}
			} else {
				execID := fmt.Sprintf("exec_%d", i)
				out, err := n.m.Exec(context.Background(), &runtime.ExecRequest{InstanceID: id, Owner: n.owner, ExecID: execID, Code: fmt.Sprintf(`print(%q); fs.write("shared.txt", %q)`, execID, execID)})
				if err != nil || out.Result.Error != nil || out.Result.Output != execID+"\n" {
					t.Errorf("exec output interleaved: %v", err)
					return
				}
				for _, event := range out.Audit.Events {
					if event.ExecID != execID {
						t.Error("exec captured another request's audit")
					}
				}
			}
		}(i)
	}
	wg.Wait()
}

func TestArtifactInlineRedactionAndAudit(t *testing.T) {
	n := newNode(t, Options{Limits: defaultLimits()}, nil)
	t.Cleanup(n.m.StopReaper)
	id := n.mustCreate(`{"capabilities":{"fs":{}},"secrets":{"TOKEN":{"value":"resource-secret"}}}`)
	n.mustExec(id, `fs.write("report/index.html", '<link rel="stylesheet" href="./view.css"><p>Report</p><script type="module">import "./view.js";</script>')
fs.write("report/view.css", 'p:after {content:"resource-secret"}')
fs.write("report/view.js", 'window.secret = "resource-secret";')
fs.write("report/unrelated.txt", "must not be captured")`)
	out, err := n.m.Export(context.Background(), &runtime.ExportRequest{InstanceID: id, Owner: n.owner, Paths: []string{"report/index.html"}, PreviewMode: "interactive"})
	if err != nil || out.Error != nil || len(out.Files) != 1 || !out.Files[0].Redacted || !strings.Contains(string(out.PreviewHTML), "[REDACTED:TOKEN]") || strings.Contains(string(out.PreviewHTML), "resource-secret") {
		t.Fatalf("inline redaction failed: %v %v", err, out.Error)
	}
	for _, match := range regexp.MustCompile(`src="data:text/javascript;charset=utf-8;base64,([^"]+)"`).FindAllSubmatch(out.PreviewHTML, -1) {
		code, err := base64.StdEncoding.DecodeString(string(match[1]))
		if err != nil || strings.Contains(string(code), "resource-secret") || !strings.Contains(string(code), "[REDACTED:TOKEN]") {
			t.Fatal("script secret was not redacted before embedding")
		}
	}
	audit, _ := json.Marshal(out.Audit)
	if strings.Contains(string(audit), "resource-secret") || strings.Contains(string(audit), "window.secret") {
		t.Fatal("resource content leaked to audit")
	}
	reads := 0
	for _, event := range out.Audit.Events {
		if event.Op == "read" {
			reads++
		}
		if strings.Contains(event.Args, "resource-secret") || strings.Contains(event.Args, "unrelated") {
			t.Fatal("content leaked or unrelated file read")
		}
	}
	if reads != 3 {
		t.Fatalf("expected one gated read per dependency, got %d", reads)
	}
	out, err = n.m.Export(context.Background(), &runtime.ExportRequest{InstanceID: id, Owner: n.owner, Paths: []string{"report/index.html"}})
	if err != nil || out.Error != nil || out.PreviewHTML != nil || out.Files[0].Redacted || !strings.Contains(string(out.Files[0].Content), `href="./view.css"`) {
		t.Fatal("conversion modified the original file or source read")
	}
}

func TestArtifactInlineDenialIsAtomic(t *testing.T) {
	for _, phase := range []string{"before", "after"} {
		t.Run(phase, func(t *testing.T) {
			n := newNode(t, Options{Limits: defaultLimits()}, nil)
			t.Cleanup(n.m.StopReaper)
			id := "ins_inline_" + phase
			policy := fmt.Sprintf(`package calcside.hooks
deny contains "blocked" if { input.phase == %q; input.capability == "fs"; input.op == "read"; input.args.path == "/work/report/denied.js" }`, phase)
			_, err := n.m.Create(context.Background(), &runtime.CreateRequest{InstanceID: id, Owner: n.owner, Spec: []byte(`{"capabilities":{"fs":{}}}`), ExpiresAt: time.Now().Add(time.Hour), Policies: runtime.PolicyBundle{Global: map[string]string{"deny.rego": policy}}})
			if err != nil {
				t.Fatal(err)
			}
			n.mustExec(id, `fs.write("report/index.html", '<script type="module">import "./denied.js";</script>'); fs.write("report/denied.js", "window.x=1;")`)
			out, err := n.m.Export(context.Background(), &runtime.ExportRequest{InstanceID: id, Owner: n.owner, Paths: []string{"report/index.html"}, PreviewMode: "interactive"})
			if err != nil || out.Error == nil || out.Error.Code != types.ErrCodeForbidden || len(out.Files) != 0 || len(out.PreviewHTML) != 0 {
				t.Fatalf("not atomic: %v %+v", err, out)
			}
			denied := false
			for _, ev := range out.Audit.Events {
				denied = denied || ev.Decision == types.DecisionDeny && string(ev.Phase) == phase
			}
			if !denied {
				t.Fatal("dependency denial audit lost")
			}
		})
	}
}

func TestArtifactInlineBounds(t *testing.T) {
	n := newNode(t, Options{Limits: defaultLimits()}, nil)
	t.Cleanup(n.m.StopReaper)
	id := n.mustCreate(`{"capabilities":{"fs":{}}}`)
	n.mustExec(id, `fs.write("report/case.html", '<script src="A.js"></script><script src="a.js"></script>')
fs.write("report/A.js", "var A=1;"); fs.write("report/a.js", "var a=1;")
fs.write("report/nul.html", '<script src="nul.js"></script>'); fs.write("report/nul.js", "a\x00b")
fs.write("report/missing.html", '<link rel="stylesheet" href="missing.css">')
fs.write("report/big.html", '<script src="big.js"></script>'); fs.write("report/big.js", 'window.big="' + "x" * (800 * 1024) + '";')
fs.write("report/count.html", "".join(['<link rel="stylesheet" href="many/%d.css">' % i for i in range(100)]))
for i in range(100):
    fs.write("report/many/%d.css" % i, "h1 { color: red }")
fs.write("report/total.html", "".join(['<link rel="stylesheet" href="large/%d.css">' % i for i in range(5)]))
for i in range(5):
    fs.write("report/large/%d.css" % i, "/*" + "x" * (1024 * 1024 - 20) + "*/h1{color:red}")`)
	for _, name := range []string{"case", "nul", "missing", "big", "count", "total"} {
		out, err := n.m.Export(context.Background(), &runtime.ExportRequest{InstanceID: id, Owner: n.owner, Paths: []string{"report/" + name + ".html"}, PreviewMode: "interactive"})
		if err != nil || out.Error == nil || len(out.Files) != 0 || len(out.PreviewHTML) != 0 {
			t.Fatalf("accepted %s: %v", name, err)
		}
		if (name == "big" || name == "count" || name == "total") && out.Error.Code != types.ErrCodeTooLarge {
			t.Errorf("incorrect size error for %s: %v", name, out.Error)
		}
	}
}
