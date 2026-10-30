package run

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"calcside/cmd/calcside/internal/service"
	"calcside/internal/runtime"
	"calcside/internal/store"
)

// dispatch.go is the background reconciler: due expirations, stale
// dispatch recovery, and claimed-interrupt handler dispatch. Every
// transition is a store CAS, so any number of API replicas may run the
// loop; a job cancelled mid-flight is recovered as unknown later.

// Start launches the dispatch loop; Stop cancels it and every in-flight
// job (their store claims lapse and are recovered).
func (s *Service) Start() {
	go func() {
		defer close(s.done)
		tick := time.NewTicker(Tick)
		defer tick.Stop()
		ctx, cancel := context.WithCancel(context.Background())
		go func() { <-s.stop; cancel() }()
		defer cancel()
		for {
			select {
			case <-s.stop:
				return
			case <-tick.C:
				s.RunOnce(ctx)
			}
		}
	}()
}

// Stop cancels the loop and waits for it to exit.
func (s *Service) Stop() {
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
	<-s.done
}

// RunOnce performs one reconciliation pass: due expiries/timer fires,
// stale-dispatch recovery, then handler dispatches up to
// DispatchConcurrency. Exported for tests and single-replica use.
func (s *Service) RunOnce(ctx context.Context) {
	now := s.o.Now().UTC()
	due, err := s.o.Store.ListDueInterrupts(ctx, now, 64)
	if err == nil {
		for _, d := range due {
			if d.Kind == store.InterruptTimer {
				if ok, err := s.o.Store.FireTimer(ctx, d.ID, now); err == nil && ok {
					s.auditEvent(d.WorkspaceID, "", d.RunID, "dispatch", "timer fired "+d.ID)
				}
			} else {
				if ok, err := s.o.Store.ExpireInterrupt(ctx, d.ID, now); err == nil && ok {
					s.auditEvent(d.WorkspaceID, "", d.RunID, "expire", "interrupt expired "+d.ID)
				}
			}
		}
	}
	stale, err := s.o.Store.ListStaleDispatches(ctx, now, 64)
	if err == nil {
		for _, d := range stale {
			_, _ = s.o.Store.RecoverStaleDispatch(ctx, d.ID, now)
		}
	}
	for i := 0; i < DispatchConcurrency; i++ {
		intr, err := s.o.Store.ClaimDispatch(ctx, now, now.Add(ClaimFor))
		if errors.Is(err, store.ErrNotFound) {
			return
		}
		if err != nil {
			return
		}
		s.dispatch(ctx, intr)
	}
}

// backoff is the dispatch deferral delay: 2s doubling, capped at 5m.
func backoff(attempts int) time.Duration {
	d := 2 * time.Second
	for i := 0; i < attempts && d < 5*time.Minute; i++ {
		d *= 2
	}
	if d > 5*time.Minute {
		d = 5 * time.Minute
	}
	return d
}

func (s *Service) deferrable(ctx context.Context, intr *store.Interrupt) {
	after := s.o.Now().UTC().Add(backoff(intr.DispatchAttempts))
	_ = s.o.Store.DeferDispatch(ctx, intr.ID, after)
}

// ownerFor resolves the actor the replica runs under: the resolver user
// when the interrupt was resolved by a usr_, else the app owner.
func (s *Service) actorFor(ctx context.Context, app *store.Application, intr *store.Interrupt) service.Actor {
	uid := app.UserID
	if strings.HasPrefix(intr.ResolvedBy, "usr_") {
		uid = intr.ResolvedBy
	}
	return service.Actor{UserID: uid, WorkspaceID: app.WorkspaceID}
}

