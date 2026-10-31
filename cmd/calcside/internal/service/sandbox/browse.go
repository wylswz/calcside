package sandbox

import (
	"context"
	"strings"
	"unicode/utf8"

	"calcside/cmd/calcside/internal/service"
	"calcside/cmd/calcside/internal/service/access"
	"calcside/internal/runtime"
	"calcside/internal/types"
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
func (s *Service) Browse(ctx context.Context, a service.Actor, id, path string, listOnly bool) (*FileView, error) {
	in, err := s.running(ctx, a, id, access.View)
	if err != nil {
		return nil, err
	}
	resp, err := s.rt.Browse(ctx, &runtime.BrowseRequest{
		InstanceID: in.ID, Owner: runtime.Owner{UserID: in.UserID}, Actor: actorOwner(a), Path: path, Epoch: in.LeaseEpoch, ListOnly: listOnly,
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
	// Text browsing is UTF-8 only: JSON would silently corrupt binary
	// content with replacement characters. Uploads may be binary, so a
	// non-UTF-8 (or NUL-bearing) file fails explicitly — the listing and
	// artifact metadata still work, the download surface stays text.
	if !utf8.ValidString(resp.Content) || strings.IndexByte(resp.Content, 0) >= 0 {
		return nil, service.Errf(types.ErrCodeBadRequest, "file is not UTF-8 text; binary files cannot be previewed")
	}
	return &FileView{Path: resp.Path, Content: resp.Content}, nil
}
