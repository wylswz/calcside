package subproc

import (
	"context"
	"errors"
	"log/slog"

	"calcside/internal/runtime"
)

// serving.go forwards the EP-0014 serving ops to per-replica child
// processes. Each prepare spawns a dedicated child -- replicas never
// share a process with a dev instance or each other.

var _ runtime.ServingRuntime = (*Supervisor)(nil)

// Prepare spawns a dedicated child and prepares the replica in it.
// Any transport or result failure terminates the child; a known
// failure result still returns its audit. There is no retry. The
// hard deadline bounds the whole forward -- spawn, prepare and the
// queue wait alike.
func (s *Supervisor) Prepare(ctx context.Context, req *runtime.PrepareRequest) (*runtime.PrepareResponse, error) {
	hctx, cancel := ctx, context.CancelFunc(func() {})
	if s.o.HardExecTimeout > 0 {
		hctx, cancel = context.WithTimeout(ctx, s.o.HardExecTimeout)
	}
	defer cancel()

	if err := hctx.Err(); err != nil {
		return nil, err
	}
	select {
	case <-s.stop:
		return nil, runtime.ErrNotRunning
	default:
	}
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-hctx.Done():
		return nil, hctx.Err()
	}
	select {
	case <-s.stop:
		return nil, runtime.ErrNotRunning
	default:
	}

	s.mu.Lock()
	if _, ok := s.procs[req.InstanceID]; ok || s.starting[req.InstanceID] {
		s.mu.Unlock()
		return nil, runtime.Errf(runtime.ErrBadSpec, "replica id already in use")
	}
	if s.o.MaxInstances > 0 && len(s.procs)+len(s.starting) >= s.o.MaxInstances {
		s.mu.Unlock()
		return nil, runtime.ErrTooMany
	}
	s.starting[req.InstanceID] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.starting, req.InstanceID)
		s.mu.Unlock()
	}()

	p, err := s.spawn(hctx, req.InstanceID)
	if err != nil {
		return nil, err
	}
	resp, err := p.rt.Prepare(hctx, req)
	if err == nil && resp.Result.Error != nil {
		// Known engine failure: audit rides back on the 200
		// response, but the replica is not usable.
		s.terminate(p)
		return resp, nil
	}
	if err == nil {
		p.renew(req.ExpiresAt)
		// A Close racing the prepare must not let a child escape the
		// teardown snapshot; the same check bounds the hard ctx.
		s.mu.Lock()
		select {
		case <-s.stop:
			s.mu.Unlock()
			s.terminate(p)
			return nil, runtime.ErrNotRunning
		case <-hctx.Done():
			s.mu.Unlock()
			s.terminate(p)
			return nil, hctx.Err()
		default:
			s.procs[req.InstanceID] = p
			s.mu.Unlock()
		}
		if p.exited() {
			s.forget(p)
			return nil, errExited(req.InstanceID)
		}
		return resp, nil
	}
	s.terminate(p)
	if ctx.Err() == nil && errors.Is(hctx.Err(), context.DeadlineExceeded) {
		return nil, errExited(req.InstanceID)
	}
	return nil, err
}

// Invoke forwards a fenced call to the replica's child under the
// shared exec bound and hard timeout. No renewal, no respawn, no
// retry: a dead child reports not_running.
func (s *Supervisor) Invoke(ctx context.Context, req *runtime.InvokeRequest) (*runtime.InvokeResponse, error) {
	hctx, cancel := ctx, context.CancelFunc(func() {})
	if s.o.HardExecTimeout > 0 {
		hctx, cancel = context.WithTimeout(ctx, s.o.HardExecTimeout)
	}
	defer cancel()

	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-hctx.Done():
		return &runtime.InvokeResponse{Result: runtime.ProgramResult{
			ID:    req.InvokeID,
			Error: &runtime.ProgramError{Code: "timeout", Message: "execution timed out; the outcome may be unknown"},
		}}, nil
	}
	return call(s, req.InstanceID, func(p *proc) (*runtime.InvokeResponse, error) {
		resp, err := p.rt.Invoke(hctx, req)
		if err != nil && ctx.Err() == nil && errors.Is(hctx.Err(), context.DeadlineExceeded) {
			slog.Warn("replica process unresponsive; killing", "instance_id", req.InstanceID, "invoke_id", req.InvokeID)
			s.kill(p)
			return nil, errExited(req.InstanceID)
		}
		return resp, err
	})
}
