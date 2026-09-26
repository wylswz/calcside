package sandbox

import (
	"context"
	"errors"
	"io"
	"strings"

	"calcside/internal/capability"
	capfs "calcside/internal/capability/fs"
	"calcside/internal/instance"
	"calcside/internal/service"
	"calcside/internal/types"
)

// FileView is either a directory listing (IsDir + Entries) or a file
// (Path + redacted Content).
type FileView struct {
	IsDir   bool
	Entries []capfs.Entry
	Path    string
	Content string
}

// Browse serves GET /instances/{id}/files?path=... through the fs
// capability's typed accessor inside a gated "console" session.
func (s *Service) Browse(ctx context.Context, a service.Actor, id, path string) (*FileView, error) {
	in, err := s.owned(ctx, a, id)
	if err != nil {
		return nil, err
	}
	if in.Status != types.InstanceRunning {
		return nil, service.Errf(types.ErrCodeNotRunning, "instance not running")
	}
	if !s.mgr.HasCapability(in.ID, string(types.CapFS)) {
		return nil, service.Errf(types.ErrCodeNoFS, "instance has no fs capability")
	}
	var stat capfs.Entry
	var listing []capfs.Entry
	var content string
	err = s.mgr.WithConsole(in.ID, types.CapFS, func(gate *capability.Gate, c io.Closer) error {
		fc, ok := c.(*capfs.Closer)
		if !ok {
			return errors.New("bad fs binding")
		}
		acc := capfs.NewAccessor(fc.V, gate)
		stat, err = acc.Stat(ctx, path)
		if err != nil {
			return err
		}
		if stat.IsDir {
			listing, err = acc.List(ctx, path)
			return err
		}
		content, err = acc.Read(ctx, path)
		return err
	})
	if err != nil {
		switch {
		case errors.Is(err, instance.ErrNotFound):
			return nil, service.NotFound("instance not found")
		case errors.Is(err, instance.ErrNotRunning):
			return nil, service.Errf(types.ErrCodeNotRunning, "instance not running")
		case strings.Contains(err.Error(), "does not exist"):
			return nil, service.NotFound(err.Error())
		default:
			return nil, service.Errf(types.ErrCodeFSError, "%s", err.Error())
		}
	}
	if stat.IsDir {
		if listing == nil {
			listing = []capfs.Entry{}
		}
		return &FileView{IsDir: true, Entries: listing}, nil
	}
	return &FileView{Path: stat.Path, Content: s.mgr.Redact(in.ID, content)}, nil
}
