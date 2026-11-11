package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"

	"calcside/cmd/calcside/internal/api/dto"
	"calcside/cmd/calcside/internal/api/gen"
	"calcside/cmd/calcside/internal/service"
	"calcside/cmd/calcside/internal/service/sandbox"
)

// EP-0022 notebook endpoints: outline, section CRUD, and batch runs.
// Reads are View-level and never restore a lost instance; writes and
// runs are Run-level and may trigger restoration (typed
// instance_restored, never silent execution).

type getNotebookResp struct{ rawJSON }

func (r getNotebookResp) VisitGetNotebookResponse(w http.ResponseWriter) error {
	return r.write(w)
}

type runNotebookSectionsResp struct{ rawJSON }

func (r runNotebookSectionsResp) VisitRunNotebookSectionsResponse(w http.ResponseWriter) error {
	return r.write(w)
}

type readNotebookSectionResp struct{ rawJSON }

func (r readNotebookSectionResp) VisitReadNotebookSectionResponse(w http.ResponseWriter) error {
	return r.write(w)
}

type saveNotebookSectionResp struct{ rawJSON }

func (r saveNotebookSectionResp) VisitSaveNotebookSectionResponse(w http.ResponseWriter) error {
	return r.write(w)
}

type deleteNotebookSectionResp struct{ rawJSON }

func (r deleteNotebookSectionResp) VisitDeleteNotebookSectionResponse(w http.ResponseWriter) error {
	return r.write(w)
}

// GetNotebook renders the outline: sections metadata, states, and
// next-step guidance. Works on running, lost, expired and deleted
// instances — read-only, never restores.
func (s *strictImpl) GetNotebook(ctx context.Context, req gen.GetNotebookRequestObject) (gen.GetNotebookResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return getNotebookResp{*e}, nil
	}
	v, err := s.d.Sandbox.Notebook(ctx, actorOf(p), req.Id)
	if err != nil {
		return getNotebookResp{fail(err)}, nil
	}
	return getNotebookResp{rawJSON{200, dto.NotebookEnvelope{Notebook: dto.NewNotebook(v)}}}, nil
}

// ReadNotebookSection returns one section's code and metadata;
// include_output adds the last run's output/error.
func (s *strictImpl) ReadNotebookSection(ctx context.Context, req gen.ReadNotebookSectionRequestObject) (gen.ReadNotebookSectionResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return readNotebookSectionResp{*e}, nil
	}
	include := req.Params.IncludeOutput != nil && *req.Params.IncludeOutput
	d, err := s.d.Sandbox.ReadSection(ctx, actorOf(p), req.Id, req.Name, include)
	if err != nil {
		return readNotebookSectionResp{fail(err)}, nil
	}
	return readNotebookSectionResp{rawJSON{200, dto.NotebookSectionEnvelope{Section: dto.NewNotebookSection(d)}}}, nil
}

// SaveNotebookSection edits a section without running it; revision is
// the notebook CAS and is required.
func (s *strictImpl) SaveNotebookSection(ctx context.Context, req gen.SaveNotebookSectionRequestObject) (gen.SaveNotebookSectionResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return saveNotebookSectionResp{*e}, nil
	}
	if req.Body == nil {
		return saveNotebookSectionResp{fail(service.BadRequest("missing body"))}, nil
	}
	b := req.Body
	if b.Revision == nil {
		return saveNotebookSectionResp{fail(service.BadRequest("revision is required"))}, nil
	}
	sreq := sandbox.SaveSectionRequest{Revision: *b.Revision}
	if b.Code != nil {
		sreq.Code = b.Code
	}
	if b.Description != nil {
		sreq.Description = b.Description
	}
	if b.Rerun != nil {
		r := string(*b.Rerun)
		sreq.Rerun = &r
	}
	if b.After != nil {
		sreq.After = b.After
	}
	if b.FromExec != nil {
		sreq.FromExec = *b.FromExec
	}
	if b.CreateOnly != nil {
		sreq.CreateOnly = *b.CreateOnly
	}
	res, err := s.d.Sandbox.SaveSection(ctx, actorOf(p), req.Id, req.Name, sreq)
	if err != nil {
		return saveNotebookSectionResp{fail(err)}, nil
	}
	// The response carries the section's derived state, so re-read the
	// outline and pick the row.
	meta := sandbox.NotebookSectionMeta{Name: res.Section.Name, Position: res.Section.Position,
		Description: res.Section.Description, Rerun: res.Section.Rerun,
		Defines: res.Section.Defines, Uses: res.Section.Uses,
		Effects: res.Section.Effects, DependsOn: res.Section.DependsOn,
		LastExecID: res.Section.LastExecID, LastIncarnation: res.Section.LastIncarnation,
		LastStatus: res.Section.LastStatus,
		LastRunAt:  res.Section.LastRunAt, OnceRan: res.Section.SucceededBefore}
	if nb, nerr := s.d.Sandbox.Notebook(ctx, actorOf(p), req.Id); nerr == nil {
		for _, m := range nb.Sections {
			if m.Name == req.Name {
				meta = m
				break
			}
		}
	}
	return saveNotebookSectionResp{rawJSON{200, dto.NewSaveSectionResult(meta, res.Revision)}}, nil
}

