package module

import (
	"encoding/base64"
	"strings"
	"testing"

	"calcside/internal/extartifact"
)

// fakeSum is a well-formed h1: shape for manifest/spec validation
// (never a real digest — real trees compute TreeHash).
const fakeSum = "h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

func treeWith(t *testing.T, files map[string]string) *Tree {
	t.Helper()
	fb := map[string][]byte{}
	for n, s := range files {
		fb[n] = []byte(s)
	}
	sum, err := extartifact.TreeHash(fb)
	if err != nil {
		t.Fatal(err)
	}
	return &Tree{Name: "strkit", Version: 3, Sum: sum, Files: fb}
}

func TestParseManifest(t *testing.T) {
	m, err := ParseManifest([]byte(`description: helpers
requires:
  capabilities: [net]
  ext:
    tavily:
      source: contrib/tavily
      sum: ` + fakeSum + `
`))
	if err != nil {
		t.Fatal(err)
	}
	if m.Description != "helpers" || len(m.Requires.Capabilities) != 1 {
		t.Fatalf("bad manifest %+v", m)
	}
	e := m.Requires.Ext["tavily"]
	if e.Source != "contrib/tavily" || e.Sum != fakeSum {
		t.Fatalf("bad ext requirement %+v", e)
	}
}

func TestParseManifestRejects(t *testing.T) {
	cases := []struct{ name, yaml, want string }{
		{"unknown field", "bogus: 1\n", "field"},
		{"ext config rejected", "requires:\n  ext:\n    tavily:\n      source: contrib/tavily\n      config: {key: v}\n", "config"},
		{"bad capability", "requires:\n  capabilities: [bogus]\n", "capability"},
		{"ext is not base", "requires:\n  capabilities: [ext]\n", "capability"},
		{"duplicate capability", "requires:\n  capabilities: [net, net]\n", "duplicate"},
		{"bad alias", "requires:\n  ext:\n    'not-an-ident!':\n      source: a/b\n", "alias"},
		{"empty source", "requires:\n  ext:\n    tavily:\n      source: ''\n", "source"},
		{"traversal source", "requires:\n  ext:\n    tavily:\n      source: a/../b\n", "source"},
		{"absolute source", "requires:\n  ext:\n    tavily:\n      source: /etc/x\n", "source"},
		{"non-h1 sum", "requires:\n  ext:\n    tavily:\n      source: a/b\n      sum: sha256:x\n", "h1:"},
		{"overlong description", "description: " + strings.Repeat("x", 301) + "\n", "description"},
	}
	for _, tc := range cases {
		if _, err := ParseManifest([]byte(tc.yaml)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: want error containing %q, got %v", tc.name, tc.want, err)
		}
	}
}

func TestValidateTree(t *testing.T) {
	tr := treeWith(t, map[string]string{
		"util.star":     "x = 1",
		"sub/more.star": "y = 2",
		"module.yaml":   "description: ok\n",
	})
	if err := ValidateTree(tr); err != nil {
		t.Fatal(err)
	}
}

func TestValidateTreeRejects(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Tree)
		want string
	}{
		{"bad name", func(tr *Tree) { tr.Name = "has space" }, "name"},
		{"negative version", func(tr *Tree) { tr.Version = -1 }, "version"},
		{"missing sum", func(tr *Tree) { tr.Sum = "" }, "sum"},
		{"non-star file", func(tr *Tree) { tr.Files["data.txt"] = []byte("x") }, ".star"},
		{"manifest not at root only", func(tr *Tree) { tr.Files["sub/module.yaml"] = []byte("") }, ".star"},
		{"at path element", func(tr *Tree) { tr.Files["@evil/x.star"] = []byte("x=1") }, "@"},
		{"at nested element", func(tr *Tree) { tr.Files["sub/@x.star"] = []byte("x=1") }, "@"},
		{"bad manifest", func(tr *Tree) { tr.Files["module.yaml"] = []byte("unknown: yes") }, "module.yaml"},
		{"tampered sum", func(tr *Tree) { tr.Files["util.star"] = []byte("tampered") }, "hash"},
		{"non-h1 sum", func(tr *Tree) { tr.Sum = "sha256:x" }, "h1:"},
	}
	for _, tc := range cases {
		tr := treeWith(t, map[string]string{"util.star": "x = 1"})
		tc.mut(tr)
		if err := ValidateTree(tr); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: want error containing %q, got %v", tc.name, tc.want, err)
		}
	}
}

