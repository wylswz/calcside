package fs

import (
	"context"
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
	for _, want := range []string{"`/work`", "1024 bytes", "5 files", "read-only"} {
		if !strings.Contains(p, want) {
			t.Fatalf("missing %q in:\n%s", want, p)
		}
	}
	if strings.Contains(p, "fs.read") {
		t.Fatalf("prompt should not inline operations:\n%s", p)
	}
	help := factory{}.Help(cfg)
	if len(help) < 2 || help[0].Name != "fs" || !strings.Contains(help[0].Text, "fs.read") {
		t.Fatalf("help missing capability docs: %+v", help)
	}
	if help[1].Name != "fs.read" || !strings.Contains(help[1].Text, "fs.read(path)") {
		t.Fatalf("help missing op entry: %+v", help[1])
	}
}

func TestWriteBytes(t *testing.T) {
	v := NewVFS(Config{QuotaBytes: 1 << 20, MaxFiles: 100})
	content := []byte{0x89, 0x50, 0x4e, 0x47, 0x00, 0xff, 'a', 'b'}
	e, replaced, err := v.WriteBytes("bin.dat", content, false)
	if err != nil || replaced || e.Path != "/work/bin.dat" || e.Size != int64(len(content)) {
		t.Fatalf("WriteBytes: %+v replaced=%v err=%v", e, replaced, err)
	}
	got, _ := v.Read("bin.dat")
	if got != string(content) {
		t.Fatalf("byte equality lost: %q", got)
	}
	// Mutating the caller's slice must not corrupt stored bytes.
	content[0] = 'X'
	if got, _ := v.Read("bin.dat"); []byte(got)[0] == 'X' {
		t.Fatal("WriteBytes kept a reference to caller memory")
	}
	// create-only conflicts
	if _, _, err := v.WriteBytes("bin.dat", []byte("y"), false); !errors.Is(err, ErrExist) {
		t.Fatalf("expected ErrExist, got %v", err)
	}
	e, replaced, err = v.WriteBytes("bin.dat", []byte("y"), true)
	if err != nil || !replaced || e.Size != 1 {
		t.Fatalf("overwrite: %+v replaced=%v err=%v", e, replaced, err)
	}
	// empty body is a valid empty file
	e, replaced, err = v.WriteBytes("empty.csv", nil, false)
	if err != nil || e.Size != 0 {
		t.Fatalf("empty write: %+v err=%v", e, err)
	}
	// directories and the root are not writable files
	if err := v.Mkdir("/work/d"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := v.WriteBytes("/work/d", []byte("x"), true); !errors.Is(err, ErrIsDir) {
		t.Fatalf("dir target: expected ErrIsDir, got %v", err)
	}
	if _, _, err := v.WriteBytes("/work", []byte("x"), true); !errors.Is(err, ErrIsDir) {
		t.Fatalf("root target: expected ErrIsDir, got %v", err)
	}
	// traversal still rejects
	if _, _, err := v.WriteBytes("../x", []byte("x"), false); err == nil {
		t.Fatal("traversal allowed")
	}
}

func TestWriteBytesQuotaDelta(t *testing.T) {
	v := NewVFS(Config{QuotaBytes: 10, MaxFiles: 100})
	if _, _, err := v.WriteBytes("f", []byte("12345678"), false); err != nil {
		t.Fatal(err)
	}
	// Overwrite charges the delta: 8 -> 10 fits, 8 -> 11 does not.
	if _, _, err := v.WriteBytes("f", []byte("0123456789"), true); err != nil {
		t.Fatalf("delta overwrite: %v", err)
	}
	if _, _, err := v.WriteBytes("f", make([]byte, 11), true); !errors.Is(err, ErrQuota) {
		t.Fatalf("expected ErrQuota, got %v", err)
	}
	got, _ := v.Read("f")
	if got != "0123456789" {
		t.Fatalf("failed overwrite corrupted file: %q", got)
	}
}

func TestWriteBytesReadOnly(t *testing.T) {
	v := NewVFS(Config{QuotaBytes: 1 << 20, MaxFiles: 100, ReadOnly: true})
	if _, _, err := v.WriteBytes("f", []byte("x"), true); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("expected ErrReadOnly, got %v", err)
	}
}

// denyHook denies every gated call on the given phase.
type denyHook struct{ phase string }

func (h denyHook) Name() string { return "deny" }
func (h denyHook) Before(ctx context.Context, c *capability.Call) error {
	if h.phase == "before" {
		return errors.New("no")
	}
	return nil
}
func (h denyHook) After(ctx context.Context, c *capability.Call, r *capability.Result) error {
	if h.phase == "after" {
		return errors.New("no")
	}
	return nil
}

func armedGate(t *testing.T, hooks []capability.Hook) *capability.Gate {
	t.Helper()
	g := capability.NewGate(capability.GateOwner{InstanceID: "i", UserID: "u"}, hooks, nil)
	g.Arm(capability.ExecContext{ExecID: "console", InstanceID: "i", UserID: "u"})
	return g
}

func TestAccessorWrite(t *testing.T) {
	ctx := context.Background()
	v := NewVFS(Config{QuotaBytes: 1 << 20, MaxFiles: 100})
	res, err := NewAccessor(v, armedGate(t, nil)).Write(ctx, "a.csv", []byte("x,y\n1,2\n"), false)
	if err != nil || !res.Committed || res.Replaced || res.Entry.Path != "/work/a.csv" {
		t.Fatalf("write: %+v err=%v", res, err)
	}
	if _, err := NewAccessor(v, armedGate(t, nil)).Write(ctx, "a.csv", []byte("z"), false); err == nil {
		t.Fatal("create-only overwrite allowed")
	}
	// A before-hook denial leaves nothing committed.
	res, err = NewAccessor(v, armedGate(t, []capability.Hook{denyHook{phase: "before"}})).Write(ctx, "b.csv", []byte("x"), false)
	var denied *capability.DeniedError
	if !errors.As(err, &denied) || res.Committed {
		t.Fatalf("before deny: %+v err=%v", res, err)
	}
	// An after-hook denial reports the error but the write landed.
	res, err = NewAccessor(v, armedGate(t, []capability.Hook{denyHook{phase: "after"}})).Write(ctx, "b.csv", []byte("x"), false)
	if !errors.As(err, &denied) || !res.Committed {
		t.Fatalf("after deny: %+v err=%v", res, err)
	}
	if got, _ := v.Read("b.csv"); got != "x" {
		t.Fatalf("committed write missing: %q", got)
	}
	// Disarmed gate: out of scope, nothing committed.
	g := capability.NewGate(capability.GateOwner{}, nil, nil)
	res, err = NewAccessor(v, g).Write(ctx, "c.csv", []byte("x"), false)
	if !errors.Is(err, capability.ErrOutOfScope) || res.Committed {
		t.Fatalf("out of scope: %+v err=%v", res, err)
	}
}
