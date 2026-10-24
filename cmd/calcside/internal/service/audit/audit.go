// Package audit serves the audit-log query endpoint. The actor's user
// ID is forced into the store filter so tenant isolation cannot be
// forgotten by a caller.
package audit

import (
	"context"
	"time"

	"calcside/cmd/calcside/internal/service"
	"calcside/cmd/calcside/internal/service/access"
	"calcside/internal/store"
)

type Service struct {
	st     store.Store
	access *access.Checker
}

func New(st store.Store, ac *access.Checker) *Service {
	return &Service{st: st, access: ac}
}

// Filter mirrors the query params of the audit endpoint.
type Filter struct {
	InstanceID string
	ExecID     string
	Limit      int
	Before     *time.Time
}

func (s *Service) List(ctx context.Context, a service.Actor, f Filter) ([]*store.AuditEvent, error) {
	if a.Restricted() {
		// A restricted key must scope its query to an instance it owns.
		id := f.InstanceID
		if id == "" {
			if f.ExecID == "" {
				return nil, service.Forbidden("restricted keys must pass instance_id or exec_id")
			}
			ex, err := s.st.GetExecution(ctx, f.ExecID)
			if err != nil {
				return nil, service.NotFound("execution not found")
			}
			id = ex.InstanceID
		}
		if _, err := s.access.Instance(ctx, a, id, access.View); err != nil {
			return nil, err
		}
		f.InstanceID = id
	}
	lst, err := s.st.ListAuditEvents(ctx, store.AuditFilter{
		WorkspaceID: a.WorkspaceID,
		InstanceID:  f.InstanceID,
		ExecID:      f.ExecID,
		Limit:       f.Limit,
		Before:      f.Before,
	})
	if err != nil {
		return nil, service.Internal(err)
	}
	if lst == nil {
		lst = []*store.AuditEvent{}
	}
	return lst, nil
}
