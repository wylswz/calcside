package instance

import (
	"context"
	"errors"
	"io"
	"strings"

	"calcside/internal/capability"
	capfs "calcside/internal/capability/fs"
	"calcside/internal/runtime"
	"calcside/internal/types"
)

// Browse reads a path from the instance's VFS through the fs
// capability's typed accessor, inside a gated "console" session, so a
// console read is policy-checked and audited exactly like a scripted
// one.
//
// The response is non-nil even when the error is, because a failed
// browse may still have produced audit events and those are the API
// tier's to persist. It is nil only when the request never reached an
// instance at all.
func (m *Manager) Browse(ctx context.Context, req *runtime.BrowseRequest) (*runtime.BrowseResponse, error) {
	in, err := m.live(req.InstanceID, req.Owner, req.Epoch)
	if err != nil {
		return nil, err
	}
	if err := in.lockRequest(ctx); err != nil {
		return nil, err
	}
	defer in.requestMu.Unlock()
	var stat capfs.Entry
	var listing []capfs.Entry
	var content string
	opErr := m.withConsole(in, types.CapFS, func(gate *capability.Gate, c io.Closer) error {
		fc, ok := c.(*capfs.Closer)
		if !ok {
			return errors.New("bad fs binding")
		}
		acc := capfs.NewAccessor(fc.V, gate)
		var err error
		if stat, err = acc.Stat(ctx, req.Path); err != nil {
			return err
		}
		if stat.IsDir {
			listing, err = acc.List(ctx, req.Path)
			return err
		}
		if req.ListOnly {
			return errors.New("path is not a directory")
		}
		content, err = acc.Read(ctx, req.Path)
		return err
	})
	if errors.Is(opErr, runtime.ErrNoCapability) {
		return nil, opErr
	}
	resp := &runtime.BrowseResponse{Audit: in.sink.drain()}
	if opErr != nil {
		if strings.Contains(opErr.Error(), "does not exist") {
			return resp, runtime.Errf(runtime.ErrNoSuchPath, "%s", opErr)
		}
		return resp, runtime.Errf(runtime.ErrFS, "%s", opErr)
	}
	if stat.IsDir {
		resp.IsDir = true
		resp.Entries = make([]runtime.FileEntry, 0, len(listing))
		for _, e := range listing {
			if req.ListOnly && (in.secrets.Redact(e.Name) != e.Name || in.secrets.Redact(e.Path) != e.Path) {
				continue
			}
			resp.Entries = append(resp.Entries, runtime.FileEntry{
				Name: e.Name, Path: e.Path, IsDir: e.IsDir, Size: e.Size, Mtime: e.Mtime,
			})
		}
		return resp, nil
	}
	resp.Path = stat.Path
	resp.Content = in.secrets.Redact(content)
	return resp, nil
}
