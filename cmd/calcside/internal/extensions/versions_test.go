package extensions

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.starlark.net/starlark"

	"calcside/cmd/calcside/internal/api/intgen"
	"calcside/internal/capability"
	capext "calcside/internal/capability/ext"
	"calcside/internal/extartifact"
	"calcside/internal/store"
	"calcside/internal/types"
)

// writeVersion writes a version dir <root>/<name>/<ver>/ with a
// manifest whose version matches ver (EP-0020).
func writeVersion(t *testing.T, root, name, ver, manifestVersion, mainSrc string, extra map[string]string) string {
	t.Helper()
	dir := filepath.Join(root, name, ver)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"capability.yaml": "name: " + name + "\nversion: " + manifestVersion + "\nops: [{name: echo, params: [value]}]\n",
		"main.star":       mainSrc,
	}
	for k, v := range extra {
		files[k] = v
	}
	for n, src := range files {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestEP0020TwoVersionsOneFamily: a fresh database admits two version
// directories of one family independently; both resolve under
// different aliases (EP-0020).
func TestEP0020TwoVersionsOneFamily(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "fixtures")
	writeVersion(t, root, "echo", "0.1.0", "0.1.0", "def echo(value): return 1\n", nil)
	writeVersion(t, root, "echo", "0.2.0", "0.2.0", "def echo(value): return 2\n", nil)
	st := &artifactStore{}
	repo := mustRepo(t, st, capext.Options{CacheDir: t.TempDir(), LocalRoots: []string{root}})

	cat := NewCatalog(repo).Available(ctx)
	if len(cat.Extensions) != 2 || len(cat.Diagnostics) != 0 {
		t.Fatalf("catalog: %+v", cat)
	}
	sums := map[string]string{}
	for _, e := range cat.Extensions {
		if e.Family != "fixtures/echo" || e.Source != e.Family+"@"+e.Version {
			t.Fatalf("identity: %+v", e)
		}
		sums[e.Version] = e.Sum
	}
	if sums["0.1.0"] == sums["0.2.0"] {
		t.Fatal("versions shared a sum")
	}

	// Both resolve under different aliases through repo options.
	f := capext.Factory(repo.Options())
	cfg, err := f.Validate([]byte(`{"a":{"source":"fixtures/echo@0.1.0"},"b":{"source":"fixtures/echo@0.2.0"}}`), capability.ServerLimits{})
	if err != nil {
		t.Fatal(err)
	}
	gate := capability.NewGate(capability.GateOwner{InstanceID: "ins"}, nil, nil)
	v, closer, err := f.New(cfg, capability.InstanceEnv{Context: ctx, Gate: gate,
		Bindings: map[types.CapabilityName]starlark.Value{}})
	if err != nil {
		t.Fatal(err)
	}
	if closer != nil {
		defer closer.Close()
	}
	thread := &starlark.Thread{Name: "t"}
	thread.SetLocal(capability.ContextKey, ctx)
	gate.Arm(capability.ExecContext{ExecID: "e"})
	defer gate.Disarm()
	for alias, want := range map[string]int64{"a": 1, "b": 2} {
		extv, _ := v.(starlark.HasAttrs).Attr(alias)
		op, _ := extv.(starlark.HasAttrs).Attr("echo")
		got, err := starlark.Call(thread, op, starlark.Tuple{starlark.MakeInt(0)}, nil)
		n, _ := got.(starlark.Int).Int64()
		if err != nil || n != want {
			t.Fatalf("alias %s: %v %v", alias, got, err)
		}
	}
}

