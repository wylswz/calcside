package artifact

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"unicode/utf8"

	"golang.org/x/net/html"

	"calcside/internal/runtime"
)

func TestPreviewOrigins(t *testing.T) {
	cases := []struct {
		preview, console string
		dev, ok          bool
	}{
		{"", "", false, true},
		{"https://reports.example.net", "https://app.example.com", false, true},
		{"https://reports.example.com", "https://app.example.com", false, false},
		{"https://preview.example.co.uk", "https://app.example.co.uk", false, false},
		{"https://app.example.com:8443", "https://app.example.com", false, false},
		{"https://preview.example.net", "https://app.preview.example.net", false, false},
		{"http://preview.example.net", "https://app.example.com", true, false},
		{"http://preview.localhost:9000", "http://localhost:5173", true, true},
		{"http://preview.localhost:9000", "http://console.localhost:9000", true, true},
		{"http://preview.localhost:9000", "http://console.localhost:9000", false, false},
		{"https://127.0.0.1", "https://app.example.com", false, false},
		{"https://preview.example.net/path", "https://app.example.com", false, false},
		{"https://user:pass@preview.example.net", "https://app.example.com", false, false},
		{"https://preview.example.net?x=y", "https://app.example.com", false, false},
		{"https://*.example.net", "https://app.example.com", false, false},
		{"https://preview.example.net:99999", "https://app.example.com", false, false},
		{"https://preview.example.net", "https://app.example.com';", false, false},
	}
	for _, tc := range cases {
		_, _, err := (Config{BaseURL: tc.preview, ConsoleOrigin: tc.console, AllowLocalHTTP: tc.dev}).Origins()
		if (err == nil) != tc.ok {
			t.Errorf("preview %q console %q: %v", tc.preview, tc.console, err)
		}
	}
	if _, _, err := (Config{AllowScripts: true}).Origins(); err == nil {
		t.Fatal("enabled scripts without isolated host")
	}
}

