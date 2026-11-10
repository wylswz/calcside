package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"calcside/cmd/calcside/internal/api/gen"
	"calcside/cmd/calcside/internal/service"
	"calcside/cmd/calcside/internal/service/application"
	"calcside/internal/runtime"
	"calcside/internal/store"
	"calcside/internal/types"
)

// Application endpoints: durable named-function drafts with real
// publish/test/invoke. Schemas are optional strings (null = default
// open object); input and spec stay json.RawMessage end to end so
// high-precision numbers are never decoded through float64.

func appFail(err error) rawJSON {
	// not_running means the deployed application cannot serve — a
	// dependency signal, not a client conflict. Instances keep their
	// 409; applications map to 503.
	var se *service.Error
	if errors.As(err, &se) && se.Code == types.ErrCodeNotRunning {
		return rawJSON{http.StatusServiceUnavailable, errEnv(se.Code, se.Msg)}
	}
	return fail(err)
}

func appDTO(v *application.View) gen.Application {
	a := v.Application
	spec := a.Spec
	if len(spec) == 0 {
		spec = json.RawMessage(`{}`)
	}
	d := gen.Application{
		Id: a.ID, WorkspaceId: a.WorkspaceID, UserId: a.UserID,
		Name: a.Name, Code: a.Code,
		Spec:                  gen.RawJSON(spec),
		SpecJson:              strPtr(string(a.Spec)),
		Version:               a.Version,
		PublishedVersion:      a.PublishedVersion,
		Status:                gen.ApplicationStatus(v.Status),
		HasUnpublishedChanges: v.HasUnpublishedChanges,
		CreatedAt:             a.CreatedAt,
		UpdatedAt:             a.UpdatedAt,
	}
	if a.InputSchema != nil {
		s := string(a.InputSchema)
		d.InputSchema = &s
	}
	if a.OutputSchema != nil {
		s := string(a.OutputSchema)
		d.OutputSchema = &s
	}
	if a.PersistentInstanceID != "" {
		d.PersistentInstanceId = &a.PersistentInstanceID
	}
	if v.ReleaseID != "" {
		d.ReleaseId = &v.ReleaseID
	}
	if v.ErrorCode != "" {
		d.ErrorCode = &v.ErrorCode
	}
	return d
}

func runDTO(rv *application.RunView) gen.ApplicationRunResponse {
	out := gen.ApplicationRunResponse{Result: rv.Result}
	if rv.OutputJSON != "" {
		out.OutputJson = &rv.OutputJSON
	}
	if rv.RunID != "" {
		out.RunId = &rv.RunID
	}
	if rv.RunStatus != "" {
		out.RunStatus = &rv.RunStatus
	}
	if rv.Waiting != nil {
		w := rv.Waiting
		out.Waiting = &gen.InterruptWaiting{
			InterruptId: w.InterruptID, ConsoleUrl: w.ConsoleURL,
		}
	}
	return out
}

func strPtr(s string) *string { return &s }

func schemaSide(s *string) json.RawMessage {
	if s == nil || *s == "" {
		return nil
	}
	return json.RawMessage(*s)
}

// --- request/response plumbing ---

type listApplicationsResp struct{ rawJSON }

func (r listApplicationsResp) VisitListApplicationsResponse(w http.ResponseWriter) error {
	return r.write(w)
}

type createApplicationResp struct{ rawJSON }

func (r createApplicationResp) VisitCreateApplicationResponse(w http.ResponseWriter) error {
	return r.write(w)
}

type getApplicationResp struct{ rawJSON }

func (r getApplicationResp) VisitGetApplicationResponse(w http.ResponseWriter) error {
	return r.write(w)
}

type updateApplicationResp struct{ rawJSON }

func (r updateApplicationResp) VisitUpdateApplicationResponse(w http.ResponseWriter) error {
	return r.write(w)
}

type deleteApplicationResp struct{ rawJSON }

func (r deleteApplicationResp) VisitDeleteApplicationResponse(w http.ResponseWriter) error {
	return r.write(w)
}

type publishApplicationResp struct{ rawJSON }