// TestEP0020ManifestMismatch: identifier suffix, directory name and
// manifest.version must agree (EP-0020).
func TestEP0020ManifestMismatch(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "fixtures")
	writeVersion(t, root, "echo", "0.1.0", "9.9.9", "def echo(value): return 1\n", nil)
	repo := mustRepo(t, &artifactStore{}, capext.Options{CacheDir: t.TempDir(), LocalRoots: []string{root}})
	if err := repo.Admit(ctx, "fixtures/echo@0.1.0"); err == nil || !strings.Contains(err.Error(), "0.1.0") {
		t.Fatalf("manifest/dir mismatch admitted: %v", err)
	}
	if err := repo.Admit(ctx, "fixtures/echo@9.9.9"); err == nil {
		t.Fatal("missing dir admitted")
	}
	cat := NewCatalog(repo).Available(ctx)
	if len(cat.Extensions) != 0 || len(cat.Diagnostics) == 0 {
		t.Fatalf("catalog: %+v", cat)
	}
}

// TestEP0020SubtreeIsolation: sibling version dirs and family-level
// files never alter a captured version's sum (EP-0020).
func TestEP0020SubtreeIsolation(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "fixtures")
	writeVersion(t, root, "echo", "0.1.0", "0.1.0", "def echo(value): return 1\n", nil)
	st := &artifactStore{}
	repo := mustRepo(t, st, capext.Options{CacheDir: t.TempDir(), LocalRoots: []string{root}})
	a1, err := repo.Capture(ctx, "fixtures/echo@0.1.0", "")
	if err != nil {
		t.Fatal(err)
	}
	writeVersion(t, root, "echo", "0.2.0", "0.2.0", "def echo(value): return 2\n", nil)
	if err := os.WriteFile(filepath.Join(root, "echo", "NOTES.md"), []byte("family level\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a2, err := repo.Capture(ctx, "fixtures/echo@0.1.0", "")
	if err != nil || a2.Sum != a1.Sum {
		t.Fatalf("captured version drifted: %v", err)
	}
}

// TestEP0020BoundVersionConflict: recapture of the same sum is
// idempotent; edited content under a bound version is a conflict with
// a diagnostic, and the archived original stays listed and resolvable
// after the directory disappears (EP-0020).
func TestEP0020BoundVersionConflict(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "fixtures")
	dir := writeVersion(t, root, "echo", "0.1.0", "0.1.0", "def echo(value): return 1\n", nil)
	st := &artifactStore{}
	repo := mustRepo(t, st, capext.Options{CacheDir: t.TempDir(), LocalRoots: []string{root}})
	a, err := repo.Capture(ctx, "fixtures/echo@0.1.0", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Admit(ctx, "fixtures/echo@0.1.0"); err != nil {
		t.Fatalf("same-sum admit: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.star"), []byte("def echo(value): return 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := repo.Admit(ctx, "fixtures/echo@0.1.0"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("edited bound version admitted: %v", err)
	}
	cat := NewCatalog(repo).Available(ctx)
	if len(cat.Extensions) != 1 || cat.Extensions[0].Sum != a.Sum {
		t.Fatalf("archived original not listed: %+v", cat)
	}
	if len(cat.Diagnostics) == 0 {
		t.Fatal("missing admission diagnostic")
	}
	// Deleting the version dir leaves the binding resolvable and listed.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Resolve(ctx, "fixtures/echo@0.1.0", a.Sum); err != nil {
		t.Fatalf("archived version unresolvable: %v", err)
	}
	if cat := NewCatalog(repo).Available(ctx); len(cat.Extensions) != 1 {
		t.Fatalf("deleted version unlisted: %+v", cat)
	}
	// Revoking the root denies resolution even on a store hit.
	revoked := mustRepo(t, st, capext.Options{CacheDir: t.TempDir()})
	if _, err := revoked.Resolve(ctx, "fixtures/echo@0.1.0", a.Sum); err == nil {
		t.Fatal("revoked family resolved")
	}
	if cat := NewCatalog(revoked).Available(ctx); len(cat.Extensions) != 0 {
		t.Fatalf("revoked family listed: %+v", cat)
	}
}

// TestEP0020ConcurrentCapture: concurrent captures of one
// (family,version) with different content yield a single winner
// (EP-0020).
func TestEP0020ConcurrentCapture(t *testing.T) {
	ctx := context.Background()
	st := &artifactStore{}
	repos := [2]*Repository{}
	for i := range repos {
		root := filepath.Join(t.TempDir(), "fixtures")
		writeVersion(t, root, "race", "0.1.0", "0.1.0", "def echo(value): return "+string(rune('1'+i))+"\n", nil)
		repos[i] = mustRepo(t, st, capext.Options{CacheDir: t.TempDir(), LocalRoots: []string{root}})
	}
	var wg sync.WaitGroup
	errs := [2]error{}
	for i := range repos {
		wg.Go(func() { _, errs[i] = repos[i].Capture(ctx, "fixtures/race@0.1.0", "") })
	}
	wg.Wait()
	b, err := st.GetExtensionVersion(ctx, "fixtures/race", "0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	switch {
	case errs[0] == nil && errors.Is(errs[1], store.ErrConflict):
	case errs[1] == nil && errors.Is(errs[0], store.ErrConflict):
	default:
		t.Fatalf("expected one winner: %v %v", errs[0], errs[1])
	}
	if a, err := st.GetExtensionArtifact(ctx, b.ArtifactSource, b.Sum); err != nil || a == nil {
		t.Fatalf("winning artifact missing: %v", err)
	}
}

// TestEP0020ExpectedSumMismatch: a wrong expected sum denies capture
// and resolution (EP-0020).
func TestEP0020ExpectedSumMismatch(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "fixtures")
	writeVersion(t, root, "echo", "0.1.0", "0.1.0", "def echo(value): return 1\n", nil)
	repo := mustRepo(t, &artifactStore{}, capext.Options{CacheDir: t.TempDir(), LocalRoots: []string{root}})
	a, err := repo.Capture(ctx, "fixtures/echo@0.1.0", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Capture(ctx, "fixtures/echo@0.1.0", "h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("mismatched pin captured: %v", err)
	}
	if _, err := repo.Resolve(ctx, "fixtures/echo@0.1.0", "h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("mismatched pin resolved: %v (sum %s)", err, a.Sum)
	}
}

// TestEP0020PinSpecLegacy: legacy unversioned source+sum pairs resolve
// by exact artifact match, migrate to family@manifestVersion when the
// stored manifest permits, stay legacy on conflict, and resolve a sole
// filesystem candidate at pin time (EP-0020).
func TestEP0020PinSpecLegacy(t *testing.T) {
	ctx := context.Background()
	st := &artifactStore{}
	files := map[string][]byte{
		"capability.yaml": []byte("name: legacy\nversion: 3.2.1\nops: [{name: a}]\n"),
		"main.star":       []byte("def a():\n    return 1\n"),
	}
	sum, err := extartifact.TreeHash(files)
	if err != nil {
		t.Fatal(err)
	}
	// Legacy artifact stored under the unversioned source.
	if err := st.PutExtensionArtifact(ctx, &store.ExtensionArtifact{
		Source: "fixtures/legacy", Sum: sum, Files: files, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "fixtures")
	repo := mustRepo(t, st, capext.Options{CacheDir: t.TempDir(), LocalRoots: []string{root}})
	spec := []byte(`{"capabilities":{"ext":{"l":{"source":"fixtures/legacy","sum":"` + sum + `"}}}}`)
	out, err := repo.PinSpec(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "fixtures/legacy@3.2.1") {
		t.Fatalf("legacy pair not canonicalized: %s", out)
	}
	// ExtTree serves the migrated artifact via the binding's artifact_source.
	b, err := st.GetExtensionVersion(ctx, "fixtures/legacy", "3.2.1")
	if err != nil || b.ArtifactSource != "fixtures/legacy" {
		t.Fatalf("binding: %v %+v", err, b)
	}
	resp, err := extTrees{store: st}.ExtTree(ctx, intgen.ExtTreeRequestObject{
		Params: intgen.ExtTreeParams{Path: "fixtures/legacy@3.2.1", Sum: sum}})
	if err != nil {
		t.Fatal(err)
	}
	ok200, ok := resp.(intgen.ExtTree200JSONResponse)
	if !ok || ok200.Sum != sum || len(ok200.Files) != 2 {
		t.Fatalf("legacy-mapped artifact not served: %#v", resp)
	}

	// A conflicting binding claims the version: the legacy pair is
	// returned unchanged.
	files2 := map[string][]byte{
		"capability.yaml": []byte("name: legacy2\nversion: 1.0.0\nops: [{name: a}]\n"),
		"main.star":       []byte("def a():\n    return 1\n"),
	}
	sum2, _ := extartifact.TreeHash(files2)
	st.PutExtensionArtifact(ctx, &store.ExtensionArtifact{Source: "fixtures/legacy2", Sum: sum2, Files: files2, CreatedAt: time.Now().UTC()})
	otherFiles := map[string][]byte{"capability.yaml": []byte("name: o\nversion: 1.0.0\nops: [{name: a}]\n"), "main.star": []byte("def a():\n    return 9\n")}
	other, _ := extartifact.TreeHash(otherFiles)
	st.PutExtensionArtifact(ctx, &store.ExtensionArtifact{Source: "fixtures/legacy2@1.0.0", Sum: other, Files: otherFiles, CreatedAt: time.Now().UTC()})
	if err := st.BindExtensionVersion(ctx, &store.ExtensionVersion{Family: "fixtures/legacy2", Version: "1.0.0", Sum: other, ArtifactSource: "fixtures/legacy2@1.0.0", CreatedAt: time.Now().UTC()}, nil); err != nil {
		t.Fatal(err)
	}
	out, err = repo.PinSpec(ctx, []byte(`{"capabilities":{"ext":{"l":{"source":"fixtures/legacy2","sum":"`+sum2+`"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "legacy2@") || !strings.Contains(string(out), `"fixtures/legacy2"`) {
		t.Fatalf("conflicting legacy pair rewritten: %s", out)
	}

	// Sole filesystem candidate resolves at pin time.
	writeVersion(t, root, "solo", "0.1.0", "0.1.0", "def echo(value): return 1\n", nil)
	out, err = repo.PinSpec(ctx, []byte(`{"capabilities":{"ext":{"s":{"source":"fixtures/solo"}}}}`))
	if err != nil || !strings.Contains(string(out), "fixtures/solo@0.1.0") {
		t.Fatalf("sole candidate not resolved: %v %s", err, out)
	}
	// Multiple candidates fail — never a "latest" pick.
	writeVersion(t, root, "multi", "0.1.0", "0.1.0", "def echo(value): return 1\n", nil)
	writeVersion(t, root, "multi", "0.2.0", "0.2.0", "def echo(value): return 1\n", nil)
	if _, err := repo.PinSpec(ctx, []byte(`{"capabilities":{"ext":{"m":{"source":"fixtures/multi"}}}}`)); err == nil {
		t.Fatal("multi-candidate unversioned source pinned")
	}
}

// gitRepo creates a git repo with the given files and a tag.
func gitRepo(t *testing.T, files map[string]string) string {
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
	return "file://" + dir
}

// TestEP0020RemoteBinding: a bound remote ref resolves from the archive
// after the remote is retagged; a different sum under the same ref is a
// conflict and a fresh differing fetch is a mismatch, not a rebind
// (EP-0020).
func TestEP0020RemoteBinding(t *testing.T) {
	ctx := context.Background()
	url := gitRepo(t, map[string]string{
		"capability.yaml": "name: r\nversion: 0.1.0\nops: [{name: a}]\n",
		"main.star":       "def a():\n    return 1\n",
	})
	src := "example.com/acme/r@v0.1.0"
	opts := func() capext.Options {
		return capext.Options{AllowSources: []string{"example.com/acme"}, CacheDir: t.TempDir(),
			AllowFileTransport: true, CloneURL: func(_, _, _ string) string { return url }}
	}
	st := &artifactStore{}
	repo := mustRepo(t, st, opts())
	// Remote captures require an integrity sum; the first missing-sum
	// error carries the computed h1 to pin.
	_, err := repo.Capture(ctx, src, "")
	if err == nil || !strings.Contains(err.Error(), "h1:") {
		t.Fatalf("missing-sum error: %v", err)
	}
	pin := "h1:" + strings.SplitN(err.Error(), "h1:", 2)[1]
	a1, err := repo.Capture(ctx, src, pin)
	if err != nil {
		t.Fatal(err)
	}
	// Retag the remote with different content.
	dir := strings.TrimPrefix(url, "file://")
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.email=t@t", "-c", "user.name=t",
			"-c", "protocol.file.allow=always"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "main.star"), []byte("def a():\n    return 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-q", "-m", "v2")
	run("tag", "-f", "v0.1.0")

	// The binding keeps serving the original archive — no refetch.
	a2, err := repo.Capture(ctx, src, "")
	if err != nil || a2.Sum != a1.Sum {
		t.Fatalf("bound ref refetched or drifted: %v", err)
	}
	// A different expected sum under the bound ref is a conflict.
	if _, err := repo.Capture(ctx, src, "h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("different sum rebound: %v", err)
	}
	// A fresh fetch with the stale pin is a mismatch, never a rebind.
	fresh := mustRepo(t, &artifactStore{}, opts())
	if _, err := fresh.Capture(ctx, src, a1.Sum); err == nil || errors.Is(err, store.ErrConflict) {
		t.Fatalf("differing fetch rebound instead of mismatching: %v", err)
	}
}

// TestEP0020ExtTree: the worker callback serves canonical, legacy-exact
// and legacy-mapped artifacts; a wrong sum is not found (EP-0020).
func TestEP0020ExtTree(t *testing.T) {
	ctx := context.Background()
	st := &artifactStore{}
	files := map[string][]byte{
		"capability.yaml": []byte("name: x\nversion: 0.1.0\nops: [{name: a}]\n"),
		"main.star":       []byte("def a():\n    return 1\n"),
	}
	sum, err := extartifact.TreeHash(files)
	if err != nil {
		t.Fatal(err)
	}
	// Canonical artifact served by exact (path,sum).
	st.PutExtensionArtifact(ctx, &store.ExtensionArtifact{Source: "fixtures/x@0.1.0", Sum: sum, Files: files, CreatedAt: time.Now().UTC()})
	trees := extTrees{store: st}
	resp, err := trees.ExtTree(ctx, intgen.ExtTreeRequestObject{
		Params: intgen.ExtTreeParams{Path: "fixtures/x@0.1.0", Sum: sum}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := resp.(intgen.ExtTree200JSONResponse); !ok {
		t.Fatalf("canonical artifact not served: %#v", resp)
	}
	// Legacy-mapped: bound under the canonical path, artifact stored
	// under a different source.
	if err := st.BindExtensionVersion(ctx, &store.ExtensionVersion{
		Family: "fixtures/x", Version: "0.2.0", Sum: sum,
		ArtifactSource: "fixtures/x@0.1.0", CreatedAt: time.Now().UTC(),
	}, nil); err != nil {
		t.Fatal(err)
	}
	resp, err = trees.ExtTree(ctx, intgen.ExtTreeRequestObject{
		Params: intgen.ExtTreeParams{Path: "fixtures/x@0.2.0", Sum: sum}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := resp.(intgen.ExtTree200JSONResponse); !ok {
		t.Fatalf("binding-mapped artifact not served: %#v", resp)
	}
	// Wrong sum -> not found.
	resp, err = trees.ExtTree(ctx, intgen.ExtTreeRequestObject{
		Params: intgen.ExtTreeParams{Path: "fixtures/x@0.1.0", Sum: "h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}})
	if err != nil {
		t.Fatal(err)
	}
	if nf, ok := resp.(intgen.ExtTreedefaultJSONResponse); !ok || nf.StatusCode != 404 {
		t.Fatalf("wrong sum not 404: %#v", resp)
	}
}