// DeleteNotebookSection removes a section under revision CAS.
func (s *strictImpl) DeleteNotebookSection(ctx context.Context, req gen.DeleteNotebookSectionRequestObject) (gen.DeleteNotebookSectionResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return deleteNotebookSectionResp{*e}, nil
	}
	res, err := s.d.Sandbox.DeleteSection(ctx, actorOf(p), req.Id, req.Name, req.Params.Revision)
	if err != nil {
		return deleteNotebookSectionResp{fail(err)}, nil
	}
	return deleteNotebookSectionResp{rawJSON{200, dto.DeleteSectionResult{Affected: res.Affected, Revision: res.Revision}}}, nil
}

// RunNotebookSections executes "all", "stale", a name, or a list of
// names in notebook order, stopping at the first script failure.
func (s *strictImpl) RunNotebookSections(ctx context.Context, req gen.RunNotebookSectionsRequestObject) (gen.RunNotebookSectionsResponseObject, error) {
	ctx = realCtx(ctx)
	p, e := needAuth(ctx)
	if e != nil {
		return runNotebookSectionsResp{*e}, nil
	}
	if req.Body == nil {
		return runNotebookSectionsResp{fail(service.BadRequest("missing body"))}, nil
	}
	sel, err := sectionSelector(req.Body.Sections)
	if err != nil {
		return runNotebookSectionsResp{fail(err)}, nil
	}
	report, err := s.d.Sandbox.RunSections(ctx, actorOf(p), req.Id, sel, req.Body.Revision)
	if err != nil {
		return runNotebookSectionsResp{fail(err)}, nil
	}
	return runNotebookSectionsResp{rawJSON{200, dto.NewRunSectionsEnvelope(report)}}, nil
}

// sectionSelector normalizes the oneOf sections field into the
// service selector. The generated union cannot discriminate a bare
// string (its enum variant accepts any string), so the raw JSON is
// parsed here: an array is an explicit name list, a string is "all",
// "stale", or a single section name — anything else is rejected. A
// section literally named "all"/"stale" is still selectable via the
// array form.
func sectionSelector(u gen.RunSectionsRequest_Sections) (sandbox.SectionSelector, error) {
	raw, err := u.MarshalJSON()
	if err != nil {
		return sandbox.SectionSelector{}, service.BadRequest("sections must be \"all\", \"stale\", a name, or a list of names")
	}
	trim := bytes.TrimSpace(raw)
	switch {
	case len(trim) > 0 && trim[0] == '[':
		var names []string
		if err := json.Unmarshal(trim, &names); err != nil || len(names) == 0 {
			return sandbox.SectionSelector{}, service.BadRequest("sections must be \"all\", \"stale\", a name, or a non-empty list of names")
		}
		return sandbox.SectionSelector{Names: names}, nil
	case len(trim) > 0 && trim[0] == '"':
		var s string
		if err := json.Unmarshal(trim, &s); err != nil {
			return sandbox.SectionSelector{}, service.BadRequest("sections must be \"all\", \"stale\", a name, or a list of names")
		}
		switch s {
		case "all":
			return sandbox.SectionSelector{All: true}, nil
		case "stale":
			return sandbox.SectionSelector{Stale: true}, nil
		default:
			return sandbox.SectionSelector{Names: []string{s}}, nil
		}
	default:
		return sandbox.SectionSelector{}, service.BadRequest("sections must be \"all\", \"stale\", a name, or a list of names")
	}
}
