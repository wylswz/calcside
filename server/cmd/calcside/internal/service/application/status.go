package application

import (
	"context"
	"errors"
	"sync/atomic"

	"calcside/cmd/calcside/internal/service"
	"calcside/internal/store"
	"calcside/internal/types"
)

// poolReplicas classifies the instance's live replicas into ready and
// still-preparing sets under the full fence: enabled pin, exact
// workspace/instance/release/generation, unexpired lease, an allocated
// epoch within the high-water mark, and a live node heartbeat under the
// same boot + incarnation. ActiveReplicaID and LastEpoch equality are
// never consulted — the pool, not a pointer, is the authority. The full
// fence is re-checked on every candidate, including rows a fake store
// hands back unfiltered.
func (s *Service) poolReplicas(ctx context.Context, app *store.Application) (ready, preparing []*store.ServingReplica, pin *store.PersistentInstance, err error) {
	if app.PersistentInstanceID == "" {
		return nil, nil, nil, service.Errf(types.ErrCodeNotRunning, "application not published")
	}
	pin, err = s.o.Store.GetPersistentInstance(ctx, app.WorkspaceID, app.PersistentInstanceID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil, nil, service.Errf(types.ErrCodeNotRunning, "application not running")
		}
		return nil, nil, nil, fail(err)
	}
	now := s.o.Now()
	if !pin.Enabled {
		return nil, nil, pin, service.Errf(types.ErrCodeNotRunning, "application not running")
	}
	rows, err := s.o.Store.ListInstanceServingReplicas(ctx, pin.ID, now)
	if err != nil {
		return nil, nil, pin, fail(err)
	}
	nodes, err := s.o.Store.ListServingNodes(ctx, now)
	if err != nil {
		return nil, nil, pin, fail(err)
	}
	live := make(map[string]*store.ServingNode, len(nodes))
	for _, n := range nodes {
		live[n.NodeID] = n
	}
	for _, rep := range rows {
		if rep == nil || rep.WorkspaceID != pin.WorkspaceID || rep.InstanceID != pin.ID ||
			rep.ReleaseID != pin.ReleaseID || rep.Generation != pin.Generation ||
			rep.Epoch <= 0 || rep.Epoch > pin.LastEpoch || !rep.LeaseUntil.After(now) {
			continue
		}
		n, ok := live[rep.NodeID]
		if !ok || n.BootID != rep.BootID || n.Incarnation != rep.NodeIncarnation ||
			!n.HeartbeatUntil.After(now) {
			continue
		}
		switch rep.State {
		case store.ServingReplicaReady:
			ready = append(ready, rep)
		case store.ServingReplicaPreparing:
			preparing = append(preparing, rep)
		}
	}
	if len(ready) == 0 && len(preparing) == 0 {
		return nil, nil, pin, service.Errf(types.ErrCodeNotRunning, "application not running")
	}
	return ready, preparing, pin, nil
}

// readyReplicas returns the pool's ready members only — invoke
// eligibility requires READY, never merely in-flight.
func (s *Service) readyReplicas(ctx context.Context, app *store.Application) ([]*store.ServingReplica, *store.PersistentInstance, error) {
	ready, _, pin, err := s.poolReplicas(ctx, app)
	if err != nil {
		return nil, pin, err
	}
	if len(ready) == 0 {
		return nil, pin, service.Errf(types.ErrCodeNotRunning, "application not running")
	}
	return ready, pin, nil
}

// readyReplica picks one replica from the ready pool, round-robin —
// each invoke lands on exactly one worker; there are no retries. The
// counter is per pin so interleaved applications never alias.
func (s *Service) readyReplica(ctx context.Context, app *store.Application) (*store.ServingReplica, *store.PersistentInstance, error) {
	reps, pin, err := s.readyReplicas(ctx, app)
	if err != nil {
		return nil, pin, err
	}
	v, _ := s.rr.LoadOrStore(pin.ID, new(atomic.Uint64))
	return reps[v.(*atomic.Uint64).Add(1)%uint64(len(reps))], pin, nil
}

// status computes the view's lifecycle word from the actual pin and
// replica state — never optimistic.
func (s *Service) status(ctx context.Context, app *store.Application) (status, releaseID, errCode string, changed bool) {
	changed = s.dirty(ctx, app)
	if app.PersistentInstanceID == "" {
		return "draft", "", "", changed
	}
	pin, err := s.o.Store.GetPersistentInstance(ctx, app.WorkspaceID, app.PersistentInstanceID)
	if err != nil {
		return "degraded", "", "deployment_unavailable", changed
	}
	if !pin.Enabled {
		return "disabled", pin.ReleaseID, "", changed
	}
	// Pool classification drives status without consuming the invoke
	// round-robin: any ready replica is ready; a still-preparing
	// sibling means recovery is in flight, which beats the stale
	// latest-epoch failure diagnostic below.
	ready, preparing, _, err := s.poolReplicas(ctx, app)
	switch {
	case err == nil && len(ready) > 0:
		return "ready", pin.ReleaseID, "", changed
	case err == nil && len(preparing) > 0:
		return "preparing", pin.ReleaseID, "", changed
	}
	rep, repErr := s.o.Store.GetCurrentServingReplica(ctx, pin.ID)
	if repErr == nil && rep != nil && rep.State == store.ServingReplicaFailed &&
		rep.WorkspaceID == pin.WorkspaceID && rep.InstanceID == pin.ID &&
		rep.ReleaseID == pin.ReleaseID && rep.Generation == pin.Generation {
		return "degraded", pin.ReleaseID, rep.ErrorCode, changed
	}
	return "preparing", pin.ReleaseID, "", changed
}

func (s *Service) view(ctx context.Context, app *store.Application) *View {
	status, rel, code, changed := s.status(ctx, app)
	return &View{Application: app, Status: status, ReleaseID: rel,
		ErrorCode: code, HasUnpublishedChanges: changed}
}

// dirty reports whether the draft's current descriptor candidate
// digest differs from the published pointer — the real dirty signal,
// not just the draft counter (edits that cancel out, and post-publish
// dependency drift, are all captured). Errors conservatively report
// dirty rather than silently clean. Never decrypts.
func (s *Service) dirty(ctx context.Context, app *store.Application) bool {
	if app.PersistentInstanceID == "" {
		return true // never published
	}
	pin, err := s.o.Store.GetPersistentInstance(ctx, app.WorkspaceID, app.PersistentInstanceID)
	if err != nil || pin == nil {
		return true
	}
	b, err := s.o.Sandbox.BuildBoundary(ctx, service.Actor{UserID: app.UserID, WorkspaceID: app.WorkspaceID}, app.Spec, false)
	if err != nil {
		return true
	}
	rel, err := newRelease(app, b, s.o.Now())
	if err != nil {
		return true
	}
	return rel.ID != pin.ReleaseID
}
