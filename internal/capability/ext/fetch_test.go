package ext

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitRepo creates a git repo with the given files and a tag.
func gitRepo(t *testing.T, files map[string]string) (path, url string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	for rel, src := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{
			"-c", "user.email=t@t", "-c", "user.name=t",
			"-c", "protocol.file.allow=always"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	run("add", "-A")
	run("commit", "-q", "-m", "init")
	run("tag", "v0.1.0")
	return dir, "file://" + dir
}

// remoteOpts builds Options whose CloneURL seam always points at repo.
func remoteOpts(t *testing.T, repoURL string) *Options {
	t.Helper()
	return &Options{
		AllowSources:       []string{"example.com/acme"},
		CacheDir:           t.TempDir(),
		AllowFileTransport: true,
		CloneURL:           func(_, _, _ string) string { return repoURL },
	}
}

func loadRemote(t *testing.T, o *Options, source, sum string) (*Module, error) {
	t.Helper()
	l := &CapabilityLoader{
		Identifier: CapabilityIdentifier(source),
		Sum:        sum,
		Options:    o,
	}
	return l.Load(context.Background())
}

func TestRemoteFetch(t *testing.T) {
	repoDir, repoURL := gitRepo(t, map[string]string{
		"capability.yaml": goodManifest,
		"main.star":       "def search(q):\n    return []\n",
	})
	src := "example.com/acme/tavily@v0.1.0"
	opts := remoteOpts(t, repoURL)

	// Missing sum: error carries the computed h1 so it can be pinned.
	_, err := loadRemote(t, opts, src, "")
	if err == nil || !strings.Contains(err.Error(), "h1:") {
		t.Fatalf("want missing-sum error with h1, got %v", err)
	}
	sum := "h1:" + strings.SplitN(err.Error(), "h1:", 2)[1]

	// Wrong sum: rejected, still reports computed.
	if _, err := loadRemote(t, opts, src, "h1:AAAA"); err == nil || !strings.Contains(err.Error(), sum) {
		t.Fatalf("want mismatch error with computed sum, got %v", err)
	}

	// Correct sum: loads.
	m, err := loadRemote(t, opts, src, sum)
	if err != nil {
		t.Fatal(err)
	}
	if m.Manifest.Name != "t" || m.Commit == "" {
		t.Fatalf("bad module %+v", m)
	}

	// Cache hit re-verifies: tamper the cache and expect a mismatch.
	cached := filepath.Join(opts.CacheDir, "example.com/acme/tavily@v0.1.0", "main.star")
	if err := os.WriteFile(cached, []byte("def search(q):\n    return [1]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadRemote(t, opts, src, sum); err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("want cache-tamper mismatch, got %v", err)
	}

	// Source outside AllowSources rejected.
	opts2 := remoteOpts(t, repoURL)
	opts2.AllowSources = []string{"other.com"}
	if _, err := loadRemote(t, opts2, src, sum); err == nil || !strings.Contains(err.Error(), "allow-sources") {
		t.Fatalf("want allow-sources error, got %v", err)
	}
	_ = repoDir
}
