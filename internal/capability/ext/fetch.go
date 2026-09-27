package ext

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/mod/sumdb/dirhash"
)

const (
	defaultFetchTimeout = 30 * time.Second
	maxTreeBytes        = 1 << 20
	maxTreeFiles        = 256
)

// Options configures the ext capability server side.
type Options struct {
	// AllowSources lists remote identifier prefixes (e.g.
	// "github.com/acme" or "github.com") the server permits; empty
	// disables remote sources entirely.
	AllowSources []string
	// LocalRoots bounds local (absolute path) sources; identifiers must
	// resolve inside one of these roots. Empty disables local sources.
	LocalRoots []string
	// LocalResolver, when set, replaces filesystem access for local
	// sources: it returns a directory containing the source tree. A
	// worker node resolves local identifiers through the API tier over
	// the wire instead of reading a filesystem it does not have.
	LocalResolver func(ctx context.Context, p ParsedIdentifier) (dir string, err error)
	// CacheDir holds fetched remote trees ({domain}/{group}/{name}@{ver}).
	CacheDir string
	// FetchTimeout bounds each remote fetch; default 30s.
	FetchTimeout time.Duration
	// CloneURL builds the git remote for a source (tests seam).
	CloneURL func(domain, group, name string) string
	// AllowFileTransport permits file:// clone URLs (tests only).
	AllowFileTransport bool
}

// fetchLocks serializes fetches per cache key.
type fetchLocks struct {
	mu sync.Mutex
	m  map[string]*sync.Mutex
}

func (l *fetchLocks) get(key string) *sync.Mutex {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.m == nil {
		l.m = map[string]*sync.Mutex{}
	}
	m, ok := l.m[key]
	if !ok {
		m = &sync.Mutex{}
		l.m[key] = m
	}
	return m
}

func (o *Options) cloneURL(p ParsedIdentifier) string {
	if o.CloneURL != nil {
		return o.CloneURL(p.Domain, p.Group, p.Name)
	}
	return fmt.Sprintf("https://%s/%s/%s.git", p.Domain, p.Group, p.Name)
}

// allowSource reports whether id's remote prefix is permitted
// (segment-boundary match against AllowSources entries).
func (o *Options) allowSource(p ParsedIdentifier) bool {
	prefix := p.Domain + "/" + p.Group + "/" + p.Name
	for _, a := range o.AllowSources {
		if prefix == a || strings.HasPrefix(prefix, a+"/") {
			return true
		}
	}
	return false
}

// localRoot resolves a local identifier and checks LocalRoots containment
// after EvalSymlinks so symlinks cannot escape the permitted roots.
func (o *Options) localRoot(p ParsedIdentifier) (string, error) {
	root, err := filepath.EvalSymlinks(p.Local)
	if err != nil {
		return "", fmt.Errorf("ext: resolving %q: %w", p.Local, err)
	}
	for _, r := range o.LocalRoots {
		if ar, err := filepath.Abs(r); err == nil {
			r = ar
		}
		rr, err := filepath.EvalSymlinks(r)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(rr, root)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return root, nil
		}
	}
	return "", fmt.Errorf("ext: local path %q is outside ext local roots", p.Local)
}

// cacheDir is where the fetched tree lands.
func (o *Options) cacheDir(p ParsedIdentifier) string {
	return filepath.Join(o.CacheDir, p.Domain, p.Group, p.Name+"@"+p.Version)
}

// hashTree computes the go-style h1: sum over a directory tree.
func hashTree(dir string) (string, error) {
	return dirhash.HashDir(dir, "", dirhash.Hash1)
}

// checkTree rejects symlinks/non-regular files and enforces size/count
// caps over the checked-out extension tree.
func checkTree(dir string) error {
	var files, bytes int64
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("ext: symlink %q not allowed in extension", path)
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("ext: non-regular file %q not allowed in extension", path)
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		files++
		bytes += fi.Size()
		if files > maxTreeFiles {
			return fmt.Errorf("ext: extension exceeds %d files", maxTreeFiles)
		}
		if bytes > maxTreeBytes {
			return fmt.Errorf("ext: extension exceeds %d bytes", maxTreeBytes)
		}
		return nil
	})
}

// verifySum checks the tree's h1 sum; want=="" (or a mismatch) is an
// error that always carries the computed value so users can pin it.
func verifySum(dir, want string) error {
	got, err := hashTree(dir)
	if err != nil {
		return fmt.Errorf("ext: hashing extension: %w", err)
	}
	if want == "" {
		return fmt.Errorf("ext: sum required for remote source; computed %s", got)
	}
	if got != want {
		return fmt.Errorf("ext: sum mismatch: got %s, want %s", got, want)
	}
	return nil
}

