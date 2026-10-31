//go:build !linux

package subproc

import "errors"

func newCgroupTree(string, int64) (iCgroupTree, error) {
	return nil, errors.New("per-instance memory limits need Linux cgroup v2")
}
