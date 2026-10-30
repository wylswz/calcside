package api

import (
	"context"
	"encoding/json"
	"net/http"

	"calcside/cmd/calcside/internal/api/gen"
	"calcside/cmd/calcside/internal/service"
	runsvc "calcside/cmd/calcside/internal/service/run"
	"calcside/internal/store"
)

// Run/interrupt endpoints (EP-0016). Contexts, events and key ids are
// never exposed; context_json/event_json appear only on detail routes
// for callers with View.

func runModel(r *store.Run) gen.Run {
	m := gen.Run{
		Id: r.ID, ApplicationId: r.ApplicationID, ReleaseId: r.ReleaseID,
		UserId: r.UserID, Trigger: r.Trigger, Status: r.Status, Steps: r.Steps,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
	if r.InterruptID != "" {
		m.InterruptId = &r.InterruptID
	}
	if r.ErrorCode != "" {
		m.ErrorCode = &r.ErrorCode
	}
	if r.EndedAt != nil {
		m.EndedAt = r.EndedAt
	}
	return m
}

func stepModel(st *store.RunStep) gen.RunStep {
	m := gen.RunStep{
		Seq: st.Seq, Attempt: st.Attempt, Entry: st.Entry,
		Status: st.Status, DurationMs: st.DurationMs, StartedAt: st.StartedAt,
	}
	if st.InterruptID != "" {
		m.InterruptId = &st.InterruptID
	}
	if st.ErrorCode != "" {
		m.ErrorCode = &st.ErrorCode
	}
	if st.EndedAt != nil {
		m.EndedAt = st.EndedAt
	}
	return m
}

func interruptModel(v *runsvc.InterruptView) gen.Interrupt {
	i := v.Interrupt
	m := gen.Interrupt{
		Id: i.ID, RunId: i.RunID, ApplicationId: i.ApplicationID,
		ApplicationName: v.ApplicationName, Status: i.Status,
		CanResolve: v.CanResolve, CreatedAt: i.CreatedAt,
	}
	if i.Kind != "" {
		m.Kind = &i.Kind
	}
	if i.Title != "" {
		m.Title = &i.Title
	}
	if i.Handler != "" {
		m.Handler = &i.Handler
	}
	if len(i.Assignees) > 0 {
		as := i.Assignees
		m.Assignees = &as
	}
	if len(v.AssigneeLabels) > 0 {
		al := v.AssigneeLabels
		m.AssigneeLabels = &al
	}
	if v.ResolvedByLabel != "" {
		m.ResolvedByLabel = &v.ResolvedByLabel
	}
	if len(i.EventSchema) > 0 {
		s := string(i.EventSchema)
		m.EventSchema = &s
	}
	if i.OnExpire != "" {
		m.OnExpire = &i.OnExpire
	}
	if i.ExpiresAt != nil {
		m.ExpiresAt = i.ExpiresAt
	}
	if i.FireAt != nil {
		m.FireAt = i.FireAt
	}
	if i.ResolvedBy != "" {
		m.ResolvedBy = &i.ResolvedBy
	}
	if i.ResolvedAt != nil {
		m.ResolvedAt = i.ResolvedAt
	}
	if v.CallbackURL != "" {
		m.CallbackUrl = &v.CallbackURL
	}
	if v.ContextJSON != "" {
		m.ContextJson = &v.ContextJSON
	}
	if v.EventJSON != "" {
		m.EventJson = &v.EventJSON
	}
	return m
}

type listRunsResp struct{ rawJSON }

func (r listRunsResp) VisitListRunsResponse(w http.ResponseWriter) error { return r.write(w) }

type getRunResp struct{ rawJSON }

func (r getRunResp) VisitGetRunResponse(w http.ResponseWriter) error { return r.write(w) }

type cancelRunResp struct{ rawJSON }

func (r cancelRunResp) VisitCancelRunResponse(w http.ResponseWriter) error { return r.write(w) }

type retryRunResp struct{ rawJSON }

func (r retryRunResp) VisitRetryRunResponse(w http.ResponseWriter) error { return r.write(w) }

type listInterruptsResp struct{ rawJSON }

func (r listInterruptsResp) VisitListInterruptsResponse(w http.ResponseWriter) error {
	return r.write(w)
}

type getInterruptResp struct{ rawJSON }

func (r getInterruptResp) VisitGetInterruptResponse(w http.ResponseWriter) error {
	return r.write(w)
}

type resolveInterruptResp struct{ rawJSON }

func (r resolveInterruptResp) VisitResolveInterruptResponse(w http.ResponseWriter) error {
	return r.write(w)
}

type interruptCallbackResp struct{ rawJSON }

func (r interruptCallbackResp) VisitInterruptCallbackResponse(w http.ResponseWriter) error {
	return r.write(w)
}

func (s *strictImpl) ListRuns(ctx context.Context, req gen.ListRunsRequestObject) (gen.ListRunsResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return listRunsResp{*e}, nil
	}
	a := actorOf(p)
	var before string
	if req.Params.Before != nil {
		before = *req.Params.Before
	}
	var limit int
	if req.Params.Limit != nil {
		limit = int(*req.Params.Limit)
	}
	runs, err := s.d.Run.ListRuns(ctx, a, req.Id, before, limit)
	if err != nil {
		return listRunsResp{fail(err)}, nil
	}
	out := make([]gen.Run, 0, len(runs))
	for _, r := range runs {
		out = append(out, runModel(r))
	}
	return listRunsResp{rawJSON{http.StatusOK, gen.RunsResponse{Runs: &out}}}, nil
}

