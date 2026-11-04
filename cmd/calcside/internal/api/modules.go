package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"calcside/cmd/calcside/internal/api/gen"
	"calcside/cmd/calcside/internal/service"
	modsvc "calcside/cmd/calcside/internal/service/module"
	"calcside/internal/types"
)

// modules.go implements the workspace module library endpoints
// (EP-0017): draft CRUD, source validation, publish preview/publish,
// version history compare and restore. The module service owns all
// domain logic; this file is transport plumbing only.

type listModulesResp struct{ rawJSON }

func (r listModulesResp) VisitListModulesResponse(w http.ResponseWriter) error { return r.write(w) }

type createModuleResp struct{ rawJSON }

func (r createModuleResp) VisitCreateModuleResponse(w http.ResponseWriter) error { return r.write(w) }

type getModuleResp struct{ rawJSON }

func (r getModuleResp) VisitGetModuleResponse(w http.ResponseWriter) error { return r.write(w) }

type updateModuleResp struct{ rawJSON }

func (r updateModuleResp) VisitUpdateModuleResponse(w http.ResponseWriter) error { return r.write(w) }

type deleteModuleResp struct{ rawJSON }

func (r deleteModuleResp) VisitDeleteModuleResponse(w http.ResponseWriter) error { return r.write(w) }

type validateModuleResp struct{ rawJSON }

func (r validateModuleResp) VisitValidateModuleResponse(w http.ResponseWriter) error {
	return r.write(w)
}

type previewModuleResp struct{ rawJSON }

func (r previewModuleResp) VisitPreviewModuleResponse(w http.ResponseWriter) error { return r.write(w) }

type publishModuleResp struct{ rawJSON }

func (r publishModuleResp) VisitPublishModuleResponse(w http.ResponseWriter) error { return r.write(w) }

type listModuleVersionsResp struct{ rawJSON }

func (r listModuleVersionsResp) VisitListModuleVersionsResponse(w http.ResponseWriter) error {
	return r.write(w)
}

type getModuleVersionResp struct{ rawJSON }

func (r getModuleVersionResp) VisitGetModuleVersionResponse(w http.ResponseWriter) error {
	return r.write(w)
}

type compareModuleResp struct{ rawJSON }

func (r compareModuleResp) VisitCompareModuleVersionsResponse(w http.ResponseWriter) error {
	return r.write(w)
}

type restoreModuleResp struct{ rawJSON }

func (r restoreModuleResp) VisitRestoreModuleVersionResponse(w http.ResponseWriter) error {
	return r.write(w)
}

func modJSON(status int, key string, v any) rawJSON {
	return rawJSON{status, map[string]any{key: v}}
}

func (s *strictImpl) ListModules(ctx context.Context, req gen.ListModulesRequestObject) (gen.ListModulesResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return listModulesResp{*e}, nil
	}
	a, err := s.actor(ctx, p, req.Params.Workspace)
	if err != nil {
		return listModulesResp{fail(err)}, nil
	}
	views, err := s.d.Module.List(ctx, a)
	if err != nil {
		return listModulesResp{fail(err)}, nil
	}
	return listModulesResp{modJSON(http.StatusOK, "modules", views)}, nil
}

func (s *strictImpl) CreateModule(ctx context.Context, req gen.CreateModuleRequestObject) (gen.CreateModuleResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return createModuleResp{*e}, nil
	}
	a, err := s.actor(ctx, p, req.Params.Workspace)
	if err != nil {
		return createModuleResp{fail(err)}, nil
	}
	var r modsvc.CreateRequest
	body, _ := json.Marshal(req.Body)
	_ = json.Unmarshal(body, &r)
	v, err := s.d.Module.Create(ctx, a, r)
	if err != nil {
		return createModuleResp{fail(err)}, nil
	}
	return createModuleResp{modJSON(http.StatusOK, "module", v)}, nil
}

func (s *strictImpl) GetModule(ctx context.Context, req gen.GetModuleRequestObject) (gen.GetModuleResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return getModuleResp{*e}, nil
	}
	a, err := s.actor(ctx, p, nil)
	if err != nil {
		return getModuleResp{fail(err)}, nil
	}
	v, err := s.d.Module.Get(ctx, a, req.Id)
	if err != nil {
		return getModuleResp{fail(err)}, nil
	}
	return getModuleResp{modJSON(http.StatusOK, "module", v)}, nil
}

