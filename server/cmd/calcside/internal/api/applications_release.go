package api

import (
	"context"
	"net/http"

	"calcside/cmd/calcside/internal/api/gen"
	"calcside/cmd/calcside/internal/service"
)

type checkApplicationReleaseResp struct{ rawJSON }

func (r checkApplicationReleaseResp) VisitCheckApplicationReleaseResponse(w http.ResponseWriter) error {
	return r.write(w)
}

func (s *strictImpl) CheckApplicationRelease(ctx context.Context, req gen.CheckApplicationReleaseRequestObject) (gen.CheckApplicationReleaseResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return checkApplicationReleaseResp{*e}, nil
	}
	if req.Body == nil {
		return checkApplicationReleaseResp{appFail(service.BadRequest("body required"))}, nil
	}
	res, err := s.d.Application.CheckRelease(ctx, actorOf(p), req.Id, req.Body.Version)
	if err != nil {
		return checkApplicationReleaseResp{appFail(err)}, nil
	}
	findings := make([]gen.ReleaseFinding, 0, len(res.Findings))
	for _, f := range res.Findings {
		gf := gen.ReleaseFinding{
			Code:     f.Code,
			Severity: gen.ReleaseFindingSeverity(f.Severity),
			Category: f.Category,
			Message:  f.Message,
		}
		if f.Section != "" {
			sec := f.Section
			gf.Section = &sec
		}
		if f.Fix != "" {
			fix := f.Fix
			gf.Fix = &fix
		}
		findings = append(findings, gf)
	}
	dto := map[string]any{
		"has_drift":       res.HasDrift,
		"status":          res.Status,
		"findings":        findings,
		"test_case_count": res.TestCaseCount,
	}
	if res.Candidate != "" {
		dto["candidate"] = res.Candidate
	}
	if res.LiveDigest != "" {
		dto["live_digest"] = res.LiveDigest
	}
	if res.LiveSeq > 0 {
		dto["live_seq"] = res.LiveSeq
	}
	if res.LatestTestRun != nil {
		dto["latest_test_run"] = res.LatestTestRun
	}
	return checkApplicationReleaseResp{rawJSON{http.StatusOK, dto}}, nil
}

type listApplicationReleasesResp struct{ rawJSON }

func (r listApplicationReleasesResp) VisitListApplicationReleasesResponse(w http.ResponseWriter) error {
	return r.write(w)
}

func (s *strictImpl) ListApplicationReleases(ctx context.Context, req gen.ListApplicationReleasesRequestObject) (gen.ListApplicationReleasesResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return listApplicationReleasesResp{*e}, nil
	}
	hv, err := s.d.Application.Releases(ctx, actorOf(p), req.Id)
	if err != nil {
		return listApplicationReleasesResp{appFail(err)}, nil
	}
	entries := make([]gen.ApplicationReleaseEntry, 0, len(hv.Releases))
	for _, r := range hv.Releases {
		entries = append(entries, gen.ApplicationReleaseEntry{
			Seq:           r.Seq,
			ReleaseId:     r.ReleaseID,
			DraftVersion:  r.DraftVersion,
			PublishedBy:   r.PublishedBy,
			Note:          r.Note,
			RollbackOfSeq: r.RollbackOfSeq,
			IsLive:        r.IsLive,
			PublishedAt:   r.PublishedAt,
		})
	}
	dto := gen.ApplicationReleaseListResponse{
		Status:   gen.ApplicationStatus(hv.Status),
		Releases: entries,
	}
	if hv.LiveReleaseID != "" {
		lr := hv.LiveReleaseID
		dto.LiveReleaseId = &lr
	}
	if hv.LiveSeq > 0 {
		ls := hv.LiveSeq
		dto.LiveSeq = &ls
	}
	return listApplicationReleasesResp{rawJSON{http.StatusOK, dto}}, nil
}

type compareApplicationReleasesResp struct{ rawJSON }

func (r compareApplicationReleasesResp) VisitCompareApplicationReleasesResponse(w http.ResponseWriter) error {
	return r.write(w)
}

func (s *strictImpl) CompareApplicationReleases(ctx context.Context, req gen.CompareApplicationReleasesRequestObject) (gen.CompareApplicationReleasesResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return compareApplicationReleasesResp{*e}, nil
	}
	rev, err := s.d.Application.CompareReleases(ctx, actorOf(p), req.Id, req.Params.Base, req.Params.Head)
	if err != nil {
		return compareApplicationReleasesResp{appFail(err)}, nil
	}
	return compareApplicationReleasesResp{rawJSON{http.StatusOK, rev}}, nil
}

type restoreApplicationReleaseResp struct{ rawJSON }

func (r restoreApplicationReleaseResp) VisitRestoreApplicationReleaseResponse(w http.ResponseWriter) error {
	return r.write(w)
}

func (s *strictImpl) RestoreApplicationRelease(ctx context.Context, req gen.RestoreApplicationReleaseRequestObject) (gen.RestoreApplicationReleaseResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return restoreApplicationReleaseResp{*e}, nil
	}
	if req.Body == nil {
		return restoreApplicationReleaseResp{appFail(service.BadRequest("body required"))}, nil
	}
	v, err := s.d.Application.RestoreRelease(ctx, actorOf(p), req.Id, req.Seq, req.Body.Version)
	if err != nil {
		return restoreApplicationReleaseResp{appFail(err)}, nil
	}
	d := appDetailDTO(v)
	return restoreApplicationReleaseResp{rawJSON{http.StatusOK, gen.ApplicationResponse{Application: &d}}}, nil
}

type rollbackApplicationReleaseResp struct{ rawJSON }

func (r rollbackApplicationReleaseResp) VisitRollbackApplicationReleaseResponse(w http.ResponseWriter) error {
	return r.write(w)
}

func (s *strictImpl) RollbackApplicationRelease(ctx context.Context, req gen.RollbackApplicationReleaseRequestObject) (gen.RollbackApplicationReleaseResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return rollbackApplicationReleaseResp{*e}, nil
	}
	if req.Body == nil {
		return rollbackApplicationReleaseResp{appFail(service.BadRequest("body required"))}, nil
	}
	var note string
	if req.Body.Note != nil {
		note = *req.Body.Note
	}
	v, _, err := s.d.Application.RollbackRelease(ctx, actorOf(p), req.Id, req.Seq, req.Body.ExpectedBase, note)
	if err != nil {
		return rollbackApplicationReleaseResp{appFail(err)}, nil
	}
	d := appDTO(v)
	return rollbackApplicationReleaseResp{rawJSON{http.StatusAccepted, gen.ApplicationResponse{Application: &d}}}, nil
}
