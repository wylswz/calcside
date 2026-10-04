//go:build linux

package subproc

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"calcside/internal/runtime"
)

func inspect(t *testing.T, s *Supervisor, id string) (runtime.ResourceUsages, error) {
	t.Helper()
	resp, err := s.Inspect(context.Background(), &runtime.InspectRequest{InstanceID: id, Owner: owner})
	if err != nil {
		return runtime.ResourceUsages{}, err
	}
	return resp.ResourceUsages, nil
}

func mustInspect(t *testing.T, s *Supervisor, id string) runtime.ResourceUsages {
	t.Helper()
	ru, err := inspect(t, s, id)
	if err != nil {
		t.Fatalf("inspect %s: %v", id, err)
	}
	return ru
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
	create(t, s, "hog")
	create(t, s, "bystander")
	for _, id := range []string{"hog", "bystander"} {
		if ru := mustInspect(t, s, id); ru.MemoryMax != limit || ru.MemoryUsage == 0 || ru.MemoryUsage >= limit {
			t.Fatalf("%s: resource usages = %+v, want max %d and 0 < usage < max (see any warning logged above)", id, ru, limit)
		} else if ru.MemoryPeak != 0 && ru.MemoryPeak < ru.MemoryUsage {
			t.Fatalf("%s: resource usages = %+v, want peak >= usage", id, ru)
		}
	}

	resp, err := run(t, s, "hog", "e1", `x = "a" * (64 << 20)`+"\nprint(len(x))")
	if err != nil || resp.Result.Error != nil || resp.Result.Output != "67108864\n" {
		t.Fatalf("exec under the limit: err=%v res=%+v", err, resp)
	}
	// Each instance is charged only for its own memory.
	hog, bystander := mustInspect(t, s, "hog").MemoryUsage, mustInspect(t, s, "bystander").MemoryUsage
	if hog < 64<<20 || hog < bystander+32<<20 {
		t.Fatalf("memory usage: hog=%d bystander=%d, want hog charged for its 64 MiB alone", hog, bystander)
	}

	// 2 GiB of live strings: far past memory.max, so the kernel must
	// OOM-kill the child rather than let it grow or swap.
	code := "def hog():\n  y = []\n  for i in range(64):\n    y.append(\"b\" * (32 << 20))\n  return y\nz = hog()"
	if _, err := run(t, s, "hog", "e2", code); err == nil {
		t.Fatal("exec over the limit succeeded")
	}
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		_, err := inspect(t, s, "hog")
		if errors.Is(err, runtime.ErrNotFound) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("instance over memory.max still alive: %v", err)
		}
	}

	resp, err = run(t, s, "bystander", "e1", "print(1)")
	if err != nil || resp.Result.Error != nil || resp.Result.Output != "1\n" {
		t.Fatalf("bystander after OOM: err=%v res=%+v", err, resp)
	}
	if ru := mustInspect(t, s, "bystander"); ru.MemoryMax != limit {
		t.Fatalf("bystander after OOM: resource usages = %+v", ru)
	}
}