func (r publishApplicationResp) VisitPublishApplicationResponse(w http.ResponseWriter) error {
	return r.write(w)
}

const sseWriteGrace = 30 * time.Second

type sseSink struct {
	w        http.ResponseWriter
	rc       *http.ResponseController
	deadline time.Time
	started  bool
	items    int64
	bytes    int64
	err      error
}

var _ runtime.InvokeStreamSink = (*sseSink)(nil)
var _ runtime.InvokeStreamSinkStats = (*sseSink)(nil)

func (s *sseSink) Stats() (bool, int64, int64) { return s.started, s.items, s.bytes }

func (s *sseSink) frame(b []byte) error {
	if s.err != nil {
		return s.err
	}
	deadline := time.Now().Add(sseWriteGrace)
	if !s.deadline.IsZero() && s.deadline.Before(deadline) {
		deadline = s.deadline
	}
	_ = s.rc.SetWriteDeadline(deadline)
	if _, err := s.w.Write(b); err != nil {
		s.err = err
		return err
	}
	if err := s.rc.Flush(); err != nil {
		s.err = err
		return err
	}
	return nil
}

func (s *sseSink) Start() error {
	if s.err != nil {
		return s.err
	}
	s.w.Header().Set("Content-Type", "text/event-stream")
	s.w.Header().Set("Cache-Control", "no-cache")
	s.w.Header().Set("X-Accel-Buffering", "no")
	if s.started {
		return errors.New("duplicate stream start")
	}
	s.w.WriteHeader(http.StatusOK)
	s.started = true
	return s.frame([]byte(": stream\n\n"))
}

func (s *sseSink) Item(raw json.RawMessage) error {
	if !s.started {
		return errors.New("stream item before start")
	}
	frame := make([]byte, 0, len(raw)+24)
	frame = append(frame, "event: item\ndata: "...)
	frame = append(frame, raw...)
	frame = append(frame, '\n', '\n')
	if err := s.frame(frame); err != nil {
		return err
	}
	s.items++
	s.bytes += int64(len(raw))
	return nil
}

func (s *sseSink) terminal(event string, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	frame := make([]byte, 0, len(body)+32)
	frame = append(frame, "event: "...)
	frame = append(frame, event...)
	frame = append(frame, "\ndata: "...)
	frame = append(frame, body...)
	frame = append(frame, '\n', '\n')
	return s.frame(frame)
}

type appRunResp struct {
	ctx  context.Context
	call func(ctx context.Context) (*application.RunView, error)
}

func (r appRunResp) serve(w http.ResponseWriter) error {
	sink := &sseSink{w: w, rc: http.NewResponseController(w)}
	if d, ok := r.ctx.Deadline(); ok {
		sink.deadline = d
	}
	rv, err := r.call(runtime.WithInvokeStream(r.ctx, sink))
	if !sink.started {
		if err != nil {
			return appFail(err).write(w)
		}
		return rawJSON{http.StatusOK, runDTO(rv)}.write(w)
	}
	if err != nil {
		code, msg := "internal", "request failed"
		var se *service.Error
		if errors.As(err, &se) {
			code, msg = string(se.Code), se.Msg
		}
		payload := gen.ApplicationRunResponse{Result: runtime.ProgramResult{
			Error: &runtime.ProgramError{Code: code, Message: msg},
			ID:    rvID(rv), Streaming: true,
			StreamItems: sink.items, StreamBytes: sink.bytes,
		}}
		return sink.terminal("error", payload)
	}
	payload := runDTO(rv)
	if payload.Result.Error == nil && rv != nil && rv.RunStatus != "" && rv.RunStatus != store.RunSucceeded {
		payload.Result.Error = &runtime.ProgramError{Code: "internal", Message: "run outcome could not be confirmed"}
	}
	if payload.Result.Error != nil {
		if payload.Result.StreamItems == 0 && sink.items > 0 {
			payload.Result.StreamItems = sink.items
			payload.Result.StreamBytes = sink.bytes
		}
		return sink.terminal("error", payload)
	}
	return sink.terminal("done", payload)
}

func rvID(rv *application.RunView) string {
	if rv == nil {
		return ""
	}
	return rv.Result.ID
}

