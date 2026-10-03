//go:build !linux

package subproc

import "errors"

type cgroupTree struct{}

type instanceCgroup struct{}

func newCgroupTree(string, int64) (*cgroupTree, error) {
	return nil, errors.New("per-instance memory limits need Linux cgroup v2")
}

func (*cgroupTree) create(string) (*instanceCgroup, error) { return nil, nil }

func (*instanceCgroup) add(int) error { return nil }

func (*instanceCgroup) remove() {}
