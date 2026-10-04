package subproc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"calcside/internal/capability"
	"calcside/internal/node"
	"calcside/internal/runtime"
)

const childEnv = "CALCSIDE_SUBPROC_TEST_CHILD"

// TestMain doubles as the instance process: the supervisor under test
// re-executes this test binary with childEnv set.
func TestMain(m *testing.M) {
	if os.Getenv(childEnv) == "1" {
		if err := RunChild(os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "child:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

var owner = runtime.Owner{UserID: "usr_1", Email: "u@x.com"}

func newSupervisor(t *testing.T, mut func(*Options)) *Supervisor {
	t.Helper()
	o := Options{
		Path: os.Args[0],
		Args: []string{},
		// atexit_sleep_ms: a -race child otherwise lingers 1s on exit.
		Env: []string{childEnv + "=1", "GORACE=atexit_sleep_ms=0"},
		Child: ChildConfig{Node: node.Config{
			Limits: capability.ServerLimits{
				DefaultTTL:     15 * time.Minute,
				MaxTTL:         24 * time.Hour,
				MaxExecTimeout: 5 * time.Minute,
			},
			ExtCacheDir: t.TempDir(),
		}},
	}
	if mut != nil {
		mut(&o)
	}
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func create(t *testing.T, s *Supervisor, id string) {
	t.Helper()
	_, err := s.Create(context.Background(), &runtime.CreateRequest{
		InstanceID: id, Owner: owner,
		Spec:      []byte(`{"capabilities":{}}`),
		ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("create %s: %v", id, err)
	}
}

func run(t *testing.T, s *Supervisor, id, execID, code string) (*runtime.ExecResponse, error) {
	t.Helper()
	return s.Exec(context.Background(), &runtime.ExecRequest{
		InstanceID: id, Owner: owner, ExecID: execID, Code: code,
	})
}

func TestGlobalsPersistAcrossExecs(t *testing.T) {
	s := newSupervisor(t, nil)
	create(t, s, "ins_1")
	if _, err := run(t, s, "ins_1", "e1", "x = [1]\ndef n():\n  return len(x)"); err != nil {
		t.Fatal(err)
	}
	// Functions and mutable values survive between execs because the
	// child process — not a snapshot — holds the globals.
	resp, err := run(t, s, "ins_1", "e2", "x.append(2)\nprint(n(), x)")
	if err != nil || resp.Result.Error != nil || resp.Result.Output != "2 [1, 2]\n" {
		t.Fatalf("exec: err=%v res=%+v", err, resp)
	}
	ins, err := s.Inspect(context.Background(), &runtime.InspectRequest{InstanceID: "ins_1", Owner: owner})
	if err != nil || ins.Variables["x"] != "[1, 2]" {
		t.Fatalf("inspect: err=%v vars=%v", err, ins)
	}
}

func TestInspectWithoutMemoryCap(t *testing.T) {
	s := newSupervisor(t, nil)
	create(t, s, "ins_1")
	ins, err := s.Inspect(context.Background(), &runtime.InspectRequest{InstanceID: "ins_1", Owner: owner})
	if err != nil || ins.ResourceUsages != (runtime.ResourceUsages{}) {
		t.Fatalf("inspect: err=%v resp=%+v, want zero resource usages", err, ins)
	}
}

func TestInstancesAreSeparateProcesses(t *testing.T) {
	s := newSupervisor(t, nil)
	create(t, s, "a")
	create(t, s, "b")
	pa, _ := s.get("a")
	pb, _ := s.get("b")
	if pa.cmd.Process.Pid == pb.cmd.Process.Pid || pa.cmd.Process.Pid == os.Getpid() {
		t.Fatalf("instances share a process: %d %d", pa.cmd.Process.Pid, pb.cmd.Process.Pid)
	}
	if _, err := run(t, s, "a", "e1", "v = 1"); err != nil {
		t.Fatal(err)
	}
	resp, err := run(t, s, "b", "e1", "print(v)")
	if err != nil || resp.Result.Error == nil {
		t.Fatalf("globals leaked across instances: err=%v res=%+v", err, resp)
	}
}

func TestDeleteEndsProcess(t *testing.T) {
	s := newSupervisor(t, nil)
	create(t, s, "ins_1")
	p, _ := s.get("ins_1")

	// A wrong owner is rejected by the child and must not end it.
	_, err := s.Delete(context.Background(), &runtime.DeleteRequest{InstanceID: "ins_1", Owner: runtime.Owner{UserID: "usr_2"}})
	if !errors.Is(err, runtime.ErrNotOwner) || p.exited() {
		t.Fatalf("foreign delete: err=%v exited=%v", err, p.exited())
	}

	if _, err := s.Delete(context.Background(), &runtime.DeleteRequest{InstanceID: "ins_1", Owner: owner}); err != nil {
		t.Fatal(err)
	}
	if !p.exited() || s.Count() != 0 {
		t.Fatalf("process survived delete: exited=%v count=%d", p.exited(), s.Count())
	}
	if _, err := run(t, s, "ins_1", "e1", "1"); !errors.Is(err, runtime.ErrNotFound) {
		t.Fatalf("exec after delete: %v", err)
	}
}

func TestCrashedChildIsNotFound(t *testing.T) {
	s := newSupervisor(t, nil)
	create(t, s, "ins_1")
	p, _ := s.get("ins_1")
	_ = p.cmd.Process.Kill()
	<-p.done
	if _, err := run(t, s, "ins_1", "e1", "1"); !errors.Is(err, runtime.ErrNotFound) {
		t.Fatalf("exec on crashed child: %v", err)
	}
	if s.Count() != 0 {
		t.Fatalf("crashed child still routed")
	}
}

func TestReapEndsExpiredInstances(t *testing.T) {
	now := time.Now()
	s := newSupervisor(t, func(o *Options) {
		o.ReapGrace = time.Minute
		o.Now = func() time.Time { return now }
	})
	create(t, s, "ins_1")
	p, _ := s.get("ins_1")
	s.Reap()
	if s.Count() != 1 {
		t.Fatal("reaped a live instance")
	}
	now = now.Add(2 * time.Hour)
	s.Reap()
	if s.Count() != 0 || !p.exited() {
		t.Fatalf("expired instance not reaped: count=%d exited=%v", s.Count(), p.exited())
	}
}

func TestMaxInstances(t *testing.T) {
	s := newSupervisor(t, func(o *Options) { o.MaxInstances = 1 })
	create(t, s, "a")
	_, err := s.Create(context.Background(), &runtime.CreateRequest{
		InstanceID: "b", Owner: owner, Spec: []byte(`{"capabilities":{}}`),
	})
	if !errors.Is(err, runtime.ErrTooMany) {
		t.Fatalf("want too_many, got %v", err)
	}
}

func TestBadSpecKillsChild(t *testing.T) {
	s := newSupervisor(t, nil)
	_, err := s.Create(context.Background(), &runtime.CreateRequest{
		InstanceID: "a", Owner: owner, Spec: []byte(`{"capabilities":{"nope":{}}}`),
	})
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("want bad capability error, got %v", err)
	}
	if s.Count() != 0 {
		t.Fatal("failed create left a process routed")
	}
}
