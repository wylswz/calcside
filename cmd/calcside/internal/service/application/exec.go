package application

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"calcside/cmd/calcside/internal/service"
	"calcside/cmd/calcside/internal/service/access"
	runsvc "calcside/cmd/calcside/internal/service/run"
	"calcside/internal/runtime"
	"calcside/internal/store"
	"calcside/internal/types"
)

// The caller defers cleanup before checking the outcome. In particular, a
// successful test must retain its replica until Invoke has finished.
func (s *Service) prepare(ctx context.Context, workspaceID string, req *runtime.PrepareRequest) (*runtime.PrepareResponse, func(), error) {
	resp, err := s.o.Runtime.Prepare(ctx, req)
	epoch := req.Epoch
	if resp != nil {
		s.o.Audit.RecordWorkspace(workspaceID, resp.Audit)
		epoch = resp.Epoch
	}
	cleanup := func() {
		cctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = s.o.Runtime.Delete(cctx, &runtime.DeleteRequest{
			InstanceID: req.InstanceID, Owner: req.Owner, Epoch: epoch})
	}
	if err != nil {
		return nil, cleanup, fail(err)
	}
	if resp == nil {
		return nil, cleanup, fail(errNilResult)
	}
	return resp, cleanup, nil
}

var errNilResult = errors.New("empty runtime response")

// finishRun maps a run outcome onto the wire view.
func runView(oc *runsvc.Outcome, res runtime.ProgramResult) *RunView {
	rv := &RunView{Result: res}
	if oc != nil {
		rv.RunID = oc.RunID
		rv.RunStatus = oc.Status
		rv.Waiting = oc.Waiting
		if oc.OutputRaw != nil {
			rv.OutputJSON = string(oc.OutputRaw)
		} else if res.Output != nil {
			// Cipher-less runs record without output; the caller
			// still gets the live result.
			rv.OutputJSON = string(res.Output)
		}
	} else if res.Output != nil {
		rv.OutputJSON = string(res.Output)
	}
	return rv
}

func (s *Service) owner(ctx context.Context, userID string) (runtime.Owner, error) {
	u, err := s.o.Store.GetUser(ctx, userID)
	if err != nil {
		return runtime.Owner{}, fail(err)
	}
	return runtime.Owner{UserID: u.ID, Email: u.Email}, nil
}

// release builds the immutable release from the app's draft —
// normalized boundary, default object schemas materialized here.
func (s *Service) release(ctx context.Context, a service.Actor, app *store.Application) (*store.ServingRelease, []runtime.Secret, error) {
	spec, pols, secs, err := s.boundary(ctx, a, app.WorkspaceID, app.Spec)
	if err != nil {
		return nil, nil, err
	}
	in, out := app.InputSchema, app.OutputSchema
	if in == nil {
		in = store.DefaultApplicationSchema()
	}
	if out == nil {
		out = store.DefaultApplicationSchema()
	}
	rel, err := store.NewServingRelease(app.WorkspaceID, app.UserID, spec, pols,
		runtime.ProgramBundle{
			Sources:      map[string][]byte{"main.star": []byte(app.Code)},
			InputSchema:  in,
			OutputSchema: out,
		}, s.o.Now())
	if err != nil {
		return nil, nil, fail(err)
	}
	return rel, secs, nil
}