type testApplicationResp struct {
	rawJSON
	deferred *appRunResp
}

func (r testApplicationResp) VisitTestApplicationResponse(w http.ResponseWriter) error {
	if r.deferred != nil {
		return r.deferred.serve(w)
	}
	return r.write(w)
}

type invokeApplicationResp struct {
	rawJSON
	deferred *appRunResp
}

func (r invokeApplicationResp) VisitInvokeApplicationResponse(w http.ResponseWriter) error {
	if r.deferred != nil {
		return r.deferred.serve(w)
	}
	return r.write(w)
}

// --- operations ---

func (s *strictImpl) ListApplications(ctx context.Context, req gen.ListApplicationsRequestObject) (gen.ListApplicationsResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return listApplicationsResp{*e}, nil
	}
	a, err := s.actor(ctx, p, req.Params.Workspace)
	if err != nil {
		return listApplicationsResp{appFail(err)}, nil
	}
	var after string
	if req.Params.After != nil {
		after = *req.Params.After
	}
	var limit int
	if req.Params.Limit != nil {
		limit = int(*req.Params.Limit)
	}
	views, err := s.d.Application.List(ctx, a, after, limit)
	if err != nil {
		return listApplicationsResp{appFail(err)}, nil
	}
	out := make([]gen.Application, 0, len(views))
	for _, v := range views {
		out = append(out, appDTO(v))
	}
	return listApplicationsResp{rawJSON{http.StatusOK, gen.ApplicationsResponse{Applications: &out}}}, nil
}

func (s *strictImpl) CreateApplication(ctx context.Context, req gen.CreateApplicationRequestObject) (gen.CreateApplicationResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return createApplicationResp{*e}, nil
	}
	if req.Body == nil {
		return createApplicationResp{appFail(service.BadRequest("body required"))}, nil
	}
	a, err := s.actor(ctx, p, req.Params.Workspace)
	if err != nil {
		return createApplicationResp{appFail(err)}, nil
	}
	b := req.Body
	var name string
	if b.Name != nil {
		name = *b.Name
	}
	var spec json.RawMessage
	if b.Spec != nil {
		spec = json.RawMessage(*b.Spec)
	}
	v, err := s.d.Application.Create(ctx, a, application.CreateRequest{
		Name: name, Code: b.Code, Spec: spec,
		InputSchema: schemaSide(b.InputSchema), OutputSchema: schemaSide(b.OutputSchema),
	})
	if err != nil {
		return createApplicationResp{appFail(err)}, nil
	}
	d := appDTO(v)
	return createApplicationResp{rawJSON{http.StatusOK, gen.ApplicationResponse{Application: &d}}}, nil
}

func (s *strictImpl) GetApplication(ctx context.Context, req gen.GetApplicationRequestObject) (gen.GetApplicationResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return getApplicationResp{*e}, nil
	}
	v, err := s.d.Application.Get(ctx, actorOf(p), req.Id)
	if err != nil {
		return getApplicationResp{appFail(err)}, nil
	}
	d := appDTO(v)
	return getApplicationResp{rawJSON{http.StatusOK, gen.ApplicationResponse{Application: &d}}}, nil
}

func (s *strictImpl) UpdateApplication(ctx context.Context, req gen.UpdateApplicationRequestObject) (gen.UpdateApplicationResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return updateApplicationResp{*e}, nil
	}
	if req.Body == nil {
		return updateApplicationResp{appFail(service.BadRequest("body required"))}, nil
	}
	b := req.Body
	var spec json.RawMessage
	if b.Spec != nil {
		spec = json.RawMessage(*b.Spec)
	}
	v, err := s.d.Application.Update(ctx, actorOf(p), req.Id, application.UpdateRequest{
		Version: b.Version, Name: b.Name, Code: b.Code, Spec: spec,
		InputSchema: schemaSide(b.InputSchema), OutputSchema: schemaSide(b.OutputSchema),
	})
	if err != nil {
		return updateApplicationResp{appFail(err)}, nil
	}
	d := appDTO(v)
	return updateApplicationResp{rawJSON{http.StatusOK, gen.ApplicationResponse{Application: &d}}}, nil
}

