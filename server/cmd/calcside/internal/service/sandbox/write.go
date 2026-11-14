package sandbox

import (
	"context"

	"calcside/cmd/calcside/internal/service"
	"calcside/cmd/calcside/internal/service/access"
	"calcside/internal/runtime"
	"calcside/internal/types"
)

// WriteView is the upload receipt returned to the API layer: the
// normalized path, size, mtime and whether an existing file was
// replaced. Uploaded contents are never echoed back.
type WriteView struct {
	Path     string
	Size     int64
	Mtime    int64
	Replaced bool
}

// WriteFile serves PUT /instances/{id}/files?path=...&overwrite=...
//
// The bytes were already bounded by the transport; the write itself
// happens on the node inside a gated console session, so it is
// policy-checked and audited exactly like a scripted fs.write. A
// receipt-style response carries the audit batch and a coded error
// together — both are persisted/reported, never either/or.
func (s *Service) WriteFile(ctx context.Context, a service.Actor, id, path string, content []byte, overwrite bool) (*WriteView, error) {
	in, err := s.running(ctx, a, id, access.Run)
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > runtime.MaxUploadBytes {
		return nil, service.Errf(types.ErrCodeTooLarge, "file exceeds %d bytes", runtime.MaxUploadBytes)
	}
	now := s.now().UTC()
	deadline := renewal(in, now)
	resp, err := s.rt.Write(ctx, &runtime.WriteRequest{
		InstanceID:       in.ID,
		Owner:            runtime.Owner{UserID: in.UserID},
		Actor:            actorOwner(a),
		Path:             path,
		Overwrite:        overwrite,
		Content:          content,
		RenewedExpiresAt: deadline,
		Epoch:            in.LeaseEpoch,
	})
	// A write that reached the instance always carries its audit batch
	// — a denied upload is exactly what the log is for — even when the
	// receipt also reports an error.
	if resp != nil {
		s.audit.Record(resp.Audit)
	}
	if err != nil {
		return nil, fail(err)
	}
	if resp.Error != nil {
		return nil, service.Errf(resp.Error.Code, "%s", resp.Error.Message)
	}
	in.LastActiveAt = now
	in.ExpiresAt = deadline
	_ = s.st.UpdateInstance(ctx, in)
	return &WriteView{Path: resp.Path, Size: resp.Size, Mtime: resp.Mtime, Replaced: resp.Replaced}, nil
}
