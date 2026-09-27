package sandbox

import (
	"context"

	"calcside/internal/runtime"
	"calcside/internal/service"
)

// FileView is either a directory listing (IsDir + Entries) or a file
// (Path + redacted Content).
type FileView struct {
	IsDir   bool
	Entries []runtime.FileEntry
	Path    string
	Content string
}

// Browse serves GET /instances/{id}/files?path=...
//
// The read happens on the node, inside a gated console session, so it
// is policy-checked and audited like a scripted read, and the content
// comes back with the instance's secrets already scrubbed.
func (s *Service) Browse(ctx context.Context, a service.Actor, id, path string) (*FileView, error) {
	in, err := s.running(ctx, a, id)
	if err != nil {
		return nil, err
	}
	resp, err := s.rt.Browse(ctx, &runtime.BrowseRequest{
		InstanceID: in.ID, Owner: owner(a), Path: path, Epoch: in.LeaseEpoch,
	})
	// A failed browse can still have produced audit events.
	if resp != nil {
		s.audit.Record(resp.Audit)
	}
	if err != nil {
		return nil, fail(err)
	}
	if resp.IsDir {
		entries := resp.Entries
		if entries == nil {
			entries = []runtime.FileEntry{}
		}
		return &FileView{IsDir: true, Entries: entries}, nil
	}
	return &FileView{Path: resp.Path, Content: resp.Content}, nil
}
