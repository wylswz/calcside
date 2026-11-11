package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"calcside/cmd/calcside/internal/service"
	"calcside/cmd/calcside/internal/service/access"
	"calcside/internal/capability"
	capfs "calcside/internal/capability/fs"
	capio "calcside/internal/capability/io"
	"calcside/internal/engine"
	"calcside/internal/runtime"
	"calcside/internal/runtime/instance"
	"calcside/internal/store"
	"calcside/internal/store/storetest"
	"calcside/internal/types"
)

type stubSecrets struct{}

func (stubSecrets) Resolve(_ context.Context, _ string, in map[string]runtime.SecretSpec) ([]runtime.Secret, map[string]runtime.SecretSpec, error) {
	return nil, in, nil
}
func (stubSecrets) Describe(_ context.Context, _ string, in map[string]runtime.SecretSpec) (map[string]runtime.SecretSpec, error) {
	return in, nil
}

type nbEnv struct {
	svc   *Service
	st    store.Store
	mgr   *instance.Manager
	actor service.Actor
	ctx   context.Context
}

func newNBEnv(t *testing.T) *nbEnv {
	t.Helper()
	return newNBEnvWrap(t, nil)
}

// newNBEnvWrap builds the env with an optional runtime decorator —
// tests wrap the manager to count or barrier Create/Exec calls.
func newNBEnvWrap(t *testing.T, wrap func(runtime.Runtime) runtime.Runtime) *nbEnv {
	t.Helper()
	return newNBEnvFull(t, wrap, nil)
}

// newNBEnvFull additionally allows wrapping the store — tests use it
// to barrier or fault store calls deterministically.
func newNBEnvFull(t *testing.T, wrapRT func(runtime.Runtime) runtime.Runtime, wrapST func(store.Store) store.Store) *nbEnv {
	t.Helper()
	ctx := context.Background()
	raw, err := store.Open(ctx, "sqlite", storetest.SQLite(t))
	if err != nil {
		t.Fatal(err)
	}
	var st store.Store = raw
	if wrapST != nil {
		st = wrapST(raw)
	}
	t.Cleanup(func() { st.Close() })
	reg := capability.NewRegistry()
	reg.Register(capfs.Factory())
	reg.Register(capio.Factory())
	limits := capability.ServerLimits{
		DefaultTTL: 15 * time.Minute, MaxTTL: 24 * time.Hour,
		MaxExecTimeout: time.Minute, MaxFSQuotaBytes: 256 << 20,
	}
	mgr := instance.New(instance.Options{
		Engine: engine.New(4), Registry: reg, Limits: limits,
		EvalTimeout: 5 * time.Second, ReapInterval: time.Hour,
	})
	t.Cleanup(mgr.StopReaper)
	var rt runtime.Runtime = mgr
	if wrapRT != nil {
		rt = wrapRT(rt)
	}
	ac := access.New(st)
	svc := New(Options{
		Store: st, Runtime: rt, Secrets: stubSecrets{}, Audit: nopAudit{},
		Registry: reg, Limits: limits, Access: ac, MaxInstancesPerUser: 10,
	})
	u, err := st.UpsertUserByEmail(ctx, "nb@x.com", "", "")
	if err != nil {
		t.Fatal(err)
	}
	return &nbEnv{
		svc: svc, st: st, mgr: mgr, ctx: ctx,
		actor: service.Actor{UserID: u.ID, Email: u.Email, Kind: types.AuthSession, WorkspaceID: store.PersonalWorkspaceID(u.ID)},
	}
}

func (e *nbEnv) create(t *testing.T) string {
	t.Helper()
	in, err := e.svc.Create(e.ctx, e.actor, []byte(`{"capabilities":{"fs":{}}}`))
	if err != nil {
		t.Fatal(err)
	}
	return in.ID
}

func sectionStateOfView(t *testing.T, e *nbEnv, id, name string) string {
	t.Helper()
	nb, err := e.svc.Notebook(e.ctx, e.actor, id)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range nb.Sections {
		if s.Name == name {
			return s.State
		}
	}
	t.Fatalf("section %s not in notebook", name)
	return ""
}

