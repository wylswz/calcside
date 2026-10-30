package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"calcside/cmd/calcside/internal/api/gen"
	"calcside/cmd/calcside/internal/service"
	"calcside/cmd/calcside/internal/service/application"
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
	if len(a.Handlers) > 0 {
		s := string(a.Handlers)
		d.Handlers = &s
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
			InterruptId: w.InterruptID, Kind: w.Kind, ConsoleUrl: w.ConsoleURL,
			Title: optStr(w.Title),
		}
	}
	return out
}

// handlersRaw carries the declarations document byte-for-byte like
// input_schema (a raw JSON string field), preserving big integers.
func handlersRaw(h *string) json.RawMessage {
	if h == nil || *h == "" {
		return nil
	}
	return json.RawMessage(*h)
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

type testApplicationResp struct{ rawJSON }

func (r testApplicationResp) VisitTestApplicationResponse(w http.ResponseWriter) error {
	return r.write(w)
}

type invokeApplicationResp struct{ rawJSON }

func (r invokeApplicationResp) VisitInvokeApplicationResponse(w http.ResponseWriter) error {
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
		Handlers: handlersRaw(b.Handlers),
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

func (s *strictImpl) PublishApplication(ctx context.Context, req gen.PublishApplicationRequestObject) (gen.PublishApplicationResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return publishApplicationResp{*e}, nil
	}
	if req.Body == nil {
		return publishApplicationResp{appFail(service.BadRequest("body required"))}, nil
	}
	v, err := s.d.Application.Publish(ctx, actorOf(p), req.Id, req.Body.Version)
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
		return testApplicationResp{*e}, nil
	}
	if req.Body == nil {
		return testApplicationResp{appFail(service.BadRequest("body required"))}, nil
	}
	var input json.RawMessage
	if req.Body.Input != nil {
		input = json.RawMessage(*req.Body.Input)
	}
	rv, err := s.d.Application.Test(ctx, actorOf(p), req.Id, req.Body.Version, input)
	if err != nil {
		return testApplicationResp{appFail(err)}, nil
	}
	return testApplicationResp{rawJSON{http.StatusOK, runDTO(rv)}}, nil
}

func (s *strictImpl) InvokeApplication(ctx context.Context, req gen.InvokeApplicationRequestObject) (gen.InvokeApplicationResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return invokeApplicationResp{*e}, nil
	}
	if req.Body == nil {
		return invokeApplicationResp{appFail(service.BadRequest("body required"))}, nil
	}
	// The raw body IS the input — never re-parsed.
	input := json.RawMessage(*req.Body)
	if len(input) == 0 {
		return invokeApplicationResp{appFail(service.BadRequest("input required"))}, nil
	}
	rv, err := s.d.Application.Invoke(ctx, actorOf(p), req.Id, input)
	if err != nil {
		return invokeApplicationResp{appFail(err)}, nil
	}
	return invokeApplicationResp{rawJSON{http.StatusOK, runDTO(rv)}}, nil
}
