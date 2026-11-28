package api

import (
	"context"
	"encoding/json"
	"net/http"

	"calcside/cmd/calcside/internal/api/gen"
	"calcside/cmd/calcside/internal/service"
	"calcside/cmd/calcside/internal/service/application"
	"calcside/internal/store"
)

func testCaseInputFromBody(b *gen.ApplicationTestCaseRequest) application.TestCaseInput {
	in := application.TestCaseInput{
		Name: b.Name,
	}
	if b.Description != nil {
		in.Description = *b.Description
	}
	if b.Input != nil {
		in.Input = json.RawMessage(*b.Input)
	}
	if b.ExpectStatus != nil {
		in.ExpectStatus = string(*b.ExpectStatus)
	}
	if b.ExpectedOutput != nil {
		in.ExpectedOutput = json.RawMessage(*b.ExpectedOutput)
	}
	if b.OutputMatchMode != nil {
		in.OutputMatchMode = string(*b.OutputMatchMode)
	}
	if b.ExpectedErrorCode != nil {
		in.ExpectedErrorCode = *b.ExpectedErrorCode
	}
	if b.Interrupts != nil {
		intrs := make([]store.ApplicationTestInterrupt, 0, len(*b.Interrupts))
		for _, it := range *b.Interrupts {
			s := store.ApplicationTestInterrupt{}
			if it.ExpectHandler != nil {
				s.ExpectHandler = *it.ExpectHandler
			}
			if it.ExpectContext != nil {
				s.ExpectContext = json.RawMessage(*it.ExpectContext)
			}
			if it.ContextMatchMode != nil {
				s.ContextMatchMode = string(*it.ContextMatchMode)
			}
			if it.Resolution != nil {
				s.Resolution = string(*it.Resolution)
			}
			if it.Event != nil {
				s.Event = json.RawMessage(*it.Event)
			}
			intrs = append(intrs, s)
		}
		in.Interrupts = intrs
	}
	return in
}

type listApplicationTestCasesResp struct{ rawJSON }

func (r listApplicationTestCasesResp) VisitListApplicationTestCasesResponse(w http.ResponseWriter) error {
	return r.write(w)
}

func (s *strictImpl) ListApplicationTestCases(ctx context.Context, req gen.ListApplicationTestCasesRequestObject) (gen.ListApplicationTestCasesResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return listApplicationTestCasesResp{*e}, nil
	}
	cases, latestRun, err := s.d.Application.ListTestCases(ctx, actorOf(p), req.Id)
	if err != nil {
		return listApplicationTestCasesResp{appFail(err)}, nil
	}
	body := map[string]any{
		"test_cases": cases,
	}
	if latestRun != nil {
		body["latest_run"] = latestRun
	}
	return listApplicationTestCasesResp{rawJSON{http.StatusOK, body}}, nil
}

type createApplicationTestCaseResp struct{ rawJSON }

func (r createApplicationTestCaseResp) VisitCreateApplicationTestCaseResponse(w http.ResponseWriter) error {
	return r.write(w)
}

func (s *strictImpl) CreateApplicationTestCase(ctx context.Context, req gen.CreateApplicationTestCaseRequestObject) (gen.CreateApplicationTestCaseResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return createApplicationTestCaseResp{*e}, nil
	}
	if req.Body == nil {
		return createApplicationTestCaseResp{appFail(service.BadRequest("body required"))}, nil
	}
	tc, err := s.d.Application.CreateTestCase(ctx, actorOf(p), req.Id, testCaseInputFromBody(req.Body))
	if err != nil {
		return createApplicationTestCaseResp{appFail(err)}, nil
	}
	return createApplicationTestCaseResp{rawJSON{http.StatusCreated, map[string]any{"test_case": tc}}}, nil
}

type updateApplicationTestCaseResp struct{ rawJSON }

func (r updateApplicationTestCaseResp) VisitUpdateApplicationTestCaseResponse(w http.ResponseWriter) error {
	return r.write(w)
}

func (s *strictImpl) UpdateApplicationTestCase(ctx context.Context, req gen.UpdateApplicationTestCaseRequestObject) (gen.UpdateApplicationTestCaseResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return updateApplicationTestCaseResp{*e}, nil
	}
	if req.Body == nil {
		return updateApplicationTestCaseResp{appFail(service.BadRequest("body required"))}, nil
	}
	tc, err := s.d.Application.UpdateTestCase(ctx, actorOf(p), req.Id, req.CaseId, testCaseInputFromBody(req.Body))
	if err != nil {
		return updateApplicationTestCaseResp{appFail(err)}, nil
	}
	return updateApplicationTestCaseResp{rawJSON{http.StatusOK, map[string]any{"test_case": tc}}}, nil
}

type deleteApplicationTestCaseResp struct{ rawJSON }

func (r deleteApplicationTestCaseResp) VisitDeleteApplicationTestCaseResponse(w http.ResponseWriter) error {
	return r.write(w)
}

func (s *strictImpl) DeleteApplicationTestCase(ctx context.Context, req gen.DeleteApplicationTestCaseRequestObject) (gen.DeleteApplicationTestCaseResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return deleteApplicationTestCaseResp{*e}, nil
	}
	if err := s.d.Application.DeleteTestCase(ctx, actorOf(p), req.Id, req.CaseId); err != nil {
		return deleteApplicationTestCaseResp{appFail(err)}, nil
	}
	return deleteApplicationTestCaseResp{rawJSON{http.StatusOK, map[string]bool{"ok": true}}}, nil
}

type runApplicationTestCasesResp struct{ rawJSON }

func (r runApplicationTestCasesResp) VisitRunApplicationTestCasesResponse(w http.ResponseWriter) error {
	return r.write(w)
}

func (s *strictImpl) RunApplicationTestCases(ctx context.Context, req gen.RunApplicationTestCasesRequestObject) (gen.RunApplicationTestCasesResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return runApplicationTestCasesResp{*e}, nil
	}
	if req.Body == nil {
		return runApplicationTestCasesResp{appFail(service.BadRequest("body required"))}, nil
	}
	var caseIDs []string
	if req.Body.CaseIds != nil {
		caseIDs = *req.Body.CaseIds
	}
	suite, err := s.d.Application.RunTestCases(ctx, actorOf(p), req.Id, req.Body.Version, caseIDs)
	if err != nil {
		return runApplicationTestCasesResp{appFail(err)}, nil
	}
	return runApplicationTestCasesResp{rawJSON{http.StatusOK, suite}}, nil
}