func TestCSVPreview(t *testing.T) {
	table, err := CSV(context.Background(), "\ufeffid,value\r\nA,001\r\nB,\"hello\nworld\"\r\n", ',')
	if err != nil || table.TotalRows != 3 || table.Rows[1][1] != "001" || table.Rows[2][1] != "hello\nworld" || table.Truncated {
		t.Fatalf("table: %+v %v", table, err)
	}
	source := strings.Repeat(strings.Repeat("界", 200)+",b\n", 220)
	table, err = CSV(context.Background(), source, ',')
	if err != nil || table.TotalRows != 220 || len(table.Rows) != 200 || !table.Truncated {
		t.Fatalf("bounds: %+v %v", table, err)
	}
	for _, row := range table.Rows {
		for _, cell := range row {
			if !utf8.ValidString(cell) || len(cell) > MaxPreviewCellBytes {
				t.Fatal("invalid truncated cell")
			}
		}
	}
	if _, err := CSV(context.Background(), strings.Repeat("a,", 256)+"a\n", ','); err == nil {
		t.Fatal("accepted excess columns")
	}
	if _, err := CSV(context.Background(), "id,value\nsecret-data\n", ','); err == nil || strings.Contains(err.Error(), "secret-data") {
		t.Fatal("malformed CSV did not fail safely")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := CSV(ctx, "a,b\n", ','); err == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestStaticHTML(t *testing.T) {
	content := []byte(`<html><head><meta http-equiv="refresh" content="0;url=https://invalid.example/"><style>@import 'https://invalid.example/a';</style></head><body><h1 onclick="steal()">Report</h1><table><tr><td style="color:red">value</td></tr></table><script>steal()</script><iframe srcdoc="attack"></iframe><form action="https://invalid.example/"><input></form><a href="https://invalid.example/">link</a><img src="https://invalid.example/pixel"><svg onload="steal()"><foreignObject>attack</foreignObject></svg></body></html>`)
	result, err := StaticHTML(context.Background(), content)
	if err != nil {
		t.Fatal(err)
	}
	text := string(result)
	for _, bad := range []string{"steal", "http-equiv", "@import", "<script", "<iframe", "<form", "<input", "href=", "<img", "foreignObject", "onload", "onclick", "srcdoc="} {
		if strings.Contains(text, bad) {
			t.Errorf("static HTML retained %s", bad)
		}
	}
	if !strings.Contains(text, "<h1>Report</h1>") || !strings.Contains(text, "<table>") {
		t.Fatalf("lost report structure: %s", text)
	}
}

func TestZIPPaths(t *testing.T) {
	files := []runtime.ArtifactFile{{Path: "/work/reports", IsDir: true}, {Path: "/work/reports/结果.csv", Content: []byte("\ufeffid,value\r\nA,001\r\n")}}
	data, err := ZIP(context.Background(), files)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if len(zr.File) != 2 || !zr.File[0].FileInfo().IsDir() || zr.File[1].Name != "reports/结果.csv" {
		t.Fatal("incorrect relative archive paths")
	}
	r, err := zr.File[1].Open()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	content, err := io.ReadAll(r)
	if err != nil || !bytes.Equal(content, files[1].Content) {
		t.Fatal("archive changed content")
	}
	for _, p := range []string{"/work/../outside", "/work//absolute", "/work/C:/drive", "/work/a\\b", "outside"} {
		if _, err := ZIP(context.Background(), []runtime.ArtifactFile{{Path: p}}); err == nil {
			t.Errorf("unsafe path: %q", p)
		}
	}
}

func TestHTMLMarkupLimitsAndSVG(t *testing.T) {
	attributes := "<div"
	for i := 0; i < 65; i++ {
		attributes += fmt.Sprintf(" data-%d='a'", i)
	}
	attributes += ">"
	for i, src := range []string{strings.Repeat("<div>", 65), strings.Repeat("<p>x</p>", 3000), attributes} {
		if _, err := StaticHTML(context.Background(), []byte(src)); err == nil {
			t.Fatalf("accepted excessive markup case %d", i)
		}
	}
	result, err := StaticHTML(context.Background(), []byte(`<svg viewBox="0 0 100 50"><rect x="0" y="0" width="100" height="20" fill="#123456"/><text x="1" y="35">Revenue</text><use href="https://invalid.example/"/><foreignObject><iframe src="https://invalid.example/"></iframe></foreignObject></svg>`))
	if err != nil || !strings.Contains(string(result), "<svg") || !strings.Contains(string(result), "<rect") || strings.Contains(string(result), "href=") || strings.Contains(string(result), "foreignObject") {
		t.Fatalf("SVG profile: %s %v", result, err)
	}
}

func TestZIPPortableNames(t *testing.T) {
	for _, names := range [][]string{{"a.txt", "A.txt"}, {"a/report.txt", "A/data.txt"}, {"é.txt", "e\u0301.txt"}, {"CON.txt"}, {"foo. "}, {"a?.txt"}, {"a\tb.txt"}} {
		var files []runtime.ArtifactFile
		for _, name := range names {
			files = append(files, runtime.ArtifactFile{Path: "/work/" + name})
		}
		if _, err := ZIP(context.Background(), files); err == nil {
			t.Errorf("accepted nonportable names: %q", names)
		}
	}
}

func inlineScriptText(t *testing.T, body []byte) string {
	t.Helper()
	var result strings.Builder
	tokens := html.NewTokenizer(bytes.NewReader(body))
	for tokens.Next() != html.ErrorToken {
		token := tokens.Token()
		if token.Data != "script" {
			continue
		}
		for _, attr := range token.Attr {
			if attr.Key != "src" {
				continue
			}
			encoded, ok := strings.CutPrefix(attr.Val, "data:text/javascript;charset=utf-8;base64,")
			if !ok {
				t.Fatal("non-embedded script")
			}
			code, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil {
				t.Fatal(err)
			}
			result.Write(code)
		}
	}
	return result.String()
}

func TestInlineHTMLModules(t *testing.T) {
	files := map[string]string{
		"/work/report/assets/main.mjs":         `import {answer} from "./nested/helper.js"; import data from "./data.json"; import "./view.css"; document.title = answer + data.n; import("./late.js").then(m => window.late = m.value);`,
		"/work/report/assets/nested/helper.js": `import {one} from "../one.js"; export const answer = 40 + one;`,
		"/work/report/assets/one.js":           `export const one = 1;`,
		"/work/report/assets/data.json":        `{"n":1}`,
		"/work/report/assets/view.css":         `.report { color: red }`,
		"/work/report/assets/late.js":          `export const value = 9;`,
	}
	seen := map[string]bool{}
	read := func(p string) ([]byte, error) {
		data, ok := files[p]
		if !ok {
			return nil, fmt.Errorf("missing %s", p)
		}
		seen[p] = true
		return []byte(data), nil
	}
	body, err := InlineHTML(context.Background(), "/work/report/index.html", []byte(`<h1 class="report">Report</h1><script type="module" src="assets/main.mjs"></script>`), read, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != len(files) || !strings.Contains(string(body), "color: red") {
		t.Fatal("module dependencies were not collected")
	}
	code := inlineScriptText(t, body)
	if strings.Contains(code, "import(") || strings.Contains(code, `from "./`) || !strings.Contains(code, "document.title") {
		t.Fatalf("dependencies not bundled: %s", code)
	}
	body, err = InlineHTML(context.Background(), "/work/report/index.html", []byte(`<script type="module">import "./assets/main.mjs";</script>`), read, true)
	if err != nil || !strings.Contains(inlineScriptText(t, body), "document.title") {
		t.Fatalf("inline module: %v", err)
	}
}

func TestInlineHTMLReferencesAndFailures(t *testing.T) {
	for _, ref := range []string{"../outside.js", "/work/report/main.js", "https://invalid.example/a.js", "//invalid.example/a.js", "file:///etc/passwd", "data:text/javascript,alert(1)", "..%2foutside.js", "assets%5ca.js", "%ff.js", "a%00.js"} {
		t.Run(ref, func(t *testing.T) {
			_, err := InlineHTML(context.Background(), "/work/report/index.html", []byte(`<script src="`+ref+`"></script>`), func(string) ([]byte, error) { t.Error("invalid URL reached VFS reader"); return nil, nil }, true)
			if err == nil {
				t.Fatal("accepted unsafe resource")
			}
		})
	}
	for _, src := range []string{
		`<script src="./image.png"></script>`,
		`<script type="importmap">{}</script>`,
		`<script type="module">console.log(1)</script><script type="module">console.log(2)</script>`,
		`<script src="./a.js" integrity="sha256-invalid"></script>`,
		`<style>@import "https://invalid.example/a.css";</style>`,
		`<style>@import "./script.js";</style>`,
		`<style>p { background: url("./image.png") }</style>`,
		`<script type="module">import "node:fs";</script>`,
		`<script type="module">import "react";</script>`,
		`<script type="module">import "file:///etc/passwd";</script>`,
		`<script type="module">import "../outside.js";</script>`,
		`<script type="module">import("./assets/" + window.name + ".js", process.env.NODE_ENV === "development" ? {with:{}} : window.options);</script>`,
		`<script type="module">import("./assets/" + window.name + ".js", window.options);</script>`,
		`<script type="module">this is not JavaScript;</script>`,
	} {
		_, err := InlineHTML(context.Background(), "/work/report/index.html", []byte(src), func(string) ([]byte, error) { t.Error("rejected input reached VFS reader"); return nil, nil }, true)
		if err == nil {
			t.Errorf("accepted unsupported resource: %s", src)
		}
	}
	denied := errors.New("denied")
	for _, src := range []string{`<script src="./a.js"></script>`, `<script type="module">import "./a.js";</script>`, `<style>@import "./a.css";</style>`} {
		body, err := InlineHTML(context.Background(), "/work/report/index.html", []byte(src), func(string) ([]byte, error) { return nil, denied }, true)
		if !errors.Is(err, denied) || body != nil {
			t.Fatal("read error or all-or-nothing guarantee lost")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := InlineHTML(ctx, "/work/report/index.html", []byte(`<h1>x</h1>`), nil, true); err == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestInlineHTMLStaticAndEscaping(t *testing.T) {
	src := []byte(`<style>p::after {content:"</style><script>alert(1)</script>"}</style>`)
	_, err := InlineHTML(context.Background(), "/work/report/index.html", src, nil, false)
	if err == nil {
		t.Fatal("accepted broken CSS after HTML raw-text termination")
	}
	body, err := InlineHTML(context.Background(), "/work/report/index.html", []byte(`<link rel="stylesheet" href="assets/颜色.css"><p id="target" class="red">Report</p><script src="./missing.js"></script>`), func(p string) ([]byte, error) {
		if p != "/work/report/assets/颜色.css" {
			t.Fatal("static preview read a script")
		}
		return []byte(`.red::after {content:"</style><script>escape()</script>"} .red {color:red}`), nil
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if strings.Contains(text, "</style><script") || !strings.Contains(text, `class="red"`) || !strings.Contains(text, `<style>`) {
		t.Fatalf("unsafe or incomplete CSS conversion: %s", text)
	}
	body, err = InlineHTML(context.Background(), "/work/report/index.html", []byte(`<script defer src="a.js"></script>`), func(string) ([]byte, error) { return []byte(`window.text = "</script><h1>not markup</h1>";`), nil }, true)
	if err != nil || !strings.Contains(string(body), "defer") || strings.Contains(string(body), "<h1>") || !strings.Contains(inlineScriptText(t, body), "not markup") {
		t.Fatalf("script embedding changed raw text or defer: %v", err)
	}
	body, err = InlineHTML(context.Background(), "/work/report/index.html", []byte(`<template><style>.report{color:red}</style></template><h1>Report</h1>`), nil, false)
	if err != nil || strings.Contains(string(body), "color") {
		t.Fatal("static conversion activated template CSS")
	}
}

func TestInlineHTMLRejectsDynamicModuleResolution(t *testing.T) {
	for _, code := range []string{
		`import(window.asset);`,
		`import("./assets/" + window.asset + ".js");`,
		"import(`../${window.asset}.js`);",
		`require("./assets/" + window.asset + ".js");`,
		`try { require("../" + window.asset); } catch {}`,
		`requ\u0069re("../" + window.asset);`,
	} {
		t.Run(code, func(t *testing.T) {
			_, err := InlineHTML(context.Background(), "/work/report/index.html", []byte(`<script type="module">`+code+`</script>`), func(string) ([]byte, error) { t.Fatal("non-literal entry reached resolution"); return nil, nil }, true)
			if err == nil {
				t.Fatal("accepted a non-literal module import")
			}
			reads := 0
			_, err = InlineHTML(context.Background(), "/work/report/index.html", []byte(`<script type="module">import "./child.js";</script>`), func(string) ([]byte, error) { reads++; return []byte(code), nil }, true)
			if err == nil || reads != 1 {
				t.Fatal("non-literal dependency reached bundling")
			}
		})
	}
}
