package extartifact

import (
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
)

func TestBounds(t *testing.T) {
	ok := map[string][]byte{"a.txt": []byte("x")}
	if err := Bounds(ok); err != nil {
		t.Fatalf("valid: %v", err)
	}
	big := map[string][]byte{}
	for i := 0; i < MaxFiles+1; i++ {
		big[strconv.Itoa(i)] = []byte{}
	}
	big2 := map[string][]byte{"f": make([]byte, MaxTotalBytes+1)}
	if err := Bounds(big2); err == nil {
		t.Fatal("oversize accepted")
	}
	if err := Bounds(big); err == nil {
		t.Fatal("too many files accepted")
	}
}

func TestValidatePath(t *testing.T) {
	for _, bad := range []string{"/abs", "a/../b", "a\\b", "a//b", ".", "..", "a/./b", "", "C:/x", "a\tb", "a\x7fb"} {
		if err := ValidatePath(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	for _, ok := range []string{"a.txt", "a/b/c.star", ".hidden/keep"} {
		if err := ValidatePath(ok); err != nil {
			t.Fatalf("rejected %q: %v", ok, err)
		}
	}
}

func TestInstall(t *testing.T) {
	dir := t.TempDir()
	files := map[string][]byte{"main.star": []byte("x = 1"), "lib/util.star": []byte("y = 2")}
	sum, err := TreeHash(files)
	if err != nil {
		t.Fatal(err)
	}
	d, err := Install(dir, "src", sum, files)
	if err != nil {
		t.Fatal(err)
	}
	// idempotent — same content returns existing dir
	d2, err := Install(dir, "src", sum, files)
	if err != nil || d2 != d {
		t.Fatalf("idempotent: %v %s", err, d2)
	}
	// mismatched sum rejected
	if _, err := Install(dir, "src", "h1:AAAA", files); err == nil {
		t.Fatal("bad sum installed")
	}
	// read verifies the tree
	if err := Read(d, sum); err != nil {
		t.Fatal(err)
	}
	// tampered dir fails Read
	if err := os.WriteFile(filepath.Join(d, "evil"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Read(d, sum); err == nil {
		t.Fatal("tampered dir passed Read")
	}
	if _, err := Install(dir, "src", sum, files); err == nil {
		t.Fatal("existing corrupt cache was advertised as ready")
	}
}

func TestConcurrentInstall(t *testing.T) {
	cache := t.TempDir()
	files := map[string][]byte{"main.star": []byte("x = 1")}
	sum, err := TreeHash(files)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := Install(cache, "test/mod", sum, files); err != nil {
				t.Errorf("concurrent install: %v", err)
			}
		})
	}
	wg.Wait()
}

func TestReadRejectsSymlink(t *testing.T) {
	cache := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("host data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(cache, "main.star")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := Read(cache, "h1:missing"); err == nil {
		t.Fatal("cache followed a host symlink")
	}
}
