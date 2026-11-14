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

// InterruptView is one interrupt as the API renders it.
type InterruptView struct {
	Interrupt       *store.Interrupt
	ApplicationName string
	CanResume       bool
	ResumeURL       string
	ContextJSON     string
	EventJSON       string
	ResolvedByLabel string
}

// RunDetail bundles a run with its steps and interrupts.
type RunDetail struct {
	Run        *store.Run
	Steps      []*store.RunStep
	Interrupts []*InterruptView
	OutputJSON string
}

// nudge wakes the dispatch loop after a fresh resolution so a handler
// is dispatched without waiting for the next Tick.
func (s *Service) nudge() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) appName(ctx context.Context, id string) string {
	a, err := s.o.Store.GetApplication(ctx, id)
	if err != nil {
		return ""
	}
	return a.Name
}

// canResume reports whether a regular actor may resume the interrupt:
// it is pending and the actor has Run on the application.
func (s *Service) canResume(ctx context.Context, a service.Actor, i *store.Interrupt) bool {
	if i.Status != store.InterruptPending {
		return false
	}
	_, err := s.o.Access.Application(ctx, a, i.ApplicationID, access.Run)
	return err == nil
}

// committed reports whether a slot was turned into an interrupt by
// wait; reserved and never-waited (cancelled) slots carry no handler.
func committed(i *store.Interrupt) bool { return i.Handler != "" }

func (s *Service) resumeURL(id string) string {
	return s.o.ConsoleOrigin + "/api/v1/interrupts/" + id + "/resume"
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
		CanResume:       s.canResume(ctx, a, i)}
	if i.ResolvedBy != "" {
		v.ResolvedByLabel = s.labelFor(ctx, i.WorkspaceID, i.ResolvedBy)
	}
	if i.Status == store.InterruptPending {
		v.ResumeURL = s.resumeURL(i.ID)
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
		if !committed(i) {
			continue // reserved or never-waited slot
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

// ListInterrupts is the workspace inbox. resumable=true keeps only
// interrupts the caller could resume right now, paging the store
// (status applied, ≤1000 rows scanned) until the page fills; the
// returned cursor continues the scan.
func (s *Service) ListInterrupts(ctx context.Context, a service.Actor, status string, resumable bool, before string, limit int) ([]*InterruptView, string, error) {
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
			if committed(i) {
				v, err := s.view(ctx, a, i, false)
				if err != nil {
					return nil, "", err
				}
				if !resumable || v.CanResume {
					if len(out) < limit {
						out = append(out, v)
					}
				}
			}
			if len(out) >= limit {
				break
			}
		}
		if !resumable || len(out) >= limit || len(intrs) < f.Limit || scanned >= 1000 {
			break
		}
		f.Before = next
	}
	// The cursor is offered only when more rows may exist beyond the
	// scan — a full store page or a filled result page.
	if len(out) >= limit || (resumable && scanned >= 1000) {
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
	if !committed(i) {
		return nil, service.NotFound("interrupt not found")
	}
	return s.view(ctx, a, i, true)
}

// Resume records the single resolution of an interrupt with a JSON
// object event and wakes the dispatcher. The interrupt's own resume
// token may resume it even while still reserved (the step has not
// reached wait yet; the event is applied when wait commits). Any other
// caller needs Run on the application and a pending interrupt. First
// resume wins; later ones get already_resolved.
func (s *Service) Resume(ctx context.Context, a service.Actor, id string, event json.RawMessage) error {
	i, byToken, err := s.o.Access.InterruptToken(ctx, a, id)
	if err != nil {
		return err
	}
	resolvedBy := store.ResolvedByToken
	if !byToken {
		resolvedBy = a.UserID
		if i, err = s.o.Access.Interrupt(ctx, a, id, access.View); err != nil {
			return err
		}
		if !committed(i) {
			return service.NotFound("interrupt not found")
		}
	}
	switch {
	case i.Status == store.InterruptPending:
		if !byToken && !s.canResume(ctx, a, i) {
			return service.Forbidden("forbidden")
		}
	case i.Status == store.InterruptSlotReserved && byToken:
	default:
		return service.Errf(types.ErrCodeAlreadyResolved, "interrupt already resolved")
	}
	if err := s.validateEvent(event); err != nil {
		return service.BadRequest("event must be a JSON object")
	}
	sealed, err := s.sealOK(event, "interrupt-event:"+i.ID)
	if err != nil {
		return service.Internal(err)
	}
	if _, err := s.o.Store.ResolveInterrupt(ctx, i.ID, sealed, resolvedBy, s.o.Now().UTC()); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return service.Errf(types.ErrCodeAlreadyResolved, "interrupt already resolved")
		}
		return service.Internal(err)
	}
	s.auditEvent(i.WorkspaceID, a.UserID, i.RunID, "resume", "interrupt resumed "+i.ID)
	s.nudge()
	return nil
}
