package fs

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"calcside/internal/capability"
)

func TestTraversalRejections(t *testing.T) {
	v := NewVFS(Config{QuotaBytes: 1 << 20, MaxFiles: 100})
	bad := []string{"../x", "/etc/passwd", "/work/../x", "a/../../x", "/work/../../etc", "..", "../.."}
	for _, p := range bad {
		if _, err := Resolve(p); err == nil {
			t.Errorf("Resolve(%q) unexpectedly allowed", p)
		}
		if _, err := v.Write(p, "x", false); err == nil {
			t.Errorf("Write(%q) unexpectedly allowed", p)
		}
	}
}

func TestRelativeResolvesUnderWork(t *testing.T) {
	got, err := Resolve("a/b.txt")
	if err != nil || got != "/work/a/b.txt" {
		t.Fatalf("got %q err %v", got, err)
	}
	got, err = Resolve("/work/c.txt")
	if err != nil || got != "/work/c.txt" {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestQuotaExceeded(t *testing.T) {
	v := NewVFS(Config{QuotaBytes: 10, MaxFiles: 100})
	if _, err := v.Write("big.txt", strings.Repeat("x", 11), false); !errors.Is(err, ErrQuota) {
		t.Fatalf("expected ErrQuota, got %v", err)
	}
	if _, err := v.Write("ok.txt", "12345", false); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Write("ok.txt", "6789", true); err != nil { // append fills to 9
		t.Fatal(err)
	}
	if _, err := v.Write("ok.txt", "xx", true); !errors.Is(err, ErrQuota) {
		t.Fatalf("append expected ErrQuota, got %v", err)
	}
	if _, err := v.Write("ok.txt", strings.Repeat("y", 11), false); !errors.Is(err, ErrQuota) {
		t.Fatalf("overwrite expected ErrQuota, got %v", err)
	}
}

func TestMaxFiles(t *testing.T) {
	v := NewVFS(Config{QuotaBytes: 1 << 20, MaxFiles: 2})
	_, _ = v.Write("a", "1", false)
	_, _ = v.Write("b", "1", false)
	if _, err := v.Write("c", "1", false); !errors.Is(err, ErrMaxFiles) {
		t.Fatalf("expected ErrMaxFiles, got %v", err)
	}
	// overwrite of existing file still allowed
	if _, err := v.Write("a", "2", false); err != nil {
		t.Fatal(err)
	}
}

func TestReadOnly(t *testing.T) {
	v := NewVFS(Config{QuotaBytes: 1 << 20, MaxFiles: 100, ReadOnly: true})
	if _, err := v.Write("a", "x", false); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("write: %v", err)
	}
	if err := v.Mkdir("d"); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := v.Delete("x", true); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("delete: %v", err)
	}
	ok, err := v.Exists("/work")
	if err != nil || !ok {
		t.Fatalf("exists: %v %v", ok, err)
	}
}

func TestWriteCreatesParentsAndRead(t *testing.T) {
	v := NewVFS(Config{QuotaBytes: 1 << 20, MaxFiles: 100})
	if _, err := v.Write("deep/dir/f.txt", "hi", false); err != nil {
		t.Fatal(err)
	}
	s, err := v.Read("/work/deep/dir/f.txt")
	if err != nil || s != "hi" {
		t.Fatalf("read: %q %v", s, err)
	}
}

func TestListWalkStat(t *testing.T) {
	v := NewVFS(Config{QuotaBytes: 1 << 20, MaxFiles: 100})
	_, _ = v.Write("a/b/c.txt", "1", false)
	_, _ = v.Write("a/d.txt", "22", false)

	list, err := v.List("/work/a")
	if err != nil || len(list) != 2 {
		t.Fatalf("list: %v %+v", err, list)
	}
	if list[0].Name != "b" || !list[0].IsDir || list[1].Name != "d.txt" || list[1].IsDir {
		t.Fatalf("bad list %+v", list)
	}
	walk, err := v.Walk("/work")
	if err != nil || len(walk) != 4 { // a, a/b, a/b/c.txt, a/d.txt
		t.Fatalf("walk: %v %+v", err, walk)
	}
	st, err := v.Stat("a/d.txt")
	if err != nil || st.Size != 2 || st.IsDir || st.Name != "d.txt" {
		t.Fatalf("stat: %v %+v", err, st)
	}
}

func TestDeleteNonRecursiveFailsOnNonEmptyDir(t *testing.T) {
	v := NewVFS(Config{QuotaBytes: 1 << 20, MaxFiles: 100})
	_, _ = v.Write("d/f.txt", "x", false)
	if _, err := v.Delete("/work/d", false); !errors.Is(err, ErrNotEmpty) {
		t.Fatalf("expected ErrNotEmpty, got %v", err)
	}
	if _, err := v.Delete("/work/d", true); err != nil {
		t.Fatal(err)
	}
	ok, _ := v.Exists("/work/d")
	if ok {
		t.Fatal("dir still exists")
	}
}

func TestPrompt(t *testing.T) {
	cfg, err := factory{}.Validate(json.RawMessage(`{"quota_bytes": 1024, "max_files": 5, "read_only": true}`), capability.ServerLimits{})
	if err != nil {
		t.Fatal(err)
	}
	p := factory{}.Prompt(cfg)
	for _, want := range []string{"`/work`", "1024 bytes", "5 files", "read-only", "fs.read", "fs.write"} {
		if !strings.Contains(p, want) {
			t.Fatalf("missing %q in:\n%s", want, p)
		}
	}
}