func (s *strictImpl) GetRun(ctx context.Context, req gen.GetRunRequestObject) (gen.GetRunResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return getRunResp{*e}, nil
	}
	d, err := s.d.Run.GetRun(ctx, actorOf(p), req.Id)
	if err != nil {
		return getRunResp{fail(err)}, nil
	}
	rd := gen.RunDetail{
		Id: d.Run.ID, ApplicationId: d.Run.ApplicationID, ReleaseId: d.Run.ReleaseID,
		UserId: d.Run.UserID, Trigger: d.Run.Trigger, Status: d.Run.Status,
		Steps: d.Run.Steps, CreatedAt: d.Run.CreatedAt, UpdatedAt: d.Run.UpdatedAt,
	}
	if d.Run.InterruptID != "" {
		rd.InterruptId = &d.Run.InterruptID
	}
	if d.Run.ErrorCode != "" {
		rd.ErrorCode = &d.Run.ErrorCode
	}
	if d.Run.EndedAt != nil {
		rd.EndedAt = d.Run.EndedAt
	}
	if d.OutputJSON != "" {
		rd.OutputJson = &d.OutputJSON
	}
	steps := make([]gen.RunStep, 0, len(d.Steps))
	for _, st := range d.Steps {
		steps = append(steps, stepModel(st))
	}
	intrs := make([]gen.Interrupt, 0, len(d.Interrupts))
	for _, v := range d.Interrupts {
		intrs = append(intrs, interruptModel(v))
	}
	return getRunResp{rawJSON{http.StatusOK, gen.RunResponse{
		Run: &rd, Steps: &steps, Interrupts: &intrs}}}, nil
}

func (s *strictImpl) CancelRun(ctx context.Context, req gen.CancelRunRequestObject) (gen.CancelRunResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return cancelRunResp{*e}, nil
	}
	r, err := s.d.Run.CancelRun(ctx, actorOf(p), req.Id)
	if err != nil {
		return cancelRunResp{fail(err)}, nil
	}
	m := runModel(r)
	return cancelRunResp{rawJSON{http.StatusOK, gen.RunsResponse{Runs: &[]gen.Run{m}}}}, nil
}

func (s *strictImpl) RetryRun(ctx context.Context, req gen.RetryRunRequestObject) (gen.RetryRunResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return retryRunResp{*e}, nil
	}
	r, err := s.d.Run.RetryRun(ctx, actorOf(p), req.Id)
	if err != nil {
		return retryRunResp{fail(err)}, nil
	}
	m := runModel(r)
	return retryRunResp{rawJSON{http.StatusOK, gen.RunsResponse{Runs: &[]gen.Run{m}}}}, nil
}

func (s *strictImpl) ListInterrupts(ctx context.Context, req gen.ListInterruptsRequestObject) (gen.ListInterruptsResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return listInterruptsResp{*e}, nil
	}
	var status, before string
	var resolvable bool
	var limit int
	if req.Params.Status != nil {
		status = *req.Params.Status
	}
	if req.Params.Resolvable != nil {
		resolvable = *req.Params.Resolvable
	}
	if req.Params.Before != nil {
		before = *req.Params.Before
	}
	if req.Params.Limit != nil {
		limit = int(*req.Params.Limit)
	}
	views, next, err := s.d.Run.ListInterrupts(ctx, actorOf(p), status, resolvable, before, limit)
	if err != nil {
		return listInterruptsResp{fail(err)}, nil
	}
	out := make([]gen.Interrupt, 0, len(views))
	for _, v := range views {
		out = append(out, interruptModel(v))
	}
	resp := gen.InterruptsResponse{Interrupts: &out}
	if next != "" {
		resp.NextBefore = &next
	}
	return listInterruptsResp{rawJSON{http.StatusOK, resp}}, nil
}

func (s *strictImpl) GetInterrupt(ctx context.Context, req gen.GetInterruptRequestObject) (gen.GetInterruptResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return getInterruptResp{*e}, nil
	}
	v, err := s.d.Run.GetInterrupt(ctx, actorOf(p), req.Id)
	if err != nil {
		return getInterruptResp{fail(err)}, nil
	}
	m := interruptModel(v)
	return getInterruptResp{rawJSON{http.StatusOK, gen.InterruptResponse{Interrupt: &m}}}, nil
}

func (s *strictImpl) ResolveInterrupt(ctx context.Context, req gen.ResolveInterruptRequestObject) (gen.ResolveInterruptResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return resolveInterruptResp{*e}, nil
	}
	if req.Body == nil {
		return resolveInterruptResp{fail(service.BadRequest("body required"))}, nil
	}
	v, err := s.d.Run.ResolveInterrupt(ctx, actorOf(p), req.Id, json.RawMessage(req.Body.Event))
	if err != nil {
		return resolveInterruptResp{fail(err)}, nil
	}
	m := interruptModel(v)
	return resolveInterruptResp{rawJSON{http.StatusOK, gen.InterruptResponse{Interrupt: &m}}}, nil
}

func (s *strictImpl) InterruptCallback(ctx context.Context, req gen.InterruptCallbackRequestObject) (gen.InterruptCallbackResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return interruptCallbackResp{*e}, nil
	}
	a := actorOf(p)
	if req.Body == nil {
		return interruptCallbackResp{fail(service.BadRequest("body required"))}, nil
	}
	if err := s.d.Run.Callback(ctx, a, req.Id, []byte(*req.Body)); err != nil {
		return interruptCallbackResp{fail(err)}, nil
	}
	status := "accepted"
	return interruptCallbackResp{rawJSON{http.StatusAccepted,
		gen.InterruptCallbackResponse{Status: &status}}}, nil
}