// dispatch runs one claimed interrupt to a handler step outcome.
func (s *Service) dispatch(ctx context.Context, intr *store.Interrupt) {
	now := s.o.Now().UTC()
	run, err := s.o.Store.GetRun(ctx, intr.RunID)
	if err != nil {
		return
	}
	app, err := s.o.Store.GetApplication(ctx, intr.ApplicationID)
	if err != nil {
		return
	}
	rel, err := s.o.Store.GetServingRelease(ctx, run.WorkspaceID, run.ReleaseID)
	if err != nil {
		return
	}
	a := s.actorFor(ctx, app, intr)
	spec, pols, secs, err := s.o.Sandbox.ApplicationBoundary(ctx, a, rel.Spec)
	if err != nil {
		s.deferrable(ctx, intr)
		return
	}
	u, err := s.o.Store.GetUser(ctx, app.UserID)
	if err != nil {
		s.deferrable(ctx, intr)
		return
	}
	owner := runtime.Owner{UserID: u.ID, Email: u.Email}

	// Ephemeral replica, same shape as application Test.
	replicaID := store.NewID("apv_")
	prep, perr := s.o.Runtime.Prepare(ctx, &runtime.PrepareRequest{
		CreateRequest: runtime.CreateRequest{
			InstanceID: replicaID, Owner: owner,
			Labels: map[string]string{"name": app.Name, "application_id": app.ID},
			Spec:   spec, Policies: pols, Secrets: secs,
			ExpiresAt: now.Add(prepareLease), Epoch: 1,
		},
		ReleaseID: rel.ID, Generation: 1, Program: rel.Program,
		PrepareID: store.NewID("prep_"),
	})
	epoch := int64(1)
	if prep != nil {
		s.o.Audit.RecordWorkspace(app.WorkspaceID, prep.Audit)
		epoch = prep.Epoch
	}
	cleanup := func() {
		cctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = s.o.Runtime.Delete(cctx, &runtime.DeleteRequest{
			InstanceID: replicaID, Owner: owner, Epoch: epoch})
	}
	defer cleanup()
	if perr != nil {
		// Transport/capacity failure before any code ran: requeue
		// with backoff (program-level failures arrive in the result,
		// not the error).
		s.deferrable(ctx, intr)
		return
	}

	handler := intr.Handler
	resolution := "resolved"
	if intr.Status == store.InterruptExpired {
		handler = intr.OnExpire
		resolution = "expired"
	}
	// A retry reuses the failed step's seq: Steps only grows on
	// attempt 1, so attempt N>1 writes to the last seq again.
	seq := run.Steps
	if intr.Attempt > 1 {
		seq = run.Steps - 1
	}

	if prep.Result.Error != nil {
		// The program failed to prepare: the step is recorded and
		// fails the run.
		step := &store.RunStep{RunID: run.ID, Seq: seq, Attempt: intr.Attempt,
			Entry: handler, InterruptID: intr.ID, Status: store.StepRunning, StartedAt: now}
		if err := s.o.Store.StartHandlerStep(ctx, intr.ID, step, nil, now); err != nil {
			return
		}
		_ = s.o.Store.FinishStep(ctx, store.StepOutcome{
			RunID: run.ID, Seq: seq, Attempt: intr.Attempt, Status: store.StepFailed,
			ErrorCode: prep.Result.Error.Code, DispatchedInterruptID: intr.ID, Now: now,
		}, nil)
		return
	}

	// Mint the new step's slots, then start the handler step.
	var slots []*store.Interrupt
	var wire []runtime.InterruptSlot
	if s.o.Cipher != nil {
		var err error
		slots, wire, err = s.mintSlots(ctx, app, run.ID, seq, now)
		if err != nil {
			s.deferrable(ctx, intr)
			return
		}
	}
	step := &store.RunStep{RunID: run.ID, Seq: seq, Attempt: intr.Attempt,
		Entry: handler, InterruptID: intr.ID, Status: store.StepRunning, StartedAt: now}
	if err := s.o.Store.StartHandlerStep(ctx, intr.ID, step, slots, now); err != nil {
		return // a concurrent transition (cancel/recover) won
	}
	// Chain-limit checks need the post-start step count.
	if fresh, err := s.o.Store.GetRun(ctx, run.ID); err == nil {
		run = fresh
	}

	// Build the handler-context input.
	ctxJSON, _ := s.open(intr.Context, "interrupt-context:"+intr.ID)
	var eventJSON json.RawMessage
	if intr.Status != store.InterruptExpired && len(intr.Event) > 0 {
		if ev, err := s.open(intr.Event, "interrupt-event:"+intr.ID); err == nil {
			eventJSON = ev
		}
	}
	if ctxJSON == nil {
		ctxJSON = json.RawMessage(`{}`)
	}
	if eventJSON == nil {
		eventJSON = json.RawMessage(`null`)
	}
	resolvedAt := ""
	if intr.ResolvedAt != nil {
		resolvedAt = intr.ResolvedAt.UTC().Format(time.RFC3339)
	}
	inputMap := map[string]any{
		"run_id": run.ID, "step": seq, "interrupt_id": intr.ID,
		"kind": intr.Kind, "handler": handler,
		"context": json.RawMessage(ctxJSON), "resolution": resolution,
		"event": json.RawMessage(eventJSON), "resolved_by": intr.ResolvedBy,
		"resolved_at": resolvedAt, "attempt": intr.Attempt,
	}
	input, err := json.Marshal(inputMap)
	if err != nil {
		return
	}

	actor := runtime.Owner{UserID: a.UserID, Email: a.Email}
	out, ierr := s.o.Runtime.Invoke(ctx, &runtime.InvokeRequest{
		InstanceID: replicaID, Owner: owner, Actor: actor,
		ReleaseID: rel.ID, Generation: 1, Epoch: epoch,
		InvokeID: store.NewID("inv_"), Input: input,
		Entry: handler, RunID: run.ID, Step: seq, Interrupts: wire,
	})
	if out != nil {
		s.o.Audit.RecordWorkspace(app.WorkspaceID, out.Audit)
	}
	st := &Started{Run: run, Interrupts: wire}
	if _, err := s.Complete(ctx, st, seq, intr.Attempt, handler, intr.ID, out, ierr); err == nil {
		s.auditEvent(app.WorkspaceID, a.UserID, run.ID, "dispatch", "handler dispatched "+intr.ID)
	}
}
