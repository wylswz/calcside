// Package module implements the workspace module library domain on the
// API tier: draft CRUD, source validation, publish preview, atomic
// publish, version history compare, and restore.
//
// Modules are source-only artifacts — .star files plus a strict
// module.yaml manifest declaring base capability requirements and
// extension dependencies (source+sum only). No runtime config,
// credentials, environment, or plaintext values ever live in a module;
// the consumer binds credentials via its own environment at create
// time. Drafts are reviewed and published under draft/base CAS; an
// identical candidate produces no new version row.
package module

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"calcside/cmd/calcside/internal/service"
	"calcside/cmd/calcside/internal/service/access"
	"calcside/cmd/calcside/internal/service/review"
	"calcside/cmd/calcside/internal/service/sandbox"
	"calcside/internal/capability"
	"calcside/internal/extartifact"
	mod "calcside/internal/module"
	"calcside/internal/stdlib"
	"calcside/internal/store"
	"calcside/internal/types"
)

// Options wires the service. SpecPinner is the API artifact
// repository; when nil, publish rejects manifests that declare ext
// dependencies (their sums could not be pinned), and has_unpublished_changes
// degrades to always-dirty for manifests that declare ext deps.
type Options struct {
	Store      store.Store
	Access     *access.Checker
	Registry   *capability.Registry
	SpecPinner SpecPinner
	Sandbox    *sandbox.Service // nil = debug endpoints unavailable
	Now        func() time.Time
}

type SpecPinner interface {
	PinSpec(context.Context, []byte) (json.RawMessage, error)
}

type Service struct {
	o Options
}

func New(o Options) (*Service, error) {
	if o.Store == nil || o.Access == nil || o.Registry == nil {
		return nil, errors.New("module: store, access, registry required")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Service{o: o}, nil
}

func fail(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		return service.NotFound("module not found")
	case errors.Is(err, store.ErrModuleDraftConflict):
		return service.Conflict("module draft changed — reload and retry")
	case errors.Is(err, store.ErrModuleDebugConflict):
		return service.Conflict("module debug settings changed — reload and retry")
	case errors.Is(err, store.ErrModuleBaseConflict):
		return service.Conflict("module base changed — re-run preview and retry")
	case errors.Is(err, store.ErrConflict):
		return service.Conflict("conflict")
	case errors.Is(err, store.ErrInvalidModule):
		return service.Errf(types.ErrCodeBadSpec, "%s", err.Error())
	default:
		return service.Internal(err)
	}
}

// ---- wire shapes ----

// RequirementsView is the manifest's requirements section as the wire
// sees it: capabilities list plus ext alias → {source,sum}.
type RequirementsView struct {
	Capabilities []string                      `json:"capabilities,omitempty"`
	Ext          map[string]mod.ExtRequirement `json:"ext,omitempty"`
}

// View is a module draft row for responses — UTF-8 files, not bytes.
type View struct {
	ID                    string            `json:"id"`
	WorkspaceID           string            `json:"workspace_id"`
	UserID                string            `json:"user_id"`
	Name                  string            `json:"name"`
	Description           string            `json:"description"`
	Files                 map[string]string `json:"files"`
	DraftVersion          int64             `json:"draft_version"`
	LatestVersion         int64             `json:"latest_version"`
	LatestSum             string            `json:"latest_sum,omitempty"`
	HasUnpublishedChanges bool              `json:"has_unpublished_changes"`
	Requirements          *RequirementsView `json:"requirements,omitempty"`
	CreatedAt             time.Time         `json:"created_at"`
	UpdatedAt             time.Time         `json:"updated_at"`
}