func (s *strictImpl) DeleteApplication(ctx context.Context, req gen.DeleteApplicationRequestObject) (gen.DeleteApplicationResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return deleteApplicationResp{*e}, nil
	}
	if err := s.d.Application.Delete(ctx, actorOf(p), req.Id, req.Params.Version); err != nil {
		return deleteApplicationResp{appFail(err)}, nil
	}
	return deleteApplicationResp{rawJSON{http.StatusNoContent, nil}}, nil
}

// PreviewApplicationPublish returns the reviewed-publish diff — the
// client must read this before publish can commit.
func (s *strictImpl) PreviewApplicationPublish(ctx context.Context, req gen.PreviewApplicationPublishRequestObject) (gen.PreviewApplicationPublishResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return previewApplicationPublishResp{*e}, nil
	}
	if req.Body == nil {
		return previewApplicationPublishResp{appFail(service.BadRequest("body required"))}, nil
	}
	rev, err := s.d.Application.Preview(ctx, actorOf(p), req.Id, req.Body.Version)
	if err != nil {
		return previewApplicationPublishResp{appFail(err)}, nil
	}
	return previewApplicationPublishResp{rawJSON{http.StatusOK, rev}}, nil
}

type previewApplicationPublishResp struct{ rawJSON }

func (r previewApplicationPublishResp) VisitPreviewApplicationPublishResponse(w http.ResponseWriter) error {
	return r.write(w)
}

func (s *strictImpl) PublishApplication(ctx context.Context, req gen.PublishApplicationRequestObject) (gen.PublishApplicationResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return publishApplicationResp{*e}, nil
	}
	if req.Body == nil {
		return publishApplicationResp{appFail(service.BadRequest("body required"))}, nil
	}
	var note string
	if req.Body.Note != nil {
		note = *req.Body.Note
	}
	_, _, err := s.d.Application.PublishReviewed(ctx, actorOf(p), req.Id, application.PublishRequest{
		Version:      req.Body.Version,
		ExpectedBase: req.Body.ExpectedBase,
		Candidate:    req.Body.Candidate,
		Note:         note,
	})
	if err != nil {
		return publishApplicationResp{appFail(err)}, nil
	}
	v, err := s.d.Application.Get(ctx, actorOf(p), req.Id)
	if err != nil {
		return publishApplicationResp{appFail(err)}, nil
	}
	d := appDTO(v)
	return publishApplicationResp{rawJSON{http.StatusAccepted, gen.ApplicationResponse{Application: &d}}}, nil
}

func (s *strictImpl) TestApplication(ctx context.Context, req gen.TestApplicationRequestObject) (gen.TestApplicationResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return testApplicationResp{rawJSON: *e}, nil
	}
	if req.Body == nil {
		return testApplicationResp{rawJSON: appFail(service.BadRequest("body required"))}, nil
	}
	var input json.RawMessage
	if req.Body.Input != nil {
		input = json.RawMessage(*req.Body.Input)
	}
	d := s.d
	return testApplicationResp{deferred: &appRunResp{ctx: ctx, call: func(cctx context.Context) (*application.RunView, error) {
		return d.Application.Test(cctx, actorOf(p), req.Id, req.Body.Version, input)
	}}}, nil
}

func (s *strictImpl) InvokeApplication(ctx context.Context, req gen.InvokeApplicationRequestObject) (gen.InvokeApplicationResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return invokeApplicationResp{rawJSON: *e}, nil
	}
	if req.Body == nil {
		return invokeApplicationResp{rawJSON: appFail(service.BadRequest("body required"))}, nil
	}
	// The raw body IS the input — never re-parsed.
	input := json.RawMessage(*req.Body)
	if len(input) == 0 {
		return invokeApplicationResp{rawJSON: appFail(service.BadRequest("input required"))}, nil
	}
	d := s.d
	return invokeApplicationResp{deferred: &appRunResp{ctx: ctx, call: func(cctx context.Context) (*application.RunView, error) {
		return d.Application.Invoke(cctx, actorOf(p), req.Id, input)
	}}}, nil
}
