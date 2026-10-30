package run

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"calcside/cmd/calcside/internal/service"
	"calcside/cmd/calcside/internal/service/access"
	"calcside/internal/store"
	"calcside/internal/types"
)

// api.go: the caller-facing run/interrupt surface. Authorization is
// access.Checker decisions; sealed fields are opened for View callers
// and never exposed through list routes.

// approvalDefaultEventSchema constrains manual approval resolutions.
var approvalDefaultEventSchema = json.RawMessage(
	`{"type":"object","properties":{"approved":{"type":"boolean"},"note":{"type":"string"}},"required":["approved"]}`)

// InterruptView is one interrupt as the API renders it.
type InterruptView struct {
	Interrupt       *store.Interrupt
	ApplicationName string
	CanResolve      bool
	CallbackURL     string
	ContextJSON     string
	EventJSON       string
	ResolvedByLabel string
	AssigneeLabels  []string
}

// RunDetail bundles a run with its steps and interrupts.
type RunDetail struct {
	Run        *store.Run
	Steps      []*store.RunStep
	Interrupts []*InterruptView
	OutputJSON string
}

func (s *Service) appName(ctx context.Context, id string) string {
	a, err := s.o.Store.GetApplication(ctx, id)
	if err != nil {
		return ""
	}
	return a.Name
}