func (s *strictImpl) UpdateModule(ctx context.Context, req gen.UpdateModuleRequestObject) (gen.UpdateModuleResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return updateModuleResp{*e}, nil
	}
	a, err := s.actor(ctx, p, nil)
	if err != nil {
		return updateModuleResp{fail(err)}, nil
	}
	var r modsvc.UpdateRequest
	body, _ := json.Marshal(req.Body)
	_ = json.Unmarshal(body, &r)
	v, err := s.d.Module.Update(ctx, a, req.Id, r)
	if err != nil {
		return updateModuleResp{fail(err)}, nil
	}
	return updateModuleResp{modJSON(http.StatusOK, "module", v)}, nil
}

func (s *strictImpl) DeleteModule(ctx context.Context, req gen.DeleteModuleRequestObject) (gen.DeleteModuleResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return deleteModuleResp{*e}, nil
	}
	a, err := s.actor(ctx, p, nil)
	if err != nil {
		return deleteModuleResp{fail(err)}, nil
	}
	if err := s.d.Module.Delete(ctx, a, req.Id); err != nil {
		return deleteModuleResp{fail(err)}, nil
	}
	return deleteModuleResp{rawJSON{http.StatusNoContent, nil}}, nil
}

func (s *strictImpl) ValidateModule(ctx context.Context, req gen.ValidateModuleRequestObject) (gen.ValidateModuleResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return validateModuleResp{*e}, nil
	}
	a, err := s.actor(ctx, p, req.Params.Workspace)
	if err != nil {
		return validateModuleResp{fail(err)}, nil
	}
	var r modsvc.CreateRequest
	body, _ := json.Marshal(req.Body)
	_ = json.Unmarshal(body, &r)
	if err := s.d.Module.Validate(ctx, a, r.Files); err != nil {
		// Only spec-validation failures answer 200 valid:false;
		// authorization and internal failures propagate as errors.
		var se *service.Error
		if !errors.As(err, &se) || se.Code != types.ErrCodeBadSpec {
			return validateModuleResp{fail(err)}, nil
		}
		return validateModuleResp{rawJSON{http.StatusOK,
			map[string]any{"valid": false, "error": err.Error()}}}, nil
	}
	return validateModuleResp{rawJSON{http.StatusOK, map[string]any{"valid": true}}}, nil
}

func (s *strictImpl) PreviewModule(ctx context.Context, req gen.PreviewModuleRequestObject) (gen.PreviewModuleResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return previewModuleResp{*e}, nil
	}
	a, err := s.actor(ctx, p, nil)
	if err != nil {
		return previewModuleResp{fail(err)}, nil
	}
	var body struct {
		DraftVersion int64 `json:"draft_version"`
	}
	b, _ := json.Marshal(req.Body)
	_ = json.Unmarshal(b, &body)
	rev, err := s.d.Module.Preview(ctx, a, req.Id, body.DraftVersion)
	if err != nil {
		return previewModuleResp{fail(err)}, nil
	}
	return previewModuleResp{rawJSON{http.StatusOK, rev}}, nil
}

func (s *strictImpl) PublishModule(ctx context.Context, req gen.PublishModuleRequestObject) (gen.PublishModuleResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return publishModuleResp{*e}, nil
	}
	a, err := s.actor(ctx, p, nil)
	if err != nil {
		return publishModuleResp{fail(err)}, nil
	}
	var r modsvc.PublishRequest
	body, _ := json.Marshal(req.Body)
	_ = json.Unmarshal(body, &r)
	v, err := s.d.Module.Publish(ctx, a, req.Id, r)
	if err != nil {
		return publishModuleResp{fail(err)}, nil
	}
	return publishModuleResp{modJSON(http.StatusOK, "version", v)}, nil
}

func (s *strictImpl) ListModuleVersions(ctx context.Context, req gen.ListModuleVersionsRequestObject) (gen.ListModuleVersionsResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return listModuleVersionsResp{*e}, nil
	}
	a, err := s.actor(ctx, p, nil)
	if err != nil {
		return listModuleVersionsResp{fail(err)}, nil
	}
	vers, err := s.d.Module.Versions(ctx, a, req.Id)
	if err != nil {
		return listModuleVersionsResp{fail(err)}, nil
	}
	return listModuleVersionsResp{modJSON(http.StatusOK, "versions", vers)}, nil
}

func (s *strictImpl) GetModuleVersion(ctx context.Context, req gen.GetModuleVersionRequestObject) (gen.GetModuleVersionResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return getModuleVersionResp{*e}, nil
	}
	a, err := s.actor(ctx, p, nil)
	if err != nil {
		return getModuleVersionResp{fail(err)}, nil
	}
	v, err := s.d.Module.Version(ctx, a, req.Id, req.Version)
	if err != nil {
		return getModuleVersionResp{fail(err)}, nil
	}
	return getModuleVersionResp{modJSON(http.StatusOK, "version", v)}, nil
}

