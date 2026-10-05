package artifact

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"unicode/utf8"

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