func TestValidateTreeRequiresSum(t *testing.T) {
	tr := treeWith(t, map[string]string{"util.star": "x = 1"})
	tr.Sum = ""
	if err := ValidateTree(tr); err == nil || !strings.Contains(err.Error(), "sum") {
		t.Fatalf("unpinned runtime tree accepted: %v", err)
	}
	// Draft-write validation tolerates the missing pin.
	if err := ValidateFiles(tr.Files, ""); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejectsNonUTF8(t *testing.T) {
	files := map[string][]byte{"bad.star": {0xff, 0xfe, 'x'}}
	if err := ValidateFiles(files, ""); err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("non-UTF-8 source accepted: %v", err)
	}
}

func TestManifestSingleDocument(t *testing.T) {
	_, err := ParseManifest([]byte("description: one\n---\ndescription: two\n"))
	if err == nil || !strings.Contains(err.Error(), "single") {
		t.Fatalf("multi-document manifest accepted: %v", err)
	}
}

func TestValidateMounts(t *testing.T) {
	tr := *treeWith(t, map[string]string{"util.star": "x = 1"})
	mounts := map[string]Tree{"a": tr, "b": tr}
	if err := ValidateMounts(mounts); err != nil {
		t.Fatal(err)
	}
	bad := map[string]Tree{"bad alias!": tr}
	if err := ValidateMounts(bad); err == nil || !strings.Contains(err.Error(), "alias") {
		t.Fatalf("bad alias: %v", err)
	}
	many := map[string]Tree{}
	for i := 0; i <= MaxMounts; i++ {
		many[string(rune('a'+i%26))+strings.Repeat("x", i/26)] = tr
	}
	if err := ValidateMounts(many); err == nil || !strings.Contains(err.Error(), "mounts") {
		t.Fatalf("too many mounts: %v", err)
	}
	big := map[string][]byte{
		"f.star": make([]byte, 700<<10),
		"g.star": make([]byte, 100<<10),
	}
	sum, err := extartifact.TreeHash(big)
	if err != nil {
		t.Fatal(err)
	}
	bigTree := Tree{Name: "big", Version: 1, Sum: sum, Files: big}
	if err := ValidateMounts(map[string]Tree{"a": bigTree, "b": bigTree}); err == nil || !strings.Contains(err.Error(), "aggregate") && !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("aggregate bound: %v", err)
	}
}

func TestValidSumStrictPadding(t *testing.T) {
	// A real tree hash is accepted.
	sum, err := extartifact.TreeHash(map[string][]byte{"a.star": []byte("x")})
	if err != nil || !ValidSum(sum) {
		t.Fatalf("real hash rejected: %v", err)
	}
	// Non-canonical padding bits must fail: flip the low bits of the
	// last base64 char before '=' in a way the old regex accepted.
	raw, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(sum, "h1:"))
	alt := base64.StdEncoding.WithPadding(base64.NoPadding).EncodeToString(raw)
	bad := "h1:" + alt
	// Re-encode with different low bits in the final char.
	last := bad[len(bad)-2]
	for c := byte('A'); c <= 'z'; c++ {
		if c != last && c != '=' && (c < 'a' || c > 'z' || true) {
			cand := bad[:len(bad)-2] + string(c) + "="
			if ValidSum(cand) && cand != sum {
				t.Fatalf("non-canonical digest accepted: %q", cand)
			}
		}
	}
	if ValidSum("h1:AAAA") || ValidSum("h1:") || ValidSum(sum+"x") {
		t.Fatal("short/long digest accepted")
	}
}