func (s *strictImpl) CompareModuleVersions(ctx context.Context, req gen.CompareModuleVersionsRequestObject) (gen.CompareModuleVersionsResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return compareModuleResp{*e}, nil
	}
	a, err := s.actor(ctx, p, nil)
	if err != nil {
		return compareModuleResp{fail(err)}, nil
	}
	rev, err := s.d.Module.Compare(ctx, a, req.Id, req.Params.Base, req.Params.Head)
	if err != nil {
		return compareModuleResp{fail(err)}, nil
	}
	return compareModuleResp{rawJSON{http.StatusOK, rev}}, nil
}

func (s *strictImpl) RestoreModuleVersion(ctx context.Context, req gen.RestoreModuleVersionRequestObject) (gen.RestoreModuleVersionResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return restoreModuleResp{*e}, nil
	}
	a, err := s.actor(ctx, p, nil)
	if err != nil {
		return restoreModuleResp{fail(err)}, nil
	}
	var body struct {
		DraftVersion int64 `json:"draft_version"`
	}
	b, _ := json.Marshal(req.Body)
	_ = json.Unmarshal(b, &body)
	v, err := s.d.Module.Restore(ctx, a, req.Id, req.Version, body.DraftVersion)
	if err != nil {
		return restoreModuleResp{fail(err)}, nil
	}
	return restoreModuleResp{modJSON(http.StatusOK, "module", v)}, nil
}

type getModuleDebugResp struct{ rawJSON }

func (r getModuleDebugResp) VisitGetModuleDebugResponse(w http.ResponseWriter) error {
	return r.write(w)
}

type saveModuleDebugResp struct{ rawJSON }

func (r saveModuleDebugResp) VisitSaveModuleDebugResponse(w http.ResponseWriter) error {
	return r.write(w)
}

type runModuleDebugResp struct{ rawJSON }

func (r runModuleDebugResp) VisitRunModuleDebugResponse(w http.ResponseWriter) error {
	return r.write(w)
}

func (s *strictImpl) GetModuleDebug(ctx context.Context, req gen.GetModuleDebugRequestObject) (gen.GetModuleDebugResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return getModuleDebugResp{*e}, nil
	}
	a, err := s.actor(ctx, p, nil)
	if err != nil {
		return getModuleDebugResp{fail(err)}, nil
	}
	v, err := s.d.Module.GetDebug(ctx, a, req.Id)
	if err != nil {
		return getModuleDebugResp{fail(err)}, nil
	}
	return getModuleDebugResp{modJSON(http.StatusOK, "debug", v)}, nil
}

func (s *strictImpl) SaveModuleDebug(ctx context.Context, req gen.SaveModuleDebugRequestObject) (gen.SaveModuleDebugResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return saveModuleDebugResp{*e}, nil
	}
	a, err := s.actor(ctx, p, nil)
	if err != nil {
		return saveModuleDebugResp{fail(err)}, nil
	}
	// Required-pointer schema fields: omitted/null arrive nil -> 400.
	if req.Body == nil || req.Body.Version == nil || req.Body.Script == nil || req.Body.Spec == nil {
		return saveModuleDebugResp{rawJSON{http.StatusBadRequest,
			errEnv(types.ErrCodeBadRequest, "version, script and spec are required")}}, nil
	}
	r := modsvc.SaveDebugRequest{
		Version: *req.Body.Version, Script: *req.Body.Script, Spec: *req.Body.Spec,
	}
	v, err := s.d.Module.SaveDebug(ctx, a, req.Id, r)
	if err != nil {
		return saveModuleDebugResp{fail(err)}, nil
	}
	return saveModuleDebugResp{modJSON(http.StatusOK, "debug", v)}, nil
}

func (s *strictImpl) RunModuleDebug(ctx context.Context, req gen.RunModuleDebugRequestObject) (gen.RunModuleDebugResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return runModuleDebugResp{*e}, nil
	}
	a, err := s.actor(ctx, p, nil)
	if err != nil {
		return runModuleDebugResp{fail(err)}, nil
	}
	if req.Body == nil || req.Body.DraftVersion == nil || req.Body.DebugVersion == nil {
		return runModuleDebugResp{rawJSON{http.StatusBadRequest,
			errEnv(types.ErrCodeBadRequest, "draft_version and debug_version are required")}}, nil
	}
	r := modsvc.RunDebugRequest{
		DraftVersion: *req.Body.DraftVersion, DebugVersion: *req.Body.DebugVersion,
	}
	v, err := s.d.Module.RunDebug(ctx, a, req.Id, r)
	if err != nil {
		return runModuleDebugResp{fail(err)}, nil
	}
	return runModuleDebugResp{rawJSON{http.StatusOK, v}}, nil
}
