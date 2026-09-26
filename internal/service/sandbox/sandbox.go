// Package sandbox implements the instance domain: lifecycle, exec,
// execution records, the agent prompt, and file browsing. It is a thin
// wrapper over instance.Manager (the runtime state machine) that adds
// ownership checks and record persistence.
package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"calcside/internal/engine"
	"calcside/internal/instance"
	"calcside/internal/service"
	"calcside/internal/store"
	"calcside/internal/types"
)

// ExecView is the exec result as returned to the API (type alias so the
// api layer needn't import the engine package).
type ExecView = engine.Result

// MaxCodeBytes caps the exec request code size.
const MaxCodeBytes = 256 << 10

const snippetBytes = 2 << 10

type Service struct {
	st  store.Store
	mgr *instance.Manager
}

func New(st store.Store, mgr *instance.Manager) *Service {
	return &Service{st: st, mgr: mgr}
}

// owned fetches the instance enforcing ownership: other users'
// instances are not_found.
func (s *Service) owned(ctx context.Context, a service.Actor, id string) (*store.Instance, error) {
	in, err := s.mgr.Get(ctx, id)
	if err != nil || in.UserID != a.UserID {
		return nil, service.NotFound("instance not found")
	}
	return in, nil
}

func (s *Service) List(ctx context.Context, a service.Actor, status types.InstanceStatus) ([]*store.Instance, error) {
	lst, err := s.mgr.List(ctx, a.UserID, status)
	if err != nil {
		return nil, service.Internal(err)
	}
	if lst == nil {
		lst = []*store.Instance{}
	}
	return lst, nil
}

func (s *Service) Create(ctx context.Context, a service.Actor, rawSpec []byte) (*store.Instance, error) {
	u, err := s.st.GetUser(ctx, a.UserID)
	if err != nil {
		return nil, service.Internal(err)
	}
	meta, err := s.mgr.Create(ctx, u, rawSpec)
	if err != nil {
		switch {
		case errors.Is(err, instance.ErrTooMany):
			return nil, service.Errf(types.ErrCodeTooMany, "%s", err.Error())
		case errors.Is(err, instance.ErrCapabilityName):
			return nil, service.Errf(types.ErrCodeBadCapability, "%s", err.Error())
		default:
			return nil, service.Errf(types.ErrCodeBadSpec, "%s", err.Error())
		}
	}
	return meta, nil
}

func (s *Service) Get(ctx context.Context, a service.Actor, id string) (*store.Instance, error) {
	return s.owned(ctx, a, id)
}

func (s *Service) Delete(ctx context.Context, a service.Actor, id string) error {
	in, err := s.owned(ctx, a, id)
	if err != nil {
		return err
	}
	if err := s.mgr.Delete(ctx, in.ID); err != nil {
		if errors.Is(err, instance.ErrNotFound) {
			return service.NotFound("instance not found")
		}
		return service.Internal(err)
	}
	return nil
}

func (s *Service) Keepalive(ctx context.Context, a service.Actor, id string) (*store.Instance, error) {
	in, err := s.owned(ctx, a, id)
	if err != nil {
		return nil, err
	}
	meta, err := s.mgr.Keepalive(ctx, in.ID)
	if err != nil {
		if errors.Is(err, instance.ErrNotRunning) {
			return nil, service.Errf(types.ErrCodeNotRunning, "instance not running")
		}
		return nil, service.NotFound("instance not found")
	}
	return meta, nil
}

// Exec runs code on a live, owned instance and persists the execution
// record (sha256 + truncated snippet).
func (s *Service) Exec(ctx context.Context, a service.Actor, id, code string, timeout time.Duration) (*ExecView, error) {
	in, err := s.owned(ctx, a, id)
	if err != nil {
		return nil, err
	}
	if in.Status != types.InstanceRunning {
		return nil, service.Errf(types.ErrCodeNotRunning, "instance not running")
	}
	if len(code) > MaxCodeBytes {
		return nil, service.Errf(types.ErrCodeTooLarge, "code exceeds 256KB")
	}
	record := func(res *engine.Result, execID string) {
		sum := sha256.Sum256([]byte(code))
		snippet := code
		if len(snippet) > snippetBytes {
			snippet = snippet[:snippetBytes]
		}
		status := types.ExecOK
		var errType types.ExecErrorType
		if res.Error != nil {
			status = types.ExecError
			errType = res.Error.Type
		}
		_ = s.st.CreateExecution(ctx, &store.Execution{
			ID: execID, InstanceID: in.ID, UserID: a.UserID,
			CodeSHA256: hex.EncodeToString(sum[:]), CodeSnippet: snippet, Code: code,
			Status: status, ErrorType: errType, DurationMs: res.DurationMs,
			Steps: res.Steps, OutputBytes: int64(len(res.Output)),
		})
	}
	res, err := s.mgr.Exec(ctx, in.ID, code, timeout, record)
	if err != nil {
		switch {
		case errors.Is(err, instance.ErrNotFound):
			return nil, service.NotFound("instance not found")
		case errors.Is(err, instance.ErrNotRunning):
			return nil, service.Errf(types.ErrCodeNotRunning, "instance not running")
		default:
			return nil, service.BadRequest("%s", err.Error())
		}
	}
	return res, nil
}

func (s *Service) ListExecutions(ctx context.Context, a service.Actor, instanceID string, limit int) ([]*store.Execution, error) {
	in, err := s.owned(ctx, a, instanceID)
	if err != nil {
		return nil, err
	}
	lst, err := s.st.ListExecutions(ctx, in.ID, limit)
	if err != nil {
		return nil, service.Internal(err)
	}
	if lst == nil {
		lst = []*store.Execution{}
	}
	return lst, nil
}

// GetExecution returns one execution with its full code. Ownership is
// enforced via the execution's user_id: other users' execs are 404.
func (s *Service) GetExecution(ctx context.Context, a service.Actor, id string) (*store.Execution, error) {
	ex, err := s.st.GetExecution(ctx, id)
	if err != nil || ex.UserID != a.UserID {
		return nil, service.NotFound("execution not found")
	}
	return ex, nil
}