// git runs the git CLI with transport locked to https (file:// only when
// the test seam opts in).
func (o *Options) git(ctx context.Context, dir string, args ...string) (string, error) {
	base := []string{"-c", "protocol.allow=never", "-c", "protocol.https.allow=always"}
	if o.AllowFileTransport {
		base = append(base, "-c", "protocol.file.allow=always")
	}
	cmd := exec.CommandContext(ctx, "git", append(base, args...)...)
	cmd.Dir = dir
	env := append([]string{}, os.Environ()...)
	env = append(env, "GIT_TERMINAL_PROMPT=0")
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// fetchRemote clones the remote source into the cache (or reuses it) and
// returns the tree dir plus the resolved commit SHA. The h1 sum is
// verified on every call, including cache hits.
func (o *Options) fetchRemote(ctx context.Context, p ParsedIdentifier, wantSum string, locks *fetchLocks) (dir, commit string, err error) {
	if o.CacheDir == "" {
		return "", "", fmt.Errorf("ext: remote sources disabled (no ext cache dir)")
	}
	if !o.allowSource(p) {
		return "", "", fmt.Errorf("ext: source %s/%s/%s not allowed by server ext allow-sources", p.Domain, p.Group, p.Name)
	}
	dest := o.cacheDir(p)
	lk := locks.get(dest)
	lk.Lock()
	defer lk.Unlock()

	if st, err := os.Stat(dest); err == nil && st.IsDir() {
		if err := verifySum(dest, wantSum); err != nil {
			return "", "", err
		}
		return dest, "", nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", "", err
	}
	fctx, cancel := context.WithTimeout(ctx, o.FetchTimeout)
	if o.FetchTimeout <= 0 {
		fctx, cancel = context.WithTimeout(ctx, defaultFetchTimeout)
	}
	defer cancel()

	tmp, err := os.MkdirTemp(filepath.Dir(dest), ".fetch-*")
	if err != nil {
		return "", "", err
	}
	defer os.RemoveAll(tmp)
	url := o.cloneURL(p)
	steps := [][]string{
		{"init", "-q"},
		{"fetch", "-q", "--depth", "1", url, p.Version},
		{"checkout", "-q", "FETCH_HEAD"},
	}
	for _, s := range steps {
		if _, err := o.git(fctx, tmp, s...); err != nil {
			return "", "", fmt.Errorf("ext: fetching %s/%s/%s@%s: %w", p.Domain, p.Group, p.Name, p.Version, err)
		}
	}
	commit, err = o.git(fctx, tmp, "rev-parse", "FETCH_HEAD")
	if err != nil {
		return "", "", err
	}
	if err := os.RemoveAll(filepath.Join(tmp, ".git")); err != nil {
		return "", "", err
	}
	if err := checkTree(tmp); err != nil {
		return "", "", err
	}
	if err := verifySum(tmp, wantSum); err != nil {
		return "", "", err
	}
	if err := os.Rename(tmp, dest); err != nil {
		// Lost a race with a concurrent rename: reuse what is there.
		if st, serr := os.Stat(dest); serr == nil && st.IsDir() {
			return dest, commit, nil
		}
		return "", "", fmt.Errorf("ext: caching extension: %w", err)
	}
	return dest, commit, nil
}

// ReadLocalTree loads a local source dir as an in-memory tree for the
// API tier to serve to worker nodes. Containment, file caps, and the
// h1 sum all match what resolveLocal enforces.
func ReadLocalTree(roots []string, path string) (files map[string][]byte, sum string, err error) {
	o := Options{LocalRoots: roots}
	root, err := o.localRoot(ParsedIdentifier{Local: filepath.Clean(path)})
	if err != nil {
		return nil, "", err
	}
	if err := checkTree(root); err != nil {
		return nil, "", err
	}
	files, err = readSources(root)
	if err != nil {
		return nil, "", fmt.Errorf("ext: reading %q: %w", path, err)
	}
	sum, err = hashTree(root)
	if err != nil {
		return nil, "", err
	}
	return files, sum, nil
}

// resolveLocal validates a local source dir and verifies sum if given.
func (o *Options) resolveLocal(p ParsedIdentifier, sum string) (string, error) {
	root, err := o.localRoot(p)
	if err != nil {
		return "", err
	}
	if err := checkTree(root); err != nil {
		return "", err
	}
	if sum != "" {
		if err := verifySum(root, sum); err != nil {
			return "", err
		}
	}
	return root, nil
}

// readSources loads all regular files under root into a map keyed by
// slash-relative path.
func readSources(root string) (map[string][]byte, error) {
	out := map[string][]byte{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		data, err := io.ReadAll(f)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = data
		return nil
	})
	return out, err
}
