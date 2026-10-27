package application

import (
	"context"
	"errors"

	"calcside/cmd/calcside/internal/service"
	"calcside/internal/store"
	"calcside/internal/types"
)

// readyReplica enforces the full fence: enabled pin, the ACTIVE replica
// (not just latest epoch), exact instance/release/generation, unexpired
// lease, ready state, and a live node heartbeat under the same boot +
// incarnation. Anything short returns not_running — never a fake ready.
func (s *Service) readyReplica(ctx context.Context, app *store.Application) (*store.ServingReplica, *store.PersistentInstance, error) {
	if app.PersistentInstanceID == "" {
		return nil, nil, service.Errf(types.ErrCodeNotRunning, "application not published")
	}
	pin, err := s.o.Store.GetPersistentInstance(ctx, app.WorkspaceID, app.PersistentInstanceID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil, service.Errf(types.ErrCodeNotRunning, "application not running")
		}
		return nil, nil, fail(err)
	}
	if !pin.Enabled || pin.ActiveReplicaID == "" {
		return nil, nil, service.Errf(types.ErrCodeNotRunning, "application not running")
	}
	rep, err := s.o.Store.GetServingReplica(ctx, pin.ActiveReplicaID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, pin, service.Errf(types.ErrCodeNotRunning, "application not running")
		}
		return nil, pin, fail(err)
	}
	now := s.o.Now()
	ok := rep != nil &&
		rep.ID == pin.ActiveReplicaID &&
		rep.InstanceID == pin.ID &&
		rep.WorkspaceID == pin.WorkspaceID &&
		rep.ReleaseID == pin.ReleaseID &&
		rep.Generation == pin.Generation &&
		rep.Epoch == pin.LastEpoch &&
		rep.State == store.ServingReplicaReady &&
		rep.LeaseUntil.After(now)
	if !ok {
		return nil, pin, service.Errf(types.ErrCodeNotRunning, "application not running")
	}
	// The replica's node must be alive under the same boot and
	// incarnation — a stale boot's lease doesn't count.
	nodes, err := s.o.Store.ListServingNodes(ctx, now)
	if err != nil {
		return nil, pin, fail(err)
	}
	live := false
	for _, n := range nodes {
		if n.NodeID == rep.NodeID && n.BootID == rep.BootID &&
			n.Incarnation == rep.NodeIncarnation && n.HeartbeatUntil.After(now) {
			live = true
			break
		}
	}
	if !live {
		return nil, pin, service.Errf(types.ErrCodeNotRunning, "application not running")
	}
	return rep, pin, nil
}

// status computes the view's lifecycle word from the actual pin and
// replica state — never optimistic.
func (s *Service) status(ctx context.Context, app *store.Application) (status, releaseID, errCode string, changed bool) {
	changed = app.Version > app.PublishedVersion
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
	if _, _, err := s.readyReplica(ctx, app); err == nil {
		return "ready", pin.ReleaseID, "", changed
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
