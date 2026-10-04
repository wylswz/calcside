//go:build linux

package subproc

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path"
	"path/filepath"
	"slices"

	"github.com/containerd/cgroups/v3"
	"github.com/containerd/cgroups/v3/cgroup2"
)

const cgroupMount = "/sys/fs/cgroup"

var (
	_ iCgroupTree    = (*cgroupTree)(nil)
	_ iInstanceGroup = (*instanceCgroup)(nil)
)

// cgroupTree is the cgroup v2 group that per-instance groups live under.
type cgroupTree struct {
	parent string
	max    int64
}

// instanceCgroup is one instance process's own group.
type instanceCgroup struct{ m *cgroup2.Manager }

func newCgroupTree(parent string, max int64) (iCgroupTree, error) {
	if cgroups.Mode() != cgroups.Unified {
		return nil, errors.New("no cgroup v2 unified hierarchy at " + cgroupMount)
	}
	if parent == "" {
		self, err := cgroup2.NestedGroupPath("")
		if err != nil {
			return nil, err
		}
		parent = self
		// cgroup v2 only lets a group without member processes hand
		// controllers to its children, so the supervisor steps into a
		// leaf of its own group first.
		if path.Base(self) == "supervisor" {
			parent = path.Dir(self)
		} else {
			leaf := path.Join(self, "supervisor")
			m, err := loadGroup(leaf)
			if err != nil {
				return nil, err
			}
			if err := m.AddProc(uint64(os.Getpid())); err != nil {
				return nil, fmt.Errorf("move supervisor into %s: %w", leaf, err)
			}
		}
	}
	m, err := loadGroup(parent)
	if err != nil {
		return nil, err
	}
	// The supervisor never writes above parent: whoever set it up (root,
	// or docker/worker-entrypoint.sh for an unprivileged supervisor) must
	// have handed it the memory controller.
	ctrls, err := m.Controllers()
	if err != nil {
		return nil, err
	}
	if !slices.Contains(ctrls, "memory") {
		return nil, fmt.Errorf("memory controller not delegated to %s: enable +memory in its parent's cgroup.subtree_control", parent)
	}
	dir := filepath.Join(cgroupMount, parent)
	if err := os.WriteFile(filepath.Join(dir, "cgroup.subtree_control"), []byte("+memory"), 0); err != nil {
		return nil, fmt.Errorf("enable memory controller under %s: %w", parent, err)
	}
	// Groups left by a previous supervisor are empty once their children
	// saw stdin EOF; rmdir only succeeds on empty groups.
	stale, _ := filepath.Glob(filepath.Join(dir, "cs-inst-*"))
	for _, d := range stale {
		_ = os.Remove(d)
	}
	return &cgroupTree{parent: parent, max: max}, nil
}

// loadGroup creates group (relative to the mount) if missing and binds a
// manager to it. Unlike cgroup2.NewManager it never writes the
// ancestors' cgroup.subtree_control, which an unprivileged supervisor
// cannot open.
func loadGroup(group string) (*cgroup2.Manager, error) {
	if err := os.MkdirAll(filepath.Join(cgroupMount, group), 0o755); err != nil {
		return nil, err
	}
	return cgroup2.Load(group, cgroup2.WithMountpoint(cgroupMount))
}

func (t *cgroupTree) create(name string) (iInstanceGroup, error) {
	group := path.Join(t.parent, name)
	m, err := loadGroup(group)
	if err != nil {
		return nil, err
	}
	if err := m.Update(&cgroup2.Resources{Memory: &cgroup2.Memory{Max: &t.max}}); err != nil {
		_ = m.Delete()
		return nil, err
	}
	// Without swap.max = 0 a child over memory.max swaps instead of being
	// OOM-killed. The file is absent only without swap accounting.
	err = os.WriteFile(filepath.Join(cgroupMount, group, "memory.swap.max"), []byte("0"), 0)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		_ = m.Delete()
		return nil, err
	}
	return &instanceCgroup{m: m}, nil
}

func (g *instanceCgroup) add(pid int) error {
	return g.m.AddProc(uint64(pid))
}

func (g *instanceCgroup) remove() {
	_ = g.m.Delete()
}

func (g *instanceCgroup) inspect() InspectResult {
	st, err := g.m.StatFiltered(cgroup2.StatMemory)
	if err != nil || st.Memory == nil {
		return InspectResult{}
	}
	// UsageLimit is memory.max ("max" reads as math.MaxUint64, reported
	// as 0 = unlimited), Usage is memory.current, MaxUsage is memory.peak
	// (0 on kernels before 5.19).
	limit := st.Memory.UsageLimit
	if limit == math.MaxUint64 {
		limit = 0
	}
	return InspectResult{MemoryMax: limit, MemoryUsage: st.Memory.Usage, MemoryPeak: st.Memory.MaxUsage}
}
