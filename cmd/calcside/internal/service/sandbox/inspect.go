package sandbox

import (
	"context"

	"calcside/cmd/calcside/internal/service"
	"calcside/internal/completion"
	"calcside/internal/runtime"
)

// InspectView is a live instance snapshot: globals plus resource usage.
// Resources is nil when the instance has no cgroup of its own.
type InspectView struct {
	Variables map[string]string
	Resources *runtime.ResourceUsages
}

// Inspect serves GET /instances/{id}/inspect.
//
// The snapshot is taken on the node under the session's exec lock, so
// a console read cannot observe the globals dict mid-mutation; values
// come back as reprs with the instance's secrets already scrubbed.
func (s *Service) Inspect(ctx context.Context, a service.Actor, id string) (*InspectView, error) {
	in, err := s.running(ctx, a, id)
	if err != nil {
		return nil, err
	}
	resp, err := s.rt.Inspect(ctx, &runtime.InspectRequest{
		InstanceID: in.ID, Owner: owner(a), Epoch: in.LeaseEpoch,
	})
	if err != nil {
		return nil, fail(err)
	}
	v := &InspectView{Variables: resp.Variables}
	if v.Variables == nil {
		v.Variables = map[string]string{}
	}
	if ru := resp.ResourceUsages; ru != (runtime.ResourceUsages{}) {
		v.Resources = &ru
	}
	return v, nil
}

func (s *Service) Completions(ctx context.Context, a service.Actor, id string) (*completion.Context, error) {
	in, err := s.running(ctx, a, id)
	if err != nil {
		return nil, err
	}
	resp, err := s.rt.Inspect(ctx, &runtime.InspectRequest{
		InstanceID: in.ID, Owner: owner(a), Epoch: in.LeaseEpoch, CompletionsOnly: true,
	})
	if err != nil {
		return nil, fail(err)
	}
	if resp.Completions == nil {
		return &completion.Context{Symbols: []completion.Symbol{}, EnvKeys: []string{}}, nil
	}
	return resp.Completions, nil
}
