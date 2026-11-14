package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"time"
	"unicode/utf8"

	"calcside/internal/module"
	"calcside/internal/runtime"
)

// PrefixModule identifies workspace-module rows.
const PrefixModule IDPrefix = "mod_"

var (
	// ErrInvalidModule is a malformed module write (bad name,
	// oversized fields, files violating tree rules).
	ErrInvalidModule = errors.New("store: invalid module record")
	// ErrModuleDraftConflict is a stale draft CAS: the draft moved
	// since the caller read it.
	ErrModuleDraftConflict = errors.New("store: module draft changed")
	// ErrModuleBaseConflict is the reviewed-publish base sentinel: the
	// latest published version differs from the base the reviewer
	// compared against. Distinct from ErrModuleDraftConflict so the
	// service maps the right 409.
	ErrModuleBaseConflict = errors.New("store: module published base changed")
	// ErrModuleDebugConflict is a stale debug-settings CAS: the debug
	// row moved since the caller read it. Debug settings carry their
	// own version counter, independent of DraftVersion.
	ErrModuleDebugConflict = errors.New("store: module debug settings changed")
)

const maxModuleNoteRunes = 500

// Module is a workspace-scoped Starlark library: a mutable draft
// (CAS-guarded by DraftVersion) plus immutable published versions in
// the module_versions table.
type Module struct {
	ID           string            `json:"id"`
	WorkspaceID  string            `json:"workspace_id"`
	UserID       string            `json:"user_id"`
	Name         string            `json:"name"`
	Description  string            `json:"description"`
	Files        map[string][]byte `json:"files"`
	DraftVersion int64             `json:"draft_version"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
}

// ModuleVersion is an immutable published snapshot. Version numbers
// are monotonic per module, allocated under LockWorkspace; a version
// row never changes after insert. BaseVersion records which version
// the publish review compared against (NULL when none existed).
type ModuleVersion struct {
	ModuleID    string            `json:"module_id"`
	WorkspaceID string            `json:"workspace_id"`
	Version     int64             `json:"version"`
	Sum         string            `json:"sum"`
	Files       map[string][]byte `json:"files"`
	PublishedBy string            `json:"published_by"`
	Note        string            `json:"note,omitempty"`
	BaseVersion *int64            `json:"base_version,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
}

// ModuleFilter scopes module listing; workspace is required.
type ModuleFilter struct {
	WorkspaceID string
	AfterID     string
	Limit       int
}

func cloneModuleFiles(files map[string][]byte) map[string][]byte {
	out := maps.Clone(files)
	for name, b := range out {
		out[name] = append([]byte(nil), b...)
	}
	return out
}

// ValidateModule checks the draft-write preconditions: identity,
// name grammar and length, description bound, and that the file tree
// satisfies module rules (paths, .star-only, manifest, bounds) without
// requiring a pinned sum — drafts may be unsummed.
func ValidateModule(m *Module) error {
	if m == nil {
		return fmt.Errorf("%w: nil module", ErrInvalidModule)
	}
	if m.WorkspaceID == "" || m.UserID == "" {
		return fmt.Errorf("%w: workspace and user required", ErrInvalidModule)
	}
	if !module.ValidName(m.Name) {
		return fmt.Errorf("%w: invalid name %q", ErrInvalidModule, m.Name)
	}
	if !utf8.ValidString(m.Description) || utf8.RuneCountInString(m.Description) > 300 {
		return fmt.Errorf("%w: description exceeds 300 runes or is not UTF-8", ErrInvalidModule)
	}
	if err := module.ValidateFiles(m.Files, ""); err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidModule, err)
	}
	return nil
}

// ValidateModuleVersion checks a publish candidate: identity, a
// pinned h1: sum matching the file tree, note bound, and UTF-8.
func ValidateModuleVersion(v *ModuleVersion) error {
	if v == nil {
		return fmt.Errorf("%w: nil module version", ErrInvalidModule)
	}
	if v.PublishedBy == "" {
		return fmt.Errorf("%w: publisher required", ErrInvalidModule)
	}
	if !utf8.ValidString(v.Note) || utf8.RuneCountInString(v.Note) > maxModuleNoteRunes {
		return fmt.Errorf("%w: note exceeds %d runes or is not UTF-8", ErrInvalidModule, maxModuleNoteRunes)
	}
	if err := module.ValidateFiles(v.Files, v.Sum); err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidModule, err)
	}
	if v.Sum == "" {
		return fmt.Errorf("%w: version must carry a pinned sum", ErrInvalidModule)
	}
	return nil
}

// Clone deep-copies a module draft for hand-out.
func (m *Module) Clone() *Module {
	out := *m
	out.Files = cloneModuleFiles(m.Files)
	return &out
}

// Clone deep-copies a published version for hand-out.
func (v *ModuleVersion) Clone() *ModuleVersion {
	out := *v
	out.Files = cloneModuleFiles(v.Files)
	if v.BaseVersion != nil {
		b := *v.BaseVersion
		out.BaseVersion = &b
	}
	return &out
}