// Publish: access -> exact version -> build release -> prepare once ->
// audit -> reject failed prepare -> atomic store CAS. No DB write until
// the program proves it can initialize.
func (s *Service) Publish(ctx context.Context, a service.Actor, id string, version int64) (*View, error) {
	app, err := s.o.Access.Application(ctx, a, id, access.Configure)
	if err != nil {
		return nil, err
	}
	if version < 1 || app.Version != version {
		return nil, service.Conflict("application changed")
	}
	if app.PublishedVersion == version && app.PersistentInstanceID != "" {
		return s.view(ctx, app), nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	rel, secs, err := s.release(ctx, a, app)
	if err != nil {
		return nil, err
	}
	owner, err := s.owner(ctx, app.UserID)
	if err != nil {
		return nil, err
	}
	replicaID := store.NewID("apv_")
	prep, cleanup, err := s.prepare(ctx, app.WorkspaceID, &runtime.PrepareRequest{
		CreateRequest: runtime.CreateRequest{
			InstanceID: replicaID, Owner: owner,
			Labels: map[string]string{"name": app.Name, "application_id": app.ID},
			Spec:   rel.Spec, Policies: rel.Policies, Secrets: secs,
			ExpiresAt: s.o.Now().Add(30 * time.Second), Epoch: 1,
		},
		ReleaseID: rel.ID, Generation: 1, Program: rel.Program,
		PrepareID: store.NewID("prep_"),
	})
	defer cleanup()
	if err != nil {
		return nil, err
	}
	if prep.Result.Error != nil {
		return nil, service.BadRequest("program failed to prepare")
	}
	_, err = s.o.Store.PublishApplication(ctx, app.WorkspaceID, id, version, rel, s.o.Now())
	if err != nil {
		return nil, fail(err)
	}
	cur, err := s.o.Store.GetApplication(ctx, id)
	if err != nil {
		return nil, fail(err)
	}
	return s.view(ctx, cur), nil
}

// Test runs a draft ephemerally — prepare + one invoke, nothing
// persisted. The semaphore bounds concurrent admissions; a failed
// prepare still returns the engine result so the UI shows the error.
func (s *Service) Test(ctx context.Context, a service.Actor, id string, version int64, input json.RawMessage) (*RunView, error) {
	app, err := s.o.Access.Application(ctx, a, id, access.Configure)
	if err != nil {
		return nil, err
	}
	if app.Version != version {
		return nil, service.Errf(types.ErrCodeConflict, "application changed")
	}
	select {
	case s.testSlots <- struct{}{}:
		defer func() { <-s.testSlots }()
	default:
		return nil, service.Errf(types.ErrCodeTooMany, "too many tests running")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	rel, secs, err := s.release(ctx, a, app)
	if err != nil {
		return nil, err
	}
	owner, err := s.owner(ctx, app.UserID)
	if err != nil {
		return nil, err
	}
	replicaID := store.NewID("apv_")
	prep, cleanup, err := s.prepare(ctx, app.WorkspaceID, &runtime.PrepareRequest{
		CreateRequest: runtime.CreateRequest{
			InstanceID: replicaID, Owner: owner,
			Labels: map[string]string{"name": app.Name, "application_id": app.ID},
			Spec:   rel.Spec, Policies: rel.Policies, Secrets: secs,
			ExpiresAt: s.o.Now().Add(30 * time.Second), Epoch: 1,
		},
		ReleaseID: rel.ID, Generation: 1, Program: rel.Program,
		PrepareID: store.NewID("prep_"),
	})
	defer cleanup()
	if err != nil {
		return nil, err
	}
	if prep.Result.Error != nil {
		return &RunView{Result: prep.Result}, nil
	}
	// Durable run: pin the release so later handler steps can load it,
	// then record + invoke + complete the first step.
	var st *runsvc.Started
	if s.o.Runs != nil {
		if err := s.o.Store.CreateServingRelease(ctx, rel); err != nil &&
			!errors.Is(err, store.ErrConflict) {
			return nil, fail(err)
		}
		st, err = s.o.Runs.Begin(ctx, app, rel, store.RunTriggerTest, a)
		if err != nil {
			return nil, err
		}
	}
	var slots []runtime.InterruptSlot
	var runID string
	if st != nil {
		slots, runID = st.Interrupts, st.Run.ID
	}
	out, err := s.o.Runtime.Invoke(ctx, &runtime.InvokeRequest{
		InstanceID: replicaID, Owner: owner,
		Actor:     runtime.Owner{UserID: a.UserID, Email: a.Email},
		ReleaseID: rel.ID, Generation: 1, Epoch: prep.Epoch,
		InvokeID: store.NewID("inv_"), Input: input,
		RunID: runID, Step: 0, Interrupts: slots,
	})
	if out != nil {
		s.o.Audit.RecordWorkspace(app.WorkspaceID, out.Audit)
	}
	if st != nil {
		oc, cerr := s.o.Runs.Complete(ctx, st, 0, 1, "main", "", out, err)
		if cerr != nil {
			return nil, cerr
		}
		var res runtime.ProgramResult
		if out != nil {
			res = out.Result
		}
		return runView(oc, res), nil
	}
	if err != nil {
		return nil, fail(err)
	}
	if out == nil {
		return nil, fail(errNilResult)
	}
	return runView(nil, out.Result), nil
}

// Invoke runs the published application against its fenced live replica.
func (s *Service) Invoke(ctx context.Context, a service.Actor, id string, input json.RawMessage) (*RunView, error) {
	app, err := s.o.Access.Application(ctx, a, id, access.Run)
	if err != nil {
		return nil, err
	}
	rep, pin, err := s.readyReplica(ctx, app)
	if err != nil {
		return nil, err
	}
	owner, err := s.owner(ctx, pin.UserID)
	if err != nil {
		return nil, err
	}
	var st *runsvc.Started
	if s.o.Runs != nil {
		rel, rerr := s.o.Store.GetServingRelease(ctx, app.WorkspaceID, rep.ReleaseID)
		if rerr != nil {
			return nil, fail(rerr)
		}
		st, err = s.o.Runs.Begin(ctx, app, rel, store.RunTriggerInvoke, a)
		if err != nil {
			return nil, err
		}
	}
	var slots []runtime.InterruptSlot
	var runID string
	if st != nil {
		slots, runID = st.Interrupts, st.Run.ID
	}
	out, err := s.o.Runtime.Invoke(ctx, &runtime.InvokeRequest{
		InstanceID: rep.ID, Owner: owner,
		Actor:     runtime.Owner{UserID: a.UserID, Email: a.Email},
		ReleaseID: rep.ReleaseID, Generation: rep.Generation, Epoch: rep.Epoch,
		InvokeID: store.NewID("inv_"), Input: input,
		RunID: runID, Step: 0, Interrupts: slots,
	})
	if out != nil {
		s.o.Audit.RecordWorkspace(app.WorkspaceID, out.Audit)
	}
	if st != nil {
		oc, cerr := s.o.Runs.Complete(ctx, st, 0, 1, "main", "", out, err)
		if cerr != nil {
			return nil, cerr
		}
		var res runtime.ProgramResult
		if out != nil {
			res = out.Result
		}
		return runView(oc, res), nil
	}
	if err != nil {
		return nil, fail(err)
	}
	if out == nil {
		return nil, fail(errNilResult)
	}
	return runView(nil, out.Result), nil
}