// canResolve applies the resolve authorization for one interrupt:
// approval → Run on the app + assignee membership (or no assignees);
// callback → Configure; timer → never.
func (s *Service) canResolve(ctx context.Context, a service.Actor, i *store.Interrupt) bool {
	if i.Status != store.InterruptPending {
		return false
	}
	switch i.Kind {
	case store.InterruptApproval:
		if len(i.Assignees) > 0 {
			found := false
			for _, u := range i.Assignees {
				if u == a.UserID {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
		_, err := s.o.Access.Application(ctx, a, i.ApplicationID, access.Run)
		return err == nil
	case store.InterruptCallback:
		_, err := s.o.Access.Application(ctx, a, i.ApplicationID, access.Configure)
		return err == nil
	}
	return false
}

// labelFor renders a usr_ id as username-or-email for workspace
// members (the members list already exposes those) and as the bare id
// otherwise; non-user resolvers pass through.
func (s *Service) labelFor(ctx context.Context, wsID, id string) string {
	if !strings.HasPrefix(id, "usr_") {
		return id
	}
	m, err := s.o.Store.GetMember(ctx, wsID, id)
	if err == nil && m != nil {
		if u, err := s.o.Store.GetUser(ctx, id); err == nil {
			if u.Name != "" {
				return u.Name
			}
			return u.Email
		}
		_ = m
	}
	return id
}

func (s *Service) view(ctx context.Context, a service.Actor, i *store.Interrupt, detail bool) (*InterruptView, error) {
	v := &InterruptView{Interrupt: i,
		ApplicationName: s.appName(ctx, i.ApplicationID),
		CanResolve:      s.canResolve(ctx, a, i)}
	if i.ResolvedBy != "" {
		v.ResolvedByLabel = s.labelFor(ctx, i.WorkspaceID, i.ResolvedBy)
	}
	for _, id := range i.Assignees {
		v.AssigneeLabels = append(v.AssigneeLabels, s.labelFor(ctx, i.WorkspaceID, id))
	}
	if i.Kind == store.InterruptCallback && i.Status == store.InterruptPending {
		v.CallbackURL = s.o.ConsoleOrigin + "/api/v1/interrupts/" + i.ID + "/callback"
	}
	if detail {
		if pt, err := s.open(i.Context, "interrupt-context:"+i.ID); err == nil && len(pt) > 0 {
			v.ContextJSON = string(pt)
		}
		if pt, err := s.open(i.Event, "interrupt-event:"+i.ID); err == nil && len(pt) > 0 {
			v.EventJSON = string(pt)
		}
	}
	return v, nil
}

// ListRuns is the application run history, newest first.
func (s *Service) ListRuns(ctx context.Context, a service.Actor, appID, before string, limit int) ([]*store.Run, error) {
	app, err := s.o.Access.Runs(ctx, a, appID)
	if err != nil {
		return nil, err
	}
	runs, err := s.o.Store.ListRuns(ctx, store.RunFilter{
		WorkspaceID: app.WorkspaceID, ApplicationID: app.ID,
		Before: before, Limit: limit,
	})
	if err != nil {
		return nil, service.Internal(err)
	}
	return runs, nil
}

// GetRun returns the full detail: run, steps, interrupts and the
// decrypted output for succeeded runs.
func (s *Service) GetRun(ctx context.Context, a service.Actor, id string) (*RunDetail, error) {
	r, err := s.o.Access.Run(ctx, a, id, access.View)
	if err != nil {
		return nil, err
	}
	steps, err := s.o.Store.ListRunSteps(ctx, id)
	if err != nil {
		return nil, service.Internal(err)
	}
	intrs, err := s.o.Store.ListInterrupts(ctx, store.InterruptFilter{
		WorkspaceID: r.WorkspaceID, RunID: id, Limit: 200,
	})
	if err != nil {
		return nil, service.Internal(err)
	}
	d := &RunDetail{Run: r, Steps: steps}
	for _, i := range intrs {
		if i.Kind == "" {
			continue // never committed: reserved or cancelled slot
		}
		v, err := s.view(ctx, a, i, true)
		if err != nil {
			return nil, err
		}
		d.Interrupts = append(d.Interrupts, v)
	}
	if r.Status == store.RunSucceeded && len(r.Output) > 0 {
		if pt, err := s.open(r.Output, "run:"+r.ID); err == nil {
			d.OutputJSON = string(pt)
		}
	}
	return d, nil
}

// CancelRun cancels a waiting/unknown run (Configure on the app).
func (s *Service) CancelRun(ctx context.Context, a service.Actor, id string) (*store.Run, error) {
	r, err := s.o.Access.Run(ctx, a, id, access.Configure)
	if err != nil {
		return nil, err
	}
	ok, err := s.o.Store.CancelRun(ctx, r.ID, s.o.Now().UTC())
	if err != nil {
		return nil, service.Internal(err)
	}
	if !ok {
		return nil, service.Conflict("run is not cancellable")
	}
	s.auditEvent(r.WorkspaceID, a.UserID, r.ID, "cancel", "run cancelled")
	return s.o.Store.GetRun(ctx, r.ID)
}

// RetryRun requeues a handler-step-unknown run for dispatch.
func (s *Service) RetryRun(ctx context.Context, a service.Actor, id string) (*store.Run, error) {
	r, err := s.o.Access.Run(ctx, a, id, access.Configure)
	if err != nil {
		return nil, err
	}
	ok, err := s.o.Store.RetryRun(ctx, r.ID, s.o.Now().UTC())
	if err != nil {
		return nil, service.Internal(err)
	}
	if !ok {
		return nil, service.Conflict("run is not retryable")
	}
	s.auditEvent(r.WorkspaceID, a.UserID, r.ID, "retry", "run retried")
	return s.o.Store.GetRun(ctx, r.ID)
}

// ListInterrupts is the workspace inbox. resolvable=true keeps only
// interrupts the caller could resolve right now, paging the store
// (status applied, ≤1000 rows scanned) until the page fills; the
// returned cursor continues the scan.
func (s *Service) ListInterrupts(ctx context.Context, a service.Actor, status string, resolvable bool, before string, limit int) ([]*InterruptView, string, error) {
	f, err := s.o.Access.Interrupts(ctx, a)
	if err != nil {
		return nil, "", err
	}
	if limit <= 0 {
		limit = 50
	}
	f.Status, f.Before = status, before
	out := make([]*InterruptView, 0, limit)
	var next string
	scanned := 0
	for {
		f.Limit = 200
		intrs, err := s.o.Store.ListInterrupts(ctx, f)
		if err != nil {
			return nil, "", service.Internal(err)
		}
		if len(intrs) == 0 {
			break
		}
		for _, i := range intrs {
			next = i.ID // deepest row scanned; cursor continues here
			scanned++
			if i.Kind != "" {
				v, err := s.view(ctx, a, i, false)
				if err != nil {
					return nil, "", err
				}
				if !resolvable || v.CanResolve {
					if len(out) < limit {
						out = append(out, v)
					}
				}
			}
			if len(out) >= limit {
				break
			}
		}
		if !resolvable || len(out) >= limit || len(intrs) < f.Limit || scanned >= 1000 {
			break
		}
		f.Before = next
	}
	// The cursor is offered only when more rows may exist beyond the
	// scan — a full store page or a filled result page.
	if len(out) >= limit || (resolvable && scanned >= 1000) {
		return out, next, nil
	}
	return out, "", nil
}

// GetInterrupt is the single-interrupt detail (decrypted context/event).
func (s *Service) GetInterrupt(ctx context.Context, a service.Actor, id string) (*InterruptView, error) {
	i, err := s.o.Access.Interrupt(ctx, a, id, access.View)
	if err != nil {
		return nil, err
	}
	// Never-committed slots are invisible to the console.
	if i.Kind == "" {
		return nil, service.NotFound("interrupt not found")
	}
	return s.view(ctx, a, i, true)
}

// ResolveInterrupt applies a human or operator resolution.
func (s *Service) ResolveInterrupt(ctx context.Context, a service.Actor, id string, event json.RawMessage) (*InterruptView, error) {
	i, err := s.o.Access.Interrupt(ctx, a, id, access.View)
	if err != nil {
		return nil, err
	}
	switch i.Status {
	case store.InterruptSlotReserved:
		return nil, service.NotFound("interrupt not found")
	case store.InterruptPending:
	default:
		return nil, service.Errf(types.ErrCodeAlreadyResolved, "interrupt already resolved")
	}
	if !s.canResolve(ctx, a, i) {
		return nil, service.Forbidden("forbidden")
	}
	schema := i.EventSchema
	if len(schema) == 0 && i.Kind == store.InterruptApproval {
		schema = approvalDefaultEventSchema
	}
	if err := s.validateEvent(schema, event); err != nil {
		return nil, service.BadRequest("invalid event")
	}
	sealed, err := s.sealOK(event, "interrupt-event:"+i.ID)
	if err != nil {
		return nil, service.Internal(err)
	}
	if _, err := s.o.Store.ResolveInterrupt(ctx, i.ID, sealed, a.UserID, s.o.Now().UTC()); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil, service.Errf(types.ErrCodeAlreadyResolved, "interrupt already resolved")
		}
		return nil, service.Internal(err)
	}
	s.auditEvent(i.WorkspaceID, a.UserID, i.RunID, "resolve", "interrupt resolved "+i.ID)
	i2, err := s.o.Store.GetInterrupt(ctx, i.ID)
	if err != nil {
		return nil, service.Internal(err)
	}
	return s.view(ctx, a, i2, true)
}

// Callback accepts a raw event body authenticated only by the
// interrupt-bound key. Pending: kind must be callback. Reserved: the
// event is stored early and validated at commit.
func (s *Service) Callback(ctx context.Context, a service.Actor, id string, event []byte) error {
	i, err := s.o.Access.InterruptCallback(ctx, a, id)
	if err != nil {
		return err
	}
	switch i.Status {
	case store.InterruptPending:
		if i.Kind != store.InterruptCallback {
			return service.NotFound("interrupt not found")
		}
		if err := s.validateEvent(i.EventSchema, event); err != nil {
			return service.BadRequest("invalid event")
		}
	case store.InterruptSlotReserved:
		// early resolution; shape checked, kind/schema validated at commit
	default:
		return service.Errf(types.ErrCodeAlreadyResolved, "interrupt already resolved")
	}
	sealed, err := s.sealOK(event, "interrupt-event:"+i.ID)
	if err != nil {
		return service.Internal(err)
	}
	if _, err := s.o.Store.ResolveInterrupt(ctx, i.ID, sealed, "callback", s.o.Now().UTC()); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return service.Errf(types.ErrCodeAlreadyResolved, "interrupt already resolved")
		}
		return service.Internal(err)
	}
	return nil
}
