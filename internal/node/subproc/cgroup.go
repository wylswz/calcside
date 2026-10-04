package subproc

type InspectResult struct {
	MemoryMax   uint64
	MemoryUsage uint64
	MemoryPeak  uint64
}

type iInstanceGroup interface {
	inspect() InspectResult
	add(pid int) error
	remove()
}

type iCgroupTree interface {
	create(name string) (iInstanceGroup, error)
}

// noCgroup is the group of an instance that runs without a memory cap.
type noCgroup struct{}

func (noCgroup) add(int) error { return nil }

func (noCgroup) remove() {}

func (noCgroup) inspect() InspectResult { return InspectResult{} }
