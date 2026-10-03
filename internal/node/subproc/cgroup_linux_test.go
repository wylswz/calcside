//go:build linux

package subproc

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"calcside/internal/runtime"
)

// cgroupOf returns pid's cgroup v2 path, relative to cgroupMount.
func cgroupOf(t *testing.T, pid int) string {
	t.Helper()
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimPrefix(strings.TrimSpace(string(b)), "0::")
}

func cgroupFile(t *testing.T, group, name string) (string, bool) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(cgroupMount, group, name))
	if errors.Is(err, fs.ErrNotExist) {
		return "", false
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b)), true
}

// TestCgroupMemoryMax needs root and a delegatable cgroup v2 hierarchy;
// `make test-cgroup` runs it in a throwaway privileged container.
func TestCgroupMemoryMax(t *testing.T) {
	if os.Getenv("CALCSIDE_TEST_CGROUP") != "1" {
		t.Skip("set CALCSIDE_TEST_CGROUP=1 (root + cgroup v2), or run `make test-cgroup`")
	}
	const limit = 256 << 20
	s := newSupervisor(t, func(o *Options) {
		o.MemoryMax = limit
		o.CgroupParent = os.Getenv("CALCSIDE_TEST_CGROUP_PARENT")
	})
	if s.cg == nil {
		t.Fatal("cgroup tree not set up (see the warning logged above)")
	}
	create(t, s, "hog")
	create(t, s, "bystander")
	hog, _ := s.get("hog")

	group := cgroupOf(t, hog.cmd.Process.Pid)
	if path.Dir(group) != s.cg.parent || !strings.HasPrefix(path.Base(group), "cs-inst-") {
		t.Fatalf("child in cgroup %q, want %s/cs-inst-*", group, s.cg.parent)
	}
	if other, _ := s.get("bystander"); cgroupOf(t, other.cmd.Process.Pid) == group {
		t.Fatal("instances share a cgroup")
	}
	if v, _ := cgroupFile(t, group, "memory.max"); v != strconv.Itoa(limit) {
		t.Fatalf("memory.max = %q, want %d", v, limit)
	}
	if v, ok := cgroupFile(t, group, "memory.swap.max"); ok && v != "0" {
		t.Fatalf("memory.swap.max = %q, want 0", v)
	}

	resp, err := run(t, s, "hog", "e1", `x = "a" * (8 << 20)`+"\nprint(len(x))")
	if err != nil || resp.Result.Error != nil || resp.Result.Output != "8388608\n" {
		t.Fatalf("exec under the limit: err=%v res=%+v", err, resp)
	}

	// 2 GiB of live strings: far past memory.max, so the kernel must
	// OOM-kill the child rather than let it grow or swap.
	code := "def hog():\n  y = []\n  for i in range(64):\n    y.append(\"b\" * (32 << 20))\n  return y\nz = hog()"
	if _, err := run(t, s, "hog", "e2", code); err == nil {
		t.Fatal("exec over the limit succeeded")
	}
	select {
	case <-hog.done:
	case <-time.After(30 * time.Second):
		t.Fatal("child over memory.max still running")
	}
	if ws, ok := hog.cmd.ProcessState.Sys().(syscall.WaitStatus); !ok || !ws.Signaled() || ws.Signal() != syscall.SIGKILL {
		t.Fatalf("child exit = %v, want SIGKILL from the OOM killer", hog.cmd.ProcessState)
	}
	if _, err := run(t, s, "hog", "e3", "1"); !errors.Is(err, runtime.ErrNotFound) {
		t.Fatalf("exec on OOM-killed instance: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cgroupMount, group)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("cgroup %s not removed after exit: %v", group, err)
	}

	resp, err = run(t, s, "bystander", "e1", "print(1)")
	if err != nil || resp.Result.Error != nil || resp.Result.Output != "1\n" {
		t.Fatalf("bystander after OOM: err=%v res=%+v", err, resp)
	}
}
