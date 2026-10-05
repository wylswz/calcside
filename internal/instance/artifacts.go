package instance

import (
	"context"
	"errors"
	"path"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"calcside/internal/capability"
	capfs "calcside/internal/capability/fs"
	"calcside/internal/runtime"
	"calcside/internal/types"
)

func artifactError(code types.APIErrorCode, message string) *runtime.ArtifactError {
	return &runtime.ArtifactError{Code: code, Message: message}
}

func (m *Manager) Export(ctx context.Context, req *runtime.ExportRequest) (*runtime.ExportResponse, error) {
	if req.Owner.UserID == "" {
		return nil, runtime.ErrNotOwner
	}
	in, err := m.live(req.InstanceID, req.Owner, req.Epoch)
	if err != nil {
		return nil, err
	}
	if req.CheckOnly {
		return &runtime.ExportResponse{}, ctx.Err()
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	resp := &runtime.ExportResponse{}
	closer, ok := in.sess.Closers[types.CapFS].(*capfs.Closer)
	if !ok {
		resp.Error = artifactError(types.ErrCodeNoFS, "instance has no fs capability")
		return resp, nil
	}
	in.sess.Gate.Arm(capability.ExecContext{ExecID: "artifact", InstanceID: in.id, UserID: in.owner.UserID, UserEmail: in.owner.Email, Labels: in.labels})
	defer in.sess.Gate.Disarm()
	files, err := collectArtifacts(ctx, in, capfs.NewAccessor(closer.V, in.sess.Gate), req)
	if err == nil && !in.sess.Gate.Armed() {
		err = artifactError(types.ErrCodeNotRunning, "instance is no longer running")
	}
	resp.Audit = in.sink.drain()
	if err != nil {
		var known *runtime.ArtifactError
		var denied *capability.DeniedError
		switch {
		case errors.As(err, &known):
			resp.Error = known
		case errors.As(err, &denied):
			resp.Error = artifactError(types.ErrCodeForbidden, "artifact access denied by policy")
		case errors.Is(err, capfs.ErrNotExist):
			resp.Error = artifactError(types.ErrCodeNotFound, "artifact path not found")
		case errors.Is(err, capfs.ErrListingLimit):
			resp.Error = artifactError(types.ErrCodeTooLarge, "artifact directory exceeds entry limit")
		case errors.Is(err, capability.ErrOutOfScope):
			resp.Error = artifactError(types.ErrCodeNotRunning, "instance is no longer running")
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			resp.Error = artifactError(types.ErrCodeBadRequest, "artifact operation canceled or deadline exceeded")
		default:
			resp.Error = artifactError(types.ErrCodeFSError, "artifact file operation failed")
		}
		return resp, nil
	}
	resp.Files = files
	return resp, nil
}

func collectArtifacts(ctx context.Context, in *inst, acc *capfs.Accessor, req *runtime.ExportRequest) ([]runtime.ArtifactFile, error) {
	if len(req.Paths) == 0 || len(req.Paths) > runtime.MaxArtifactFiles || !req.Recursive && len(req.Paths) != 1 {
		return nil, artifactError(types.ErrCodeBadRequest, "invalid artifact selection")
	}
	queued := map[string]bool{}
	portableNames := map[string]string{}
	var pending []string
	enqueue := func(p string) error {
		if len(p) == 0 || len(p) > runtime.MaxArtifactPathBytes || !utf8.ValidString(p) || strings.ContainsAny(p, "\\:") || strings.IndexFunc(p, unicode.IsControl) >= 0 {
			return artifactError(types.ErrCodeBadRequest, "invalid artifact path")
		}
		full, err := capfs.Resolve(p)
		if err != nil {
			return artifactError(types.ErrCodeBadRequest, "artifact path must stay inside /work")
		}
		if len(full) > runtime.MaxArtifactPathBytes {
			return artifactError(types.ErrCodeTooLarge, "artifact path exceeds limit")
		}
		if err := capfs.CheckExportPath(full, portableNames); err != nil {
			return artifactError(types.ErrCodeBadRequest, err.Error())
		}
		redacted, err := in.secrets.RedactBounded(ctx, full, runtime.MaxArtifactPathBytes)
		if err != nil || redacted != full {
			return artifactError(types.ErrCodeBadRequest, "artifact path cannot be exported")
		}
		if queued[full] {
			return nil
		}
		if len(pending) >= runtime.MaxArtifactEntries {
			return artifactError(types.ErrCodeTooLarge, "artifact selection exceeds entry limit")
		}
		queued[full] = true
		pending = append(pending, full)
		return nil
	}
	for _, p := range req.Paths {
		if err := enqueue(p); err != nil {
			return nil, err
		}
	}
	files := []runtime.ArtifactFile{}
	total, sourceTotal, count := 0, 0, 0
	for i := 0; i < len(pending); i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		p := pending[i]
		stat, err := acc.Stat(ctx, p)
		if err != nil {
			return nil, err
		}
		if stat.IsDir {
			if !req.Recursive {
				return nil, artifactError(types.ErrCodeBadRequest, "select a file for this operation")
			}
			entries, err := acc.ListBounded(ctx, p, runtime.MaxArtifactEntries)
			if err != nil {
				return nil, err
			}
			for _, entry := range entries {
				if err := enqueue(entry.Path); err != nil {
					return nil, err
				}
			}
			if p != "/work" {
				files = append(files, runtime.ArtifactFile{Path: p, IsDir: true})
			}
			continue
		}
		count++
		if count > runtime.MaxArtifactFiles || stat.Size > runtime.MaxArtifactFileBytes || stat.Size > int64(runtime.MaxArtifactTotalBytes-sourceTotal) {
			return nil, artifactError(types.ErrCodeTooLarge, "artifact file count or byte limit exceeded")
		}
		sourceTotal += int(stat.Size)
		switch strings.ToLower(path.Ext(p)) {
		case "", ".txt", ".log", ".csv", ".tsv", ".html", ".htm", ".json", ".md", ".markdown", ".css", ".js", ".mjs", ".yaml", ".yml", ".xml", ".sql":
		default:
			return nil, artifactError(types.ErrCodeBadRequest, "unsupported artifact file type; only UTF-8 text artifacts can be exported")
		}
		content, err := acc.Read(ctx, p)
		if err != nil {
			return nil, err
		}
		if !utf8.ValidString(content) || strings.IndexByte(content, 0) >= 0 {
			return nil, artifactError(types.ErrCodeBadRequest, "artifact is not NUL-free UTF-8 text")
		}
		redacted, err := in.secrets.RedactBounded(ctx, content, runtime.MaxArtifactFileBytes)
		if err != nil {
			return nil, artifactError(types.ErrCodeTooLarge, "redacted artifact exceeds limit")
		}
		if !utf8.ValidString(redacted) || strings.IndexByte(redacted, 0) >= 0 {
			return nil, artifactError(types.ErrCodeBadRequest, "redacted artifact is not NUL-free UTF-8 text")
		}
		total += len(redacted)
		if total > runtime.MaxArtifactTotalBytes {
			return nil, artifactError(types.ErrCodeTooLarge, "artifact selection exceeds byte limit")
		}
		files = append(files, runtime.ArtifactFile{Path: p, Content: []byte(redacted), Redacted: redacted != content})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}
