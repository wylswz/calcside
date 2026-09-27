package sandbox

import (
	"context"

	"calcside/internal/runtime"
	"calcside/internal/service"
)

// Inspect serves GET /instances/{id}/inspect.
//
// The snapshot is taken on the node under the session's exec lock, so
// a console read cannot observe the globals dict mid-mutation; values
// come back as reprs with the instance's secrets already scrubbed.
func (s *Service) Inspect(ctx context.Context, a service.Actor, id string) (map[string]string, error) {
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
	if resp.Variables == nil {
		return map[string]string{}, nil
	}
	return resp.Variables, nil
}
