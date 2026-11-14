package instance

import (
	"context"
	"errors"
	"time"

	"calcside/internal/capability"
	capfs "calcside/internal/capability/fs"
	"calcside/internal/runtime"
	"calcside/internal/types"
)

// writeError stamps a stable API code on the write receipt; the codes
// are already the vocabulary the API tier renders, same as artifact
// export errors.
func writeError(code types.APIErrorCode, message string) *runtime.ArtifactError {
	return &runtime.ArtifactError{Code: code, Message: message}
}

// Write commits one uploaded file into the instance VFS through the fs
// capability's typed accessor, inside a gated console session — an
// upload is policy-checked and audited exactly like a scripted
// fs.write, and file contents never reach Call/Result metadata.
//
// The response is the receipt: once the request reaches the instance
// it is non-nil and carries the audit batch and an optional error on
// the 200 path, so a denied or failed write is still persisted. It is
// nil only when no instance ever saw the request. Committed
// distinguishes a write that landed before an after-hook denial from
// one that never committed.
//
// Lock order matches Export: requestMu (bounded wait) -> ExecMu ->
// artifactMu, with liveness/epoch re-checked inside, so an upload
// cannot race an exec, an artifact snapshot, or teardown.
func (m *Manager) Write(ctx context.Context, req *runtime.WriteRequest) (*runtime.WriteResponse, error) {
	in, err := m.live(req.InstanceID, req.Owner, req.Epoch)
	if err != nil {
		return nil, err
	}
	resp := &runtime.WriteResponse{}
	if int64(len(req.Content)) > runtime.MaxUploadBytes {
		resp.Error = writeError(types.ErrCodeTooLarge, "file exceeds upload limit")
		return resp, nil
	}
	full, err := capfs.Resolve(req.Path)
	if err != nil || full == capfs.Root {
		resp.Error = writeError(types.ErrCodeBadRequest, "invalid destination path")
		return resp, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := in.lockRequest(ctx); err != nil {
		return nil, err
	}
	defer in.requestMu.Unlock()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for !in.sess.ExecMu.TryLock() {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-tick.C:
		}
	}
	defer in.sess.ExecMu.Unlock()
	in.artifactMu.Lock()
	defer in.artifactMu.Unlock()
	if _, err = m.live(req.InstanceID, req.Owner, req.Epoch); err != nil {
		return nil, err
	}
	closer, ok := in.sess.Closers[types.CapFS].(*capfs.Closer)
	if !ok {
		resp.Error = writeError(types.ErrCodeNoFS, "instance has no fs capability")
		return resp, nil
	}
	in.sess.SetActor(req.Actor.UserID, req.Actor.Email)
	in.sess.Gate.Arm(capability.ExecContext{
		ExecID: "console", InstanceID: in.id,
		UserID: in.sess.UserID, UserEmail: in.sess.UserEmail, Labels: in.labels,
	})
	defer in.sess.Gate.Disarm()
	res, opErr := capfs.NewAccessor(closer.V, in.sess.Gate).Write(ctx, req.Path, req.Content, req.Overwrite)
	resp.Audit = in.sink.drain()
	resp.Committed = res.Committed
	if opErr != nil {
		var denied *capability.DeniedError
		switch {
		case errors.As(opErr, &denied):
			resp.Error = writeError(types.ErrCodeForbidden, "file upload denied by policy")
		case errors.Is(opErr, capfs.ErrExist):
			resp.Error = writeError(types.ErrCodeConflict, "file already exists; pass overwrite to replace it")
		case errors.Is(opErr, capfs.ErrQuota), errors.Is(opErr, capfs.ErrMaxFiles):
			resp.Error = writeError(types.ErrCodeTooLarge, "instance filesystem quota exceeded")
		case errors.Is(opErr, capfs.ErrReadOnly):
			resp.Error = writeError(types.ErrCodeConflict, "instance filesystem is read-only")
		case errors.Is(opErr, capfs.ErrIsDir), errors.Is(opErr, capfs.ErrNotDir):
			resp.Error = writeError(types.ErrCodeBadRequest, "destination path is not a writable file")
		case errors.Is(opErr, capability.ErrOutOfScope):
			resp.Error = writeError(types.ErrCodeNotRunning, "instance is no longer running")
		case errors.Is(opErr, context.Canceled), errors.Is(opErr, context.DeadlineExceeded):
			resp.Error = writeError(types.ErrCodeBadRequest, "file upload canceled or deadline exceeded")
		default:
			resp.Error = writeError(types.ErrCodeFSError, "file upload failed")
		}
		return resp, nil
	}
	in.renew(req.RenewedExpiresAt)
	resp.Path, resp.Size, resp.Mtime, resp.Replaced = res.Entry.Path, res.Entry.Size, res.Entry.Mtime, res.Replaced
	return resp, nil
}
