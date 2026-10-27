// Package application manages durable named-function drafts and their
// publish/test/invoke lifecycle (EP-0014). Authorization always runs
// before any mutation; secrets resolve in memory only.
package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"calcside/cmd/calcside/internal/service"
	"calcside/cmd/calcside/internal/service/access"
	"calcside/cmd/calcside/internal/service/sandbox"
	"calcside/internal/runtime"
	"calcside/internal/store"
	"calcside/internal/types"
)

const scaffold = "def main(input):\n    # Accept a JSON object and return a JSON object.\n    output = {\"message\": \"Hello, \" + str(input.get(\"name\", \"world\"))}\n    return output\n"

type Options struct {
	Store      store.Store
	Runtime    runtime.Runtime
	Access     *access.Checker
	Sandbox    *sandbox.Service
	Audit      sandbox.AuditSink
	MaxPerUser int
	Now        func() time.Time
}

type Service struct {
	o Options
	// testSlots bounds concurrent ephemeral Test admissions.
	testSlots chan struct{}
}

func New(o Options) (*Service, error) {
	if o.Store == nil || o.Access == nil || o.Sandbox == nil || o.Runtime == nil {
		return nil, errors.New("application: missing dependency")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Audit == nil {
		o.Audit = nopAudit{}
	}
	return &Service{o: o, testSlots: make(chan struct{}, 8)}, nil
}

type nopAudit struct{}

func (nopAudit) Record(runtime.AuditBatch) {}

type View struct {
	Application           *store.Application `json:"application"`
	Status                string             `json:"status"`
	ReleaseID             string             `json:"release_id,omitempty"`
	ErrorCode             string             `json:"error_code,omitempty"`
	HasUnpublishedChanges bool               `json:"has_unpublished_changes"`
}

type RunView struct {
	Result     runtime.ProgramResult `json:"result"`
	OutputJSON string                `json:"output_json,omitempty"`
}

type CreateRequest struct {
	Name         string
	Code         *string
	Spec         json.RawMessage
	InputSchema  json.RawMessage
	OutputSchema json.RawMessage
}

type UpdateRequest struct {
	Version      int64
	Name         string
	Code         string
	Spec         json.RawMessage
	InputSchema  json.RawMessage
	OutputSchema json.RawMessage
}

func specOr(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	return raw
}

// boundary normalizes + validates the spec through the sandbox so
// drafts store refs-only normalized settings (policies preserved).
func (s *Service) boundary(ctx context.Context, a service.Actor, appWS string, raw json.RawMessage) (json.RawMessage, runtime.PolicyBundle, []runtime.Secret, error) {
	a.WorkspaceID = appWS
	spec, policies, secrets, err := s.o.Sandbox.ApplicationBoundary(ctx, a, raw)
	if err != nil {
		return nil, runtime.PolicyBundle{}, nil, fail(err)
	}
	return spec, policies, secrets, nil
}

func (s *Service) Create(ctx context.Context, a service.Actor, r CreateRequest) (*View, error) {
	if err := s.o.Access.Workspace(ctx, a, a.WorkspaceID, access.Configure); err != nil {
		return nil, err
	}
	code := scaffold
	if r.Code != nil {
		code = *r.Code
	}
	name := strings.TrimSpace(r.Name)
	if name == "" {
		name = "Untitled application"
	}
	// Normalize + validate the spec before persistence: refs only,
	// ext explicitly rejected, policies preserved.
	spec, _, _, err := s.boundary(ctx, a, a.WorkspaceID, specOr(r.Spec))
	if err != nil {
		return nil, fail(err)
	}
	app, err := store.NewApplication(a.WorkspaceID, a.UserID, name, code, spec, r.InputSchema, r.OutputSchema, s.o.Now())
	if err != nil {
		return nil, fail(err)
	}
	// Quota + create must be atomic under the workspace lock.
	var quotaErr error
	err = s.o.Store.Tx(ctx, func(tx store.Store) error {
		if err := tx.LockWorkspace(ctx, a.WorkspaceID); err != nil {
			return err
		}
		n, err := tx.CountApplications(ctx, a.WorkspaceID, a.UserID)
		if err != nil {
			return err
		}
		if s.o.MaxPerUser > 0 && n >= int64(s.o.MaxPerUser) {
			quotaErr = service.Errf(types.ErrCodeTooMany, "application limit reached")
			return quotaErr
		}
		return tx.CreateApplication(ctx, app)
	})
	if err != nil {
		if quotaErr != nil {
			return nil, quotaErr
		}
		return nil, fail(err)
	}
	return s.view(ctx, app), nil
}

func (s *Service) Update(ctx context.Context, a service.Actor, id string, r UpdateRequest) (*View, error) {
	app, err := s.o.Access.Application(ctx, a, id, access.Configure)
	if err != nil {
		return nil, err
	}
	if r.Version < 1 {
		return nil, service.BadRequest("version required")
	}
	name := strings.TrimSpace(r.Name)
	if name == "" {
		name = app.Name
	}
	spec := r.Spec
	if len(spec) == 0 {
		spec = app.Spec
	}
	norm, _, _, err := s.boundary(ctx, a, app.WorkspaceID, spec)
	if err != nil {
		return nil, fail(err)
	}
	next := &store.Application{
		ID: app.ID, WorkspaceID: app.WorkspaceID, UserID: app.UserID,
		Name: name, Code: r.Code, Spec: norm,
		InputSchema: r.InputSchema, OutputSchema: r.OutputSchema,
		UpdatedAt: s.o.Now().UTC(),
	}
	ok, err := s.o.Store.UpdateApplicationDraft(ctx, next, r.Version)
	if err != nil {
		return nil, fail(err)
	}
	if !ok {
		return nil, service.Errf(types.ErrCodeConflict, "application changed")
	}
	cur, err := s.o.Store.GetApplication(ctx, id)
	if err != nil {
		return nil, fail(err)
	}
	return s.view(ctx, cur), nil
}

func (s *Service) Get(ctx context.Context, a service.Actor, id string) (*View, error) {
	app, err := s.o.Access.Application(ctx, a, id, access.View)
	if err != nil {
		return nil, err
	}
	return s.view(ctx, app), nil
}

func (s *Service) List(ctx context.Context, a service.Actor, after string, limit int) ([]*View, error) {
	filter, err := s.o.Access.Applications(ctx, a)
	if err != nil {
		return nil, err
	}
	filter.AfterID = after
	if limit < 0 || limit > 200 {
		return nil, service.BadRequest("limit must be between 1 and 200")
	}
	filter.Limit = limit
	rows, err := s.o.Store.ListApplications(ctx, filter)
	if err != nil {
		return nil, fail(err)
	}
	out := make([]*View, 0, len(rows))
	for _, r := range rows {
		out = append(out, s.view(ctx, r))
	}
	return out, nil
}

func (s *Service) Delete(ctx context.Context, a service.Actor, id string, version int64) error {
	app, err := s.o.Access.Application(ctx, a, id, access.Configure)
	if err != nil {
		return err
	}
	ok, err := s.o.Store.DeleteApplication(ctx, app.WorkspaceID, id, version, s.o.Now())
	if err != nil {
		return fail(err)
	}
	if !ok {
		return service.Errf(types.ErrCodeConflict, "application changed")
	}
	return nil
}

func fail(err error) error {
	var domain *service.Error
	if errors.As(err, &domain) {
		switch domain.Code {
		case types.ErrCodeBadRequest, types.ErrCodeBadSpec, types.ErrCodeBadCapability,
			types.ErrCodeBadPolicy, types.ErrCodeBadSecret, types.ErrCodeTooLarge:
			return service.Errf(domain.Code, "invalid application settings")
		case types.ErrCodeForbidden:
			return service.Forbidden("application access denied")
		case types.ErrCodeNotFound:
			return service.NotFound("application not found")
		case types.ErrCodeTooMany:
			return service.Errf(types.ErrCodeTooMany, "application capacity exhausted")
		case types.ErrCodeConflict:
			return service.Conflict("application changed")
		case types.ErrCodeNotRunning:
			return service.Errf(types.ErrCodeNotRunning, "application is not ready")
		}
	}
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrNotFound):
		return service.NotFound("application not found")
	case errors.Is(err, store.ErrConflict):
		return service.Errf(types.ErrCodeConflict, "application changed")
	case errors.Is(err, store.ErrInvalidServingRecord):
		return service.BadRequest("invalid application")
	case errors.Is(err, runtime.ErrTooMany):
		return service.Errf(types.ErrCodeTooMany, "application capacity exhausted")
	case errors.Is(err, runtime.ErrBadSpec), errors.Is(err, runtime.ErrBadCapability):
		return service.BadRequest("invalid application runtime settings")
	case errors.Is(err, runtime.ErrStaleEpoch), errors.Is(err, runtime.ErrNotRunning), errors.Is(err, runtime.ErrNotFound):
		return service.Errf(types.ErrCodeNotRunning, "application is not ready")
	default:
		// Never leak source/spec/secret text — fixed message.
		return service.Internal(errors.New("application error"))
	}
}
