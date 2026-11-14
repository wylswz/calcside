package policy

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeDir(t *testing.T, files map[string]string) string {
	t.Helper()
	d := t.TempDir()
	for name, src := range files {
		if err := os.WriteFile(filepath.Join(d, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func TestWrongPackageRejectedAtLoad(t *testing.T) {
	dir := writeDir(t, map[string]string{
		"bad.rego": `package wrong.name
x := 1`,
	})
	if _, err := LoadDir(dir); err == nil {
		t.Fatal("expected package rejection")
	}
}

func TestLoadDirSnapshot(t *testing.T) {
	files := map[string]string{"allow.rego": "package calcside.hooks\ndeny contains msg if { false; msg := \"no\" }\n"}
	dir := writeDir(t, files)
	if err := os.WriteFile(filepath.Join(dir, "ignored.txt"), []byte("not rego"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadDir(dir)
	if err != nil || !reflect.DeepEqual(got, files) {
		t.Fatalf("snapshot: %v %v", got, err)
	}
	if got, err := LoadDir(""); err != nil || got != nil {
		t.Fatalf("disabled: %v %v", got, err)
	}
}