// ModuleStore persists module drafts and their published version
// history (EP-0017). All mutations are workspace-scoped; draft writes
// are CAS on DraftVersion, and publish is an atomic reviewed insert
// under LockWorkspace.
type ModuleStore interface {
	CreateModule(ctx context.Context, m *Module) error
	GetModule(ctx context.Context, id string) (*Module, error)
	// GetModuleByName resolves the library lookup used by spec
	// resolution — names are unique per workspace.
	GetModuleByName(ctx context.Context, workspaceID, name string) (*Module, error)
	ListModules(ctx context.Context, f ModuleFilter) ([]*Module, error)
	// UpdateModuleDraft applies draft fields under expected-Version
	// CAS; returns ok=false on a stale read (draft moved).
	UpdateModuleDraft(ctx context.Context, m *Module, expectedDraftVersion int64) (bool, error)
	// DeleteModule drops the draft and its whole version history.
	// Already-created instances and prepared replicas hold their own
	// snapshots, so deletion does not affect them.
	DeleteModule(ctx context.Context, workspaceID, id string) error

	GetModuleVersion(ctx context.Context, moduleID string, version int64) (*ModuleVersion, error)
	ListModuleVersions(ctx context.Context, moduleID string) ([]*ModuleVersion, error)
	// PublishModule atomically inserts the next immutable version:
	// under LockWorkspace it verifies the draft CAS
	// (ErrModuleDraftConflict), verifies the reviewed base — the
	// latest version's sum must equal expectedBaseSum, "" meaning
	// never published (ErrModuleBaseConflict) — and allocates
	// version = max+1 with base_version pointing at the previous
	// latest. A candidate identical to the latest version is a no-op
	// returning that existing version without an insert.
	PublishModule(ctx context.Context, workspaceID, id string, expectedDraftVersion int64, expectedBaseSum string, candidate *ModuleVersion) (*ModuleVersion, error)

	// Debug settings are per-module editor state — script + spec —
	// carrying their own CAS version, never affecting DraftVersion
	// or the published hash.
	// GetModuleDebugSettings returns the settings row, or the absent
	// scaffold (Version 0, empty script, "{}" spec) after verifying
	// the module exists.
	GetModuleDebugSettings(ctx context.Context, workspaceID, id string) (*ModuleDebugSettings, error)
	// PutModuleDebugSettings writes settings under expected-Version
	// CAS: expectedVersion 0 inserts (row becomes 1); otherwise the
	// row must match and increments. ok=false on a stale read.
	PutModuleDebugSettings(ctx context.Context, s *ModuleDebugSettings, expectedVersion int64) (bool, error)
	// GetModuleDebugSnapshot atomically locks the module row, loads
	// the settings, and verifies expected draft + debug versions —
	// the frozen pair a debug run executes against.
	GetModuleDebugSnapshot(ctx context.Context, workspaceID, id string, expectedDraft, expectedDebug int64) (*Module, *ModuleDebugSettings, error)
}

// ModuleDebugSettings is the debug panel's per-module state (EP-0017):
// an editor script plus a refs-only runtime spec. The Version counter
// CAS-guards writes independent of the module draft and has no effect
// on published hashes.
type ModuleDebugSettings struct {
	ModuleID    string          `json:"module_id"`
	WorkspaceID string          `json:"workspace_id"`
	Version     int64           `json:"version"`
	Script      string          `json:"script"`
	Spec        json.RawMessage `json:"spec"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

const (
	// maxDebugScriptBytes caps the debug script — same bound as the
	// exec code limit, declared here to keep the store leaf free of
	// service imports.
	maxDebugScriptBytes = 256 << 10
	// maxDebugSpecBytes bounds the stored spec JSON.
	maxDebugSpecBytes = 1 << 20
)

// ValidateModuleDebugSettings checks the debug row's content
// constraints: identity, a UTF-8 script under the byte cap, and a
// spec that decodes as a runtime Spec carrying references only —
// any SecretSpec.Value (inline plaintext) is rejected.
func ValidateModuleDebugSettings(s *ModuleDebugSettings) error {
	if s == nil {
		return fmt.Errorf("%w: nil settings", ErrInvalidModule)
	}
	if s.ModuleID == "" || s.WorkspaceID == "" {
		return fmt.Errorf("%w: module and workspace required", ErrInvalidModule)
	}
	if s.Version < 0 {
		return fmt.Errorf("%w: invalid version", ErrInvalidModule)
	}
	if !utf8.ValidString(s.Script) || len(s.Script) > maxDebugScriptBytes {
		return fmt.Errorf("%w: script exceeds %d bytes or is not UTF-8", ErrInvalidModule, maxDebugScriptBytes)
	}
	spec := s.Spec
	if len(spec) == 0 {
		spec = []byte("{}")
	}
	if len(spec) > maxDebugSpecBytes {
		return fmt.Errorf("%w: spec exceeds %d bytes", ErrInvalidModule, maxDebugSpecBytes)
	}
	var typed runtime.Spec
	if err := json.Unmarshal(spec, &typed); err != nil {
		return fmt.Errorf("%w: invalid spec", ErrInvalidModule)
	}
	for name, sec := range typed.Secrets {
		if sec.Value != "" {
			return fmt.Errorf("%w: debug specs hold vault references only, not inline values", ErrInvalidModule)
		}
		_ = name
	}
	if typed.Environment != nil {
		if err := typed.Environment.Validate(); err != nil {
			return fmt.Errorf("%w: %s", ErrInvalidModule, err)
		}
	}
	return nil
}

func (s *ModuleDebugSettings) Clone() *ModuleDebugSettings {
	out := *s
	out.Spec = append(json.RawMessage(nil), s.Spec...)
	return &out
}

// NewModule constructs a draft-version-1 module.
func NewModule(workspaceID, userID, name, description string, files map[string][]byte, now time.Time) (*Module, error) {
	m := &Module{
		WorkspaceID:  workspaceID,
		UserID:       userID,
		Name:         name,
		Description:  description,
		Files:        cloneModuleFiles(files),
		DraftVersion: 1,
		CreatedAt:    now.UTC(),
		UpdatedAt:    now.UTC(),
	}
	if err := ValidateModule(m); err != nil {
		return nil, err
	}
	m.ID = NewID(PrefixModule)
	return m, nil
}
