// Package audit serves the audit-log query endpoint. The actor's user
// ID is forced into the store filter so tenant isolation cannot be
// forgotten by a caller.
package audit

import (
	"context"
	"time"

	"calcside/cmd/calcside/internal/service"
	"calcside/internal/store"
)

type Service struct {
	st store.Store
}

func New(st store.Store) *Service {
	return &Service{st: st}
}

// Filter mirrors the query params of the audit endpoint.
type Filter struct {
	InstanceID string
	ExecID     string
	Limit      int
	Before     *time.Time
}

func (s *Service) List(ctx context.Context, a service.Actor, f Filter) ([]*store.AuditEvent, error) {
	lst, err := s.st.ListAuditEvents(ctx, store.AuditFilter{
		UserID:     a.UserID,
		InstanceID: f.InstanceID,
		ExecID:     f.ExecID,
		Limit:      f.Limit,
		Before:     f.Before,
	})
	if err != nil {
		return nil, service.Internal(err)
	}
	if lst == nil {
		lst = []*store.AuditEvent{}
	}
	return lst, nil
}
