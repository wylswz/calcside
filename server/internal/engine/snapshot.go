package engine

import "go.starlark.net/starlark"

type SnapshotterType string

const (
	LocalSnapshotter SnapshotterType = "local"
)

type LocalSnapshotterConfig struct {
	Dir string
}

type SnapshotterOptions struct {
	Type  SnapshotterType
	Local LocalSnapshotterConfig
}

type Snapshotter interface {
	TakeSnapshot(globals starlark.StringDict)
	IntoGlobals() starlark.StringDict
}

func NewSnapshotter(opts SnapshotterOptions) (Snapshotter, error) {
	// TODO: impl
	return nil, nil
}