func TestNotebookSectionLifecycle(t *testing.T) {
	e := newNBEnv(t)
	id := e.create(t)

	// Sectioned exec persists the section and runs it.
	out, err := e.svc.ExecSection(e.ctx, e.actor, id, "x = 40 + 2", 0,
		&SectionInput{Name: "setup", Description: "the answer"})
	if err != nil {
		t.Fatal(err)
	}
	if out.View.Error != nil {
		t.Fatalf("exec error: %+v", out.View.Error)
	}
	if out.Section != "setup" || out.State != SectionFresh {
		t.Fatalf("outcome: %+v", out)
	}
	if out.Revision == 0 {
		t.Fatal("expected notebook revision bump")
	}

	nb, err := e.svc.Notebook(e.ctx, e.actor, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(nb.Sections) != 1 || nb.Sections[0].State != SectionFresh {
		t.Fatalf("notebook: %+v", nb.Sections)
	}
	if len(nb.Sections[0].Defines) != 1 || nb.Sections[0].Defines[0] != "x" {
		t.Fatalf("defines: %v", nb.Sections[0].Defines)
	}

	// A dependent section reads x: fresh after running.
	out, err = e.svc.ExecSection(e.ctx, e.actor, id, "y = x * 2\nprint(y)", 0,
		&SectionInput{Name: "double", Description: "twice x"})
	if err != nil {
		t.Fatal(err)
	}
	if out.View.Output != "84\n" {
		t.Fatalf("output: %q", out.View.Output)
	}
	if got := sectionStateOfView(t, e, id, "double"); got != SectionFresh {
		t.Fatalf("double state: %s", got)
	}

	// Editing setup makes it and its dependent stale.
	code := "x = 41 + 2"
	rev := nb.Revision + 1 // save bumped once for double's exec... refetch below
	nb, _ = e.svc.Notebook(e.ctx, e.actor, id)
	rev = nb.Revision
	if _, err := e.svc.SaveSection(e.ctx, e.actor, id, "setup", SaveSectionRequest{
		Code: &code, Revision: rev,
	}); err != nil {
		t.Fatal(err)
	}
	if got := sectionStateOfView(t, e, id, "setup"); got != SectionStale {
		t.Fatalf("setup after edit: %s", got)
	}
	if got := sectionStateOfView(t, e, id, "double"); got != SectionStale {
		t.Fatalf("double after upstream edit: %s", got)
	}

	// run(stale) re-runs both in order; double sees the new x.
	rep, err := e.svc.RunSections(e.ctx, e.actor, id, SectionSelector{Stale: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Results) != 2 {
		t.Fatalf("results: %+v", rep.Results)
	}
	for _, r := range rep.Results {
		if r.Status != "ok" {
			t.Fatalf("run result: %+v", r)
		}
	}
	if got := sectionStateOfView(t, e, id, "double"); got != SectionFresh {
		t.Fatalf("double after run: %s", got)
	}

	// Scratch exec does not create a section and counts drift.
	view, err := e.svc.Exec(e.ctx, e.actor, id, "scratch_var = 7", 0)
	if err != nil {
		t.Fatal(err)
	}
	if view == nil {
		t.Fatal("nil exec view")
	}
	nb, _ = e.svc.Notebook(e.ctx, e.actor, id)
	if nb.Drift != 1 {
		t.Fatalf("drift: %d", nb.Drift)
	}
	if len(nb.Sections) != 2 {
		t.Fatalf("scratch created a section: %+v", nb.Sections)
	}
}

func TestNotebookFailedSectionPreserved(t *testing.T) {
	e := newNBEnv(t)
	id := e.create(t)

	// Syntax error: the section is still persisted, marked failed.
	out, err := e.svc.ExecSection(e.ctx, e.actor, id, "def broken(:\n", 0,
		&SectionInput{Name: "bad", Description: "broken on purpose"})
	if err != nil {
		t.Fatal(err)
	}
	if out.View.Error == nil || out.View.Error.Type != types.ErrSyntax {
		t.Fatalf("expected syntax error, got %+v", out.View.Error)
	}
	if out.State != SectionFailed {
		t.Fatalf("state: %s", out.State)
	}
	d, err := e.svc.ReadSection(e.ctx, e.actor, id, "bad", false)
	if err != nil {
		t.Fatal(err)
	}
	if d.Code != "def broken(:\n" {
		t.Fatalf("failed code not preserved: %q", d.Code)
	}
	if d.Section.State != SectionFailed {
		t.Fatalf("read state: %s", d.Section.State)
	}

	// run(all) stops at the failure; later sections report not_run.
	_, err = e.svc.AddSection(e.ctx, e.actor, id, "after", "runs last", "z = 1", "", "")
	if err != nil {
		t.Fatal(err)
	}
	rep, err := e.svc.RunSections(e.ctx, e.actor, id, SectionSelector{All: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var sawBad, sawAfter bool
	for _, r := range rep.Results {
		if r.Name == "bad" && r.Status == "error" {
			sawBad = true
		}
		if r.Name == "after" {
			sawAfter = true
			if r.Status != "not_run" {
				t.Fatalf("after should be not_run, got %s", r.Status)
			}
		}
	}
	if !sawBad || !sawAfter {
		t.Fatalf("report: %+v", rep.Results)
	}
}

func TestNotebookOnceSemantics(t *testing.T) {
	e := newNBEnv(t)
	id := e.create(t)

	// once section succeeds once.
	out, err := e.svc.ExecSection(e.ctx, e.actor, id, "n = 1", 0,
		&SectionInput{Name: "init", Rerun: "once", Description: "init once"})
	if err != nil {
		t.Fatal(err)
	}
	if out.View.Error != nil {
		t.Fatal(out.View.Error)
	}

	// Bulk run skips it after success.
	rep, err := e.svc.RunSections(e.ctx, e.actor, id, SectionSelector{All: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Results) != 1 || rep.Results[0].Status != "skipped" {
		t.Fatalf("bulk should skip once: %+v", rep.Results)
	}

	// Explicit run by name still executes it.
	rep, err = e.svc.RunSections(e.ctx, e.actor, id, SectionSelector{Names: []string{"init"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Results[0].Status != "ok" {
		t.Fatalf("explicit once run: %+v", rep.Results[0])
	}
}

func TestNotebookRestore(t *testing.T) {
	e := newNBEnv(t)
	id := e.create(t)

	out, err := e.svc.ExecSection(e.ctx, e.actor, id, "secret_val = 123", 0,
		&SectionInput{Name: "load", Description: "load value"})
	if err != nil {
		t.Fatal(err)
	}
	if out.View.Error != nil {
		t.Fatal(out.View.Error)
	}
	out, err = e.svc.ExecSection(e.ctx, e.actor, id, "print(secret_val)", 0,
		&SectionInput{Name: "show", Description: "show value"})
	if err != nil {
		t.Fatal(err)
	}
	if out.View.Output != "123\n" {
		t.Fatalf("output: %q", out.View.Output)
	}

	// Simulate process loss: drop the instance from the runtime manager
	// while the DB row still says running — the next exec gets
	// ErrNotFound, which is what marks it lost and triggers restore.
	in, err := e.st.GetInstance(e.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.mgr.Delete(e.ctx, &runtime.DeleteRequest{
		InstanceID: id, Owner: runtime.Owner{UserID: in.UserID}, Epoch: in.LeaseEpoch,
	}); err != nil {
		t.Fatal(err)
	}

	// The next exec restores and returns instance_restored WITHOUT
	// executing — scratch or sectioned alike. A scratch call keeps the
	// stored section code untouched.
	_, err = e.svc.ExecSection(e.ctx, e.actor, id, "print('should not run')", 0, nil)
	var se *service.Error
	if !errors.As(err, &se) || se.Code != types.ErrCodeInstanceRestored {
		t.Fatalf("want instance_restored, got %v", err)
	}

	// Instance is running again, incarnation bumped, memory empty.
	in, _ = e.st.GetInstance(e.ctx, id)
	if in.Status != types.InstanceRunning || in.Incarnation != 2 || in.RestoredAt == nil {
		t.Fatalf("restored row: %+v", in)
	}

	// Notebook: sections preserved, all not_run, drift cleared.
	nb, err := e.svc.Notebook(e.ctx, e.actor, id)
	if err != nil {
		t.Fatal(err)
	}
	if nb.Incarnation != 2 || len(nb.Sections) != 2 {
		t.Fatalf("notebook: %+v", nb)
	}
	for _, s := range nb.Sections {
		if s.State != SectionNotRun {
			t.Fatalf("section %s state after restore: %s", s.Name, s.State)
		}
	}

	// Running "show" alone fails — the process is empty.
	rep, err := e.svc.RunSections(e.ctx, e.actor, id, SectionSelector{Names: []string{"show"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Results[0].Status != "error" {
		t.Fatalf("show on empty process should fail: %+v", rep.Results[0])
	}

	// run(all) restores state in order.
	rep, err = e.svc.RunSections(e.ctx, e.actor, id, SectionSelector{All: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rep.Results {
		if r.Status != "ok" {
			t.Fatalf("run all after restore: %+v", r)
		}
	}
	if got := sectionStateOfView(t, e, id, "show"); got != SectionFresh {
		t.Fatalf("show state: %s", got)
	}
}

// countingRuntime wraps the manager to observe Create/Exec traffic —
// restore-concurrency tests assert exactly one restore Create and no
// Exec on the new incarnation.
type countingRuntime struct {
	runtime.Runtime
	mu      sync.Mutex
	creates int
	execs   int
	epochs  []int64       // epoch of every Exec seen
	armed   chan struct{} // closed to arm the Create barrier
	createN chan struct{}
	release chan struct{}
}

func (c *countingRuntime) Create(ctx context.Context, req *runtime.CreateRequest) (*runtime.CreateResponse, error) {
	c.mu.Lock()
	c.creates++
	c.mu.Unlock()
	select {
	case <-c.armed:
		select {
		case c.createN <- struct{}{}:
		default:
		}
		select {
		case <-c.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	default:
	}
	return c.Runtime.Create(ctx, req)
}

func (c *countingRuntime) Exec(ctx context.Context, req *runtime.ExecRequest) (*runtime.ExecResponse, error) {
	c.mu.Lock()
	c.execs++
	c.epochs = append(c.epochs, req.Epoch)
	c.mu.Unlock()
	return c.Runtime.Exec(ctx, req)
}

// TestNotebookRestoreConcurrency: two callers race a lost instance.
// The op lease lets exactly one restore through — one runtime Create,
// no Exec on the new incarnation, and the loser gets a typed
// restored/conflict rather than executing into the fresh process.
func TestNotebookRestoreConcurrency(t *testing.T) {
	var cr *countingRuntime
	e := newNBEnvWrap(t, func(rt runtime.Runtime) runtime.Runtime {
		cr = &countingRuntime{
			Runtime: rt,
			armed:   make(chan struct{}),
			createN: make(chan struct{}, 8),
			release: make(chan struct{}),
		}
		return cr
	})
	id := e.create(t)

	// Seed state, then capture spec/expiry for the byte-identical check.
	out, err := e.svc.ExecSection(e.ctx, e.actor, id, "marker = 'alive'", 0,
		&SectionInput{Name: "seed", Description: "seed value"})
	if err != nil || out.View.Error != nil {
		t.Fatalf("seed exec: %v %+v", err, out)
	}
	before, err := e.st.GetInstance(e.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	specBefore := string(before.Spec)
	expBefore := before.ExpiresAt

	// Simulate process loss: the DB row still says running.
	if _, err := e.mgr.Delete(e.ctx, &runtime.DeleteRequest{
		InstanceID: id, Owner: runtime.Owner{UserID: before.UserID}, Epoch: before.LeaseEpoch,
	}); err != nil {
		t.Fatal(err)
	}

	// Caller A races the restore; the barrier holds its Create open.
	// The barrier arms only now — the instance's own create already
	// went through unimpeded.
	close(cr.armed)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, errs[0] = e.svc.ExecSection(e.ctx, e.actor, id, "print('must not run')", 0,
			&SectionInput{Name: "probe", Description: "probe"})
	}()
	select {
	case <-cr.createN:
	case <-time.After(10 * time.Second):
		t.Fatal("restore Create never reached the runtime")
	}
	// A is inside Create: it holds the op lease and the row is lost.
	// Caller B now deterministically loses the lease race — it cannot
	// be scheduled past the commit because the barrier still holds.
	_, errs[1] = e.svc.ExecSection(e.ctx, e.actor, id, "print('must not run')", 0,
		&SectionInput{Name: "probe", Description: "probe"})
	close(cr.release)
	wg.Wait()

	// Exactly one restore Create beyond the instance's own create. Exec
	// traffic: the seed plus at most the winner's doomed probe on the
	// OLD epoch — nothing may execute on the restored incarnation.
	cr.mu.Lock()
	creates, execs, epochs := cr.creates, cr.execs, append([]int64(nil), cr.epochs...)
	cr.mu.Unlock()
	if creates != 2 {
		t.Fatalf("runtime Create calls: %d, want 2 (instance + one restore)", creates)
	}
	for _, ep := range epochs {
		if ep > before.LeaseEpoch {
			t.Fatalf("exec ran on restored epoch %d (epochs seen: %v)", ep, epochs)
		}
	}
	if execs != 2 {
		t.Fatalf("runtime Exec calls: %d, want 2 (seed + one doomed probe)", execs)
	}

	// Both callers got a typed outcome: instance_restored or a lease
	// conflict — never a silent execution.
	restored, conflicted := 0, 0
	for _, err := range errs {
		var se *service.Error
		if errors.As(err, &se) && se.Code == types.ErrCodeInstanceRestored {
			restored++
		} else if errors.As(err, &se) && se.Code == types.ErrCodeConflict {
			conflicted++
		} else {
			t.Fatalf("unexpected caller error: %v", err)
		}
	}
	if restored+conflicted != 2 || restored == 0 {
		t.Fatalf("caller outcomes: restored=%d conflicted=%d errs=%v", restored, conflicted, errs)
	}

	// Spec bytes and expiry are byte-identical after restoration.
	after, err := e.st.GetInstance(e.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if string(after.Spec) != specBefore {
		t.Fatal("stored spec changed across restore")
	}
	if !after.ExpiresAt.Equal(expBefore) {
		t.Fatalf("expiry changed: %v → %v", expBefore, after.ExpiresAt)
	}
	if after.Status != types.InstanceRunning || after.Incarnation != before.Incarnation+1 {
		t.Fatalf("restored row: %+v", after)
	}

	// The new process is verifiably empty: the seed's global is gone.
	ins, err := e.mgr.Inspect(e.ctx, &runtime.InspectRequest{
		InstanceID: id, Owner: runtime.Owner{UserID: before.UserID}, Epoch: after.LeaseEpoch,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ins.Variables["marker"]; ok {
		t.Fatalf("restored process kept globals: %v", ins.Variables)
	}

	// An explicit re-run now works on the empty process.
	rep, err := e.svc.RunSections(e.ctx, e.actor, id, SectionSelector{Names: []string{"seed"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Results[0].Status != "ok" {
		t.Fatalf("seed rerun after restore: %+v", rep.Results[0])
	}
}

func TestNotebookExpiredNeverRestores(t *testing.T) {
	e := newNBEnv(t)
	id := e.create(t)

	in, err := e.st.GetInstance(e.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().UTC().Add(-time.Hour)
	in.ExpiresAt = past
	in.Status = types.InstanceExpired
	now := past
	in.EndedAt = &now
	if err := e.st.UpdateInstance(e.ctx, in); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.Exec(e.ctx, e.actor, id, "print(1)", 0)
	var se *service.Error
	if !errors.As(err, &se) || se.Code != types.ErrCodeInstanceExpired {
		t.Fatalf("want instance_expired, got %v", err)
	}
	// Still expired — no resurrection.
	in, _ = e.st.GetInstance(e.ctx, id)
	if in.Status != types.InstanceExpired {
		t.Fatalf("status: %s", in.Status)
	}
}

func TestNotebookAddSectionAndOutline(t *testing.T) {
	e := newNBEnv(t)
	id := e.create(t)

	res, err := e.svc.AddSection(e.ctx, e.actor, id, "fetch", "load data", "raw = []", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Revision == 0 {
		t.Fatal("no revision")
	}
	// Insert after fetch.
	if _, err := e.svc.AddSection(e.ctx, e.actor, id, "clean", "clean raw", "cleaned = [r for r in raw]", "", "fetch"); err != nil {
		t.Fatal(err)
	}
	nb, err := e.svc.Notebook(e.ctx, e.actor, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(nb.Sections) != 2 || nb.Sections[0].Name != "fetch" || nb.Sections[1].Name != "clean" {
		t.Fatalf("order: %+v", nb.Sections)
	}
	if len(nb.Outline) == 0 || len(nb.Next) == 0 {
		t.Fatalf("outline/next empty: %q %v", nb.Outline, nb.Next)
	}

	// Delete reports dependents.
	del, err := e.svc.DeleteSection(e.ctx, e.actor, id, "fetch", nb.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if len(del.Affected) != 1 || del.Affected[0] != "clean" {
		t.Fatalf("affected: %v", del.Affected)
	}

	// Revision CAS conflict.
	_, err = e.svc.SaveSection(e.ctx, e.actor, id, "clean", SaveSectionRequest{
		Code: strPtr("c = 1"), Revision: 1,
	})
	var se *service.Error
	if !errors.As(err, &se) || se.Code != types.ErrCodeConflict {
		t.Fatalf("want conflict, got %v", err)
	}
}

func strPtr(s string) *string { return &s }

func TestNotebookOutlineJSON(t *testing.T) {
	e := newNBEnv(t)
	id := e.create(t)
	if _, err := e.svc.ExecSection(e.ctx, e.actor, id, "a = 1", 0, &SectionInput{Name: "s1", Description: "first"}); err != nil {
		t.Fatal(err)
	}
	nb, err := e.svc.Notebook(e.ctx, e.actor, id)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(nb)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m["instance_id"] != id || m["revision"] == nil {
		t.Fatalf("outline json: %s", b)
	}
}

// flakyExecRuntime fails one chosen Exec call with ErrNotFound to
// simulate the child dying mid-batch; every other call delegates.
type flakyExecRuntime struct {
	runtime.Runtime
	mu     sync.Mutex
	calls  int
	failOn int // 1-based Exec call to fail; 0 = never
}

func (f *flakyExecRuntime) Exec(ctx context.Context, req *runtime.ExecRequest) (*runtime.ExecResponse, error) {
	f.mu.Lock()
	f.calls++
	n := f.calls
	f.mu.Unlock()
	if f.failOn > 0 && n == f.failOn {
		// Really kill the child — a bare ErrNotFound would leave the
		// instance registered and the restore's Create would collide.
		_, _ = f.Runtime.Delete(ctx, &runtime.DeleteRequest{
			InstanceID: req.InstanceID, Owner: req.Owner, Epoch: req.Epoch,
		})
		return nil, runtime.ErrNotFound
	}
	return f.Runtime.Exec(ctx, req)
}

func (f *flakyExecRuntime) execCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// TestNotebookRunDeadRuntimeKeepsExpiry: a run whose FIRST exec hits a
// dead child restores the empty process and returns the typed
// instance_restored error — and the persisted expiry is byte-identical,
// never renewed by the doomed attempt.
func TestNotebookRunDeadRuntimeKeepsExpiry(t *testing.T) {
	e := newNBEnv(t)
	id := e.create(t)
	if _, err := e.svc.AddSection(e.ctx, e.actor, id, "s1", "first section", "x = 1", "", ""); err != nil {
		t.Fatal(err)
	}
	before, err := e.st.GetInstance(e.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	expBefore := before.ExpiresAt

	// Kill the child while the row still says running.
	if _, err := e.mgr.Delete(e.ctx, &runtime.DeleteRequest{
		InstanceID: id, Owner: runtime.Owner{UserID: before.UserID}, Epoch: before.LeaseEpoch,
	}); err != nil {
		t.Fatal(err)
	}

	_, err = e.svc.RunSections(e.ctx, e.actor, id, SectionSelector{All: true}, nil)
	var se *service.Error
	if !errors.As(err, &se) || se.Code != types.ErrCodeInstanceRestored {
		t.Fatalf("dead-first run: %v, want instance_restored", err)
	}
	after, err := e.st.GetInstance(e.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ExpiresAt.Equal(expBefore) {
		t.Fatalf("expiry changed across restore: %v → %v", expBefore, after.ExpiresAt)
	}
	if after.Status != types.InstanceRunning || after.Incarnation != before.Incarnation+1 {
		t.Fatalf("restored row: %+v", after)
	}
}

// TestNotebookRunPartialRestore: the runtime dies AFTER the first
// section already executed. The run restores the empty process and
// returns an honest HTTP-200-style report — prior results retained,
// remaining selections not_run, restored flag + stopped reason set —
// never a blind retry of the sections that already ran.
func TestNotebookRunPartialRestore(t *testing.T) {
	var fr *flakyExecRuntime
	e := newNBEnvWrap(t, func(rt runtime.Runtime) runtime.Runtime {
		fr = &flakyExecRuntime{Runtime: rt, failOn: 2}
		return fr
	})
	id := e.create(t)
	if _, err := e.svc.AddSection(e.ctx, e.actor, id, "s1", "first", "x = 1", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.AddSection(e.ctx, e.actor, id, "s2", "second", "y = x + 1", "", "s1"); err != nil {
		t.Fatal(err)
	}
	before, err := e.st.GetInstance(e.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	specBefore := string(before.Spec)

	rep, err := e.svc.RunSections(e.ctx, e.actor, id, SectionSelector{All: true}, nil)
	if err != nil {
		t.Fatalf("partial run returned error: %v", err)
	}
	if !rep.Restored || rep.StoppedReason != "instance_restored" {
		t.Fatalf("report flags: %+v", rep)
	}
	if len(rep.Results) != 2 || rep.Results[0].Status != "ok" || rep.Results[1].Status != "not_run" {
		t.Fatalf("partial results: %+v", rep.Results)
	}
	warned := false
	for _, w := range rep.Warnings {
		if strings.Contains(w, "previous runtime") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("missing previous-runtime warning: %v", rep.Warnings)
	}
	after, err := e.st.GetInstance(e.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if after.Incarnation != before.Incarnation+1 || string(after.Spec) != specBefore {
		t.Fatalf("restored row: inc=%d spec-changed=%v", after.Incarnation, string(after.Spec) != specBefore)
	}
	// Exactly two execs reached the runtime: s1 ran once, s2 hit the
	// dead child — nothing was replayed into the new incarnation.
	if n := fr.execCalls(); n != 2 {
		t.Fatalf("runtime Exec calls: %d, want 2", n)
	}
}

// gateStore blocks the first ClaimInstanceOp after arm() so a test can
// interleave a restore between a caller's status read and its claim.
type gateStore struct {
	store.Store
	mu      sync.Mutex
	armed   bool
	n       int
	gate    chan struct{}
	entered chan struct{}
}

func (g *gateStore) arm() {
	g.mu.Lock()
	g.armed = true
	g.mu.Unlock()
}

func (g *gateStore) ClaimInstanceOp(ctx context.Context, id, token string, ttl time.Duration, allowed ...types.InstanceStatus) (*store.Instance, error) {
	g.mu.Lock()
	if g.armed {
		g.n++
	}
	block := g.armed && g.n == 1
	g.mu.Unlock()
	if block {
		select {
		case g.entered <- struct{}{}:
		default:
		}
		select {
		case <-g.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return g.Store.ClaimInstanceOp(ctx, id, token, ttl, allowed...)
}

// TestNotebookPrepareRunIncarnationRace: caller A reads a running row,
// then stalls inside the op claim while caller B restores the instance.
// When A's claim finally lands it sees the NEW incarnation — it must
// refuse with instance_restored instead of executing on the empty
// replacement process.
func TestNotebookPrepareRunIncarnationRace(t *testing.T) {
	var cr *countingRuntime
	var gs *gateStore
	e := newNBEnvFull(t, func(rt runtime.Runtime) runtime.Runtime {
		cr = &countingRuntime{Runtime: rt}
		return cr
	}, func(s store.Store) store.Store {
		gs = &gateStore{Store: s, gate: make(chan struct{}), entered: make(chan struct{}, 1)}
		return gs
	})
	id := e.create(t)
	before, err := e.st.GetInstance(e.ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	gs.arm()
	errs := make([]error, 2)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Scratch exec: same prepareRun path, no section bookkeeping.
		_, errs[0] = e.svc.Exec(e.ctx, e.actor, id, "print('must not run')", 0)
	}()
	select {
	case <-gs.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("caller A never reached the op claim")
	}
	// The row still says running but its child is dead: kill the child
	// AND mark the row lost, then let caller B win the restore.
	if _, err := e.mgr.Delete(e.ctx, &runtime.DeleteRequest{
		InstanceID: id, Owner: runtime.Owner{UserID: before.UserID}, Epoch: before.LeaseEpoch,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.MarkRunningAsLostForNode(e.ctx, before.NodeID); err != nil {
		t.Fatal(err)
	}
	_, errs[1] = e.svc.Exec(e.ctx, e.actor, id, "print('must not run')", 0)
	close(gs.gate)
	wg.Wait()

	for i, err := range errs {
		var se *service.Error
		if !errors.As(err, &se) || se.Code != types.ErrCodeInstanceRestored {
			t.Fatalf("caller %d: %v, want instance_restored", i, err)
		}
	}
	// No exec reached the runtime on the restored incarnation: the
	// only creates are the instance's own and B's restore.
	cr.mu.Lock()
	creates, execs := cr.creates, cr.execs
	cr.mu.Unlock()
	if creates != 2 {
		t.Fatalf("runtime Create calls: %d, want 2", creates)
	}
	if execs != 0 {
		t.Fatalf("runtime Exec calls: %d, want 0 — nothing may run on the empty process", execs)
	}
}

// TestNotebookProviderRerunStale: A defines x, B reads x. Re-running A
// with code that no longer defines x must stale B even though A itself
// is fresh — the binding B's recorded run used is gone.
func TestNotebookProviderRerunStale(t *testing.T) {
	e := newNBEnv(t)
	id := e.create(t)
	if _, err := e.svc.AddSection(e.ctx, e.actor, id, "a", "defines x", "x = 1", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.AddSection(e.ctx, e.actor, id, "b", "reads x", "y = x + 1", "", "a"); err != nil {
		t.Fatal(err)
	}
	rep, err := e.svc.RunSections(e.ctx, e.actor, id, SectionSelector{All: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rep.Results {
		if r.Status != "ok" {
			t.Fatalf("initial run: %+v", rep.Results)
		}
	}
	if got := sectionStateOfView(t, e, id, "b"); got != SectionFresh {
		t.Fatalf("b after initial run: %s", got)
	}
	// Re-run A alone with code that drops x.
	out, err := e.svc.ExecSection(e.ctx, e.actor, id, "z = 1", 0,
		&SectionInput{Name: "a", Description: "defines x"})
	if err != nil || out.View.Error != nil {
		t.Fatalf("rerun a: %v %+v", err, out)
	}
	if got := sectionStateOfView(t, e, id, "a"); got != SectionFresh {
		t.Fatalf("a after rerun: %s", got)
	}
	if got := sectionStateOfView(t, e, id, "b"); got != SectionStale {
		t.Fatalf("b after provider rerun: %s, want stale", got)
	}
}

// TestNotebookLostDerivesNotRun: while the instance is lost, sections
// that ran in the dead incarnation derive not_run — their recorded
// facts stay as history but nothing claims live state.
func TestNotebookLostDerivesNotRun(t *testing.T) {
	e := newNBEnv(t)
	id := e.create(t)
	if _, err := e.svc.AddSection(e.ctx, e.actor, id, "s1", "first", "x = 1", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.RunSections(e.ctx, e.actor, id, SectionSelector{All: true}, nil); err != nil {
		t.Fatal(err)
	}
	if got := sectionStateOfView(t, e, id, "s1"); got != SectionFresh {
		t.Fatalf("s1 before loss: %s", got)
	}
	in, err := e.st.GetInstance(e.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.MarkRunningAsLostForNode(e.ctx, in.NodeID); err != nil {
		t.Fatal(err)
	}
	if got := sectionStateOfView(t, e, id, "s1"); got != SectionNotRun {
		t.Fatalf("s1 while lost: %s, want not_run", got)
	}
	// History is retained: the meta still reports the last run.
	nb, err := e.svc.Notebook(e.ctx, e.actor, id)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range nb.Sections {
		if s.Name == "s1" && (s.LastExecID == "" || s.LastStatus != "ok") {
			t.Fatalf("lost section lost its history: %+v", s)
		}
	}
}

// TestNotebookRestoredEmptyTracksActivity: after a restore, a scratch
// exec that only writes files (no tracked defines, no drift) still
// proves the process is not verifiably empty.
func TestNotebookRestoredEmptyTracksActivity(t *testing.T) {
	e := newNBEnv(t)
	id := e.create(t)
	before, err := e.st.GetInstance(e.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.mgr.Delete(e.ctx, &runtime.DeleteRequest{
		InstanceID: id, Owner: runtime.Owner{UserID: before.UserID}, Epoch: before.LeaseEpoch,
	}); err != nil {
		t.Fatal(err)
	}
	// First exec triggers the restore and does not run.
	if _, err := e.svc.Exec(e.ctx, e.actor, id, "print('no')", 0); err == nil {
		t.Fatal("expected instance_restored")
	}
	nb, err := e.svc.Notebook(e.ctx, e.actor, id)
	if err != nil {
		t.Fatal(err)
	}
	if !nb.RestoredEmpty {
		t.Fatal("freshly restored process should report restored_empty")
	}
	// A scratch exec that mutates files without defining names: drift
	// stays 0 but the process is provably not empty.
	out, err := e.svc.Exec(e.ctx, e.actor, id, `fs.write("mem.txt", "data")`, 0)
	if err != nil {
		t.Fatal(err)
	}
	_ = out
	nb, err = e.svc.Notebook(e.ctx, e.actor, id)
	if err != nil {
		t.Fatal(err)
	}
	if nb.RestoredEmpty {
		t.Fatal("scratch fs.write after restore must clear restored_empty")
	}
}

// TestNotebookRestoredEmptyAfterFileWrite: a file upload mutates the
// restored process's VFS without producing an execution row — the
// last_active_at heartbeat is the only evidence, and restored_empty
// must still clear conservatively.
func TestNotebookRestoredEmptyAfterFileWrite(t *testing.T) {
	e := newNBEnv(t)
	id := e.create(t)
	before, err := e.st.GetInstance(e.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.mgr.Delete(e.ctx, &runtime.DeleteRequest{
		InstanceID: id, Owner: runtime.Owner{UserID: before.UserID}, Epoch: before.LeaseEpoch,
	}); err != nil {
		t.Fatal(err)
	}
	// First exec triggers the restore and does not run.
	if _, err := e.svc.Exec(e.ctx, e.actor, id, "print('no')", 0); err == nil {
		t.Fatal("expected instance_restored")
	}
	nb, err := e.svc.Notebook(e.ctx, e.actor, id)
	if err != nil {
		t.Fatal(err)
	}
	if !nb.RestoredEmpty {
		t.Fatal("freshly restored process should report restored_empty")
	}
	// An upload writes into the new process's VFS: no execution record,
	// no defines, no drift — only last_active_at moves.
	if _, err := e.svc.WriteFile(e.ctx, e.actor, id, "/work/blob.bin", []byte("payload"), false); err != nil {
		t.Fatal(err)
	}
	nb, err = e.svc.Notebook(e.ctx, e.actor, id)
	if err != nil {
		t.Fatal(err)
	}
	if nb.RestoredEmpty {
		t.Fatal("file upload after restore must clear restored_empty")
	}
}
