package artifact

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

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