// VersionView is one immutable published version.
type VersionView struct {
	Version      int64             `json:"version"`
	Sum          string            `json:"sum"`
	Files        map[string]string `json:"files"`
	PublishedBy  string            `json:"published_by"`
	Note         string            `json:"note,omitempty"`
	BaseVersion  *int64            `json:"base_version,omitempty"`
	Requirements *RequirementsView `json:"requirements,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
}

type CreateRequest struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Files       map[string]string `json:"files"`
}

type UpdateRequest struct {
	DraftVersion int64             `json:"draft_version"`
	Name         *string           `json:"name,omitempty"`
	Description  *string           `json:"description,omitempty"`
	Files        map[string]string `json:"files,omitempty"`
	Requirements *RequirementsView `json:"requirements,omitempty"`
}

// PublishRequest carries the review outputs the caller already saw.
// ExpectedBase is required-in-presence: "" means "no prior version".
// PublishRequest carries the review outputs the caller already saw:
// the draft version reviewed, the expected base sum ("" = no prior
// version, presence required) and the candidate digest preview
// returned. The service rebuilds the pinned candidate and compares
// digests — the caller never re-uploads a tree.
type PublishRequest struct {
	DraftVersion int64   `json:"draft_version"`
	ExpectedBase *string `json:"expected_base"` // nil = field absent
	Candidate    string  `json:"candidate"`     // h1: digest from preview
	Note         string  `json:"note,omitempty"`
}

// Review is the preview/compare response.
type Review = review.Result

func utf8Files(in map[string]string) map[string][]byte {
	if in == nil {
		return nil
	}
	out := make(map[string][]byte, len(in))
	for k, v := range in {
		out[k] = []byte(v)
	}
	return out
}

func strFiles(in map[string][]byte) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = string(v)
	}
	return out
}

// requirements extracts requirements from the manifest in files, or
// nil when absent — defensive: stored files already validated.
func requirements(files map[string][]byte) *RequirementsView {
	data, ok := files[mod.ManifestFile]
	if !ok {
		return nil
	}
	mf, err := mod.ParseManifest(data)
	if err != nil {
		return nil
	}
	r := &RequirementsView{Capabilities: mf.Requires.Capabilities, Ext: mf.Requires.Ext}
	if len(r.Capabilities) == 0 && len(r.Ext) == 0 {
		return nil
	}
	return r
}

// toView builds the draft view. has_unpublished_changes is true when
// nothing was ever published, or when the pinned candidate hash — the
// digest a publish would produce — differs from the latest version.
// A pinning failure conservatively reports dirty rather than hiding
// a real change behind an error.
func (s *Service) toView(ctx context.Context, m *store.Module, latest *store.ModuleVersion) *View {
	v := &View{
		ID: m.ID, WorkspaceID: m.WorkspaceID, UserID: m.UserID,
		Name: m.Name, Description: m.Description,
		Files: strFiles(m.Files), DraftVersion: m.DraftVersion,
		Requirements: requirements(m.Files),
		CreatedAt:    m.CreatedAt, UpdatedAt: m.UpdatedAt,
	}
	if latest == nil {
		v.HasUnpublishedChanges = true
	} else {
		v.LatestVersion = latest.Version
		v.LatestSum = latest.Sum
		pinned, err := s.pinCandidate(ctx, m.Files)
		if err != nil {
			v.HasUnpublishedChanges = true
		} else if sum, err := extartifact.TreeHash(pinned); err != nil {
			v.HasUnpublishedChanges = true
		} else {
			v.HasUnpublishedChanges = sum != latest.Sum
		}
	}
	return v
}

func toVersionView(v *store.ModuleVersion) *VersionView {
	return &VersionView{
		Version: v.Version, Sum: v.Sum, Files: strFiles(v.Files),
		PublishedBy: v.PublishedBy, Note: v.Note, BaseVersion: v.BaseVersion,
		Requirements: requirements(v.Files), CreatedAt: v.CreatedAt,
	}
}

// has is the static predeclared-name predicate for source validation:
// stdlib modules + env + secrets + help + print + interrupt + every// capability name. It never initializes capabilities.
func (s *Service) has(name string) bool {
	switch name {
	case "env", "secrets", "help", "print", "interrupt":
		return true
	}
	if _, ok := stdlib.Modules()[name]; ok {
		return true
	}
	if s.o.Registry != nil {
		if _, ok := s.o.Registry.Get(types.CapabilityName(name)); ok {
			return true
		}
	}
	return false
}
