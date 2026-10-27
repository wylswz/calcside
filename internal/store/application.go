package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"calcside/internal/serving"
)

// PrefixApplication identifies application rows (durable JSON-function
// drafts + publish pointer, distinct from serving replicas/instances).
const PrefixApplication IDPrefix = "app_"

// Application is a durable named function draft owned by a user in a
// workspace. Version is a CAS counter over the draft; PublishedVersion
// records which draft version the persistent instance currently serves
// (0 = never published).
type Application struct {
	ID                   string          `json:"id"`
	WorkspaceID          string          `json:"workspace_id"`
	UserID               string          `json:"user_id"`
	Name                 string          `json:"name"`
	Code                 string          `json:"code"`
	Spec                 json.RawMessage `json:"spec"`
	InputSchema          json.RawMessage `json:"input_schema,omitempty"`
	OutputSchema         json.RawMessage `json:"output_schema,omitempty"`
	Version              int64           `json:"version"`
	PublishedVersion     int64           `json:"published_version"`
	PersistentInstanceID string          `json:"persistent_instance_id,omitempty"`
	CreatedAt            time.Time       `json:"created_at"`
	UpdatedAt            time.Time       `json:"updated_at"`
	DeletedAt            *time.Time      `json:"deleted_at,omitempty"`
}

// DefaultApplicationSchema is the unconstrained-object schema applied
// at publish when a side was never given a contract.
func DefaultApplicationSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","additionalProperties":true}`)
}

// ApplicationFilter scopes listing — workspace is required so one
// workspace can never read another's drafts.
type ApplicationFilter struct {
	WorkspaceID string
	UserID      string
	AfterID     string
	Limit       int
}

func validSchemaSide(raw json.RawMessage) error {
	if raw == nil {
		return nil // optional — materialized at publish
	}
	if len(raw) > 64<<10 {
		return fmt.Errorf("%w: schema over 64 KiB", ErrInvalidServingRecord)
	}
	if _, err := serving.CompileSchema(raw, serving.Limits{MaxBytes: 64 << 10, MaxDepth: 32, MaxNodes: 2048}); err != nil {
		return fmt.Errorf("%w: invalid schema", ErrInvalidServingRecord)
	}
	return nil
}

// ValidateApplication checks the write preconditions shared by create
// and draft update.
func ValidateApplication(a *Application) error {
	if a == nil {
		return fmt.Errorf("%w: nil application", ErrInvalidServingRecord)
	}
	if a.WorkspaceID == "" || a.UserID == "" {
		return fmt.Errorf("%w: workspace and user required", ErrInvalidServingRecord)
	}
	name := a.Name
	if !utf8.ValidString(name) || name == "" || strings.TrimSpace(name) == "" || utf8.RuneCountInString(name) > 128 {
		return fmt.Errorf("%w: name required, max 128 runes", ErrInvalidServingRecord)
	}
	if !utf8.ValidString(a.Code) || len(a.Code) > 128<<10 {
		return fmt.Errorf("%w: code must be UTF-8 under 128 KiB", ErrInvalidServingRecord)
	}
	if len(a.Spec) > 0 {
		if len(a.Spec) > 64<<10 {
			return fmt.Errorf("%w: spec over 64 KiB", ErrInvalidServingRecord)
		}
		v, err := serving.DecodeJSON(a.Spec, serving.Limits{MaxBytes: 64 << 10, MaxDepth: 16, MaxNodes: 512})
		if err != nil {
			return fmt.Errorf("%w: spec must be a JSON object", ErrInvalidServingRecord)
		}
		m, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("%w: spec must be a JSON object", ErrInvalidServingRecord)
		}
		if err := SanitizeSpecSecrets(m); err != nil {
			return err
		}
	}
	if err := validSchemaSide(a.InputSchema); err != nil {
		return err
	}
	if err := validSchemaSide(a.OutputSchema); err != nil {
		return err
	}
	return nil
}

// NewApplication constructs a version-1 draft.
func NewApplication(workspaceID, userID, name, code string, spec, inSchema, outSchema json.RawMessage, now time.Time) (*Application, error) {
	if len(spec) == 0 {
		spec = json.RawMessage(`{}`)
	}
	a := &Application{
		WorkspaceID: workspaceID, UserID: userID, Name: name, Code: code,
		Spec:         append(json.RawMessage(nil), spec...),
		InputSchema:  append(json.RawMessage(nil), inSchema...),
		OutputSchema: append(json.RawMessage(nil), outSchema...),
		Version:      1, PublishedVersion: 0,
		CreatedAt: now.UTC(), UpdatedAt: now.UTC(),
	}
	if err := ValidateApplication(a); err != nil {
		return nil, err
	}
	a.ID = NewID(PrefixApplication)
	return a, nil
}

// ApplicationStore persists application drafts and their publish
// pointer. All CAS mutations take the draft's expected Version;
// publishing never bumps the draft version — it records
// PublishedVersion against the version it published.
type ApplicationStore interface {
	CreateApplication(ctx context.Context, a *Application) error
	GetApplication(ctx context.Context, id string) (*Application, error)
	ListApplications(ctx context.Context, f ApplicationFilter) ([]*Application, error)
	CountApplications(ctx context.Context, workspaceID, userID string) (int64, error)
	// UpdateApplicationDraft applies the draft fields under
	// expected-Version CAS; published state is untouched.
	UpdateApplicationDraft(ctx context.Context, a *Application, expectedVersion int64) (bool, error)
	// PublishApplication records the release + persistent instance
	// atomically. expectedVersion must equal the current draft version;
	// republishing the same version returns the existing pointer without
	// a generation bump. The caller performs runtime initialization
	// BEFORE this call — no prepare inside the transaction.
	PublishApplication(ctx context.Context, workspaceID, id string, expectedVersion int64, release *ServingRelease, now time.Time) (*PersistentInstance, error)
	// DeleteApplication soft-deletes the draft and disables the
	// persistent instance under the same CAS.
	DeleteApplication(ctx context.Context, workspaceID, id string, expectedVersion int64, now time.Time) (bool, error)
}
