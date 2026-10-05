package sandbox

import (
	"context"
	"time"

	"calcside/internal/runtime"
	"calcside/internal/service"
	"calcside/internal/types"
)

type ArtifactSnapshot struct {
	PreviewHTML []byte
	Files       []runtime.ArtifactFile
	ExpiresAt   time.Time
	Epoch       int64
}

func (s *Service) Export(ctx context.Context, a service.Actor, id string, paths []string, recursive bool) (*ArtifactSnapshot, error) {
	return s.export(ctx, a, id, runtime.ExportRequest{Paths: paths, Recursive: recursive})
}

func (s *Service) Preview(ctx context.Context, a service.Actor, id, path, mode string) (*ArtifactSnapshot, error) {
	if mode == "source" {
		mode = ""
	}
	return s.export(ctx, a, id, runtime.ExportRequest{Paths: []string{path}, PreviewMode: mode})
}

func (s *Service) export(ctx context.Context, a service.Actor, id string, req runtime.ExportRequest) (*ArtifactSnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	in, err := s.running(ctx, a, id)
	if err != nil {
		return nil, err
	}
	if !in.ExpiresAt.After(s.now()) {
		return nil, service.Errf(types.ErrCodeNotRunning, "instance expired")
	}
	req.InstanceID, req.Owner, req.Epoch = id, owner(a), in.LeaseEpoch
	resp, err := s.rt.Export(ctx, &req)
	if resp != nil {
		s.audit.Record(resp.Audit)
	}
	if err != nil {
		return nil, fail(err)
	}
	if resp.Error != nil {
		return nil, service.Errf(resp.Error.Code, "%s", resp.Error.Message)
	}
	if err := s.ArtifactAlive(ctx, a.UserID, id, in.LeaseEpoch); err != nil {
		return nil, err
	}
	return &ArtifactSnapshot{PreviewHTML: resp.PreviewHTML, Files: resp.Files, ExpiresAt: in.ExpiresAt, Epoch: in.LeaseEpoch}, nil
}

func (s *Service) ArtifactAlive(ctx context.Context, userID, id string, epoch int64) error {
	in, err := s.running(ctx, service.Actor{UserID: userID}, id)
	if err != nil {
		return err
	}
	if !in.ExpiresAt.After(s.now()) || in.LeaseEpoch != epoch {
		return service.Errf(types.ErrCodeNotRunning, "instance expired or execution state changed")
	}
	_, err = s.rt.Export(ctx, &runtime.ExportRequest{InstanceID: id, Owner: runtime.Owner{UserID: userID}, Epoch: epoch, CheckOnly: true})
	if err != nil {
		return fail(err)
	}
	return nil
}
