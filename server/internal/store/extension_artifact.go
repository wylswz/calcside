package store

import (
	"context"
	"errors"
	"maps"
	"strings"
	"time"

	"calcside/internal/extartifact"
)

var ErrInvalidExtensionArtifact = errors.New("store: invalid extension artifact")

// ExtensionArtifact is immutable source code, never resolved credentials.
// A source may have many content-addressed versions.
type ExtensionArtifact struct {
	Source    string            `json:"source"`
	Sum       string            `json:"sum"`
	Files     map[string][]byte `json:"files"`
	CreatedAt time.Time         `json:"created_at"`
}

// ExtensionVersion is the immutable binding of one (family, version)
// pair to an artifact sum. ArtifactSource is the source string the
// artifact was stored under — the canonical source for new captures, a
// legacy source for migrated bindings.
type ExtensionVersion struct {
	Family         string    `json:"family"`
	Version        string    `json:"version"`
	Sum            string    `json:"sum"`
	ArtifactSource string    `json:"artifact_source"`
	CreatedAt      time.Time `json:"created_at"`
}

type ExtensionArtifactStore interface {
	PutExtensionArtifact(context.Context, *ExtensionArtifact) error
	GetExtensionArtifact(ctx context.Context, source, sum string) (*ExtensionArtifact, error)
	// BindExtensionVersion atomically stores artifact (idempotent put;
	// may be nil when the (v.ArtifactSource, v.Sum) artifact already
	// exists — ErrNotFound if it does not) and binds
	// (v.Family, v.Version) -> v.Sum. Re-binding the same sum is
	// idempotent and keeps the original row (including its
	// artifact_source). A different sum returns ErrConflict and stores
	// nothing.
	BindExtensionVersion(ctx context.Context, v *ExtensionVersion, artifact *ExtensionArtifact) error
	GetExtensionVersion(ctx context.Context, family, version string) (*ExtensionVersion, error)
	ListExtensionVersions(ctx context.Context) ([]ExtensionVersion, error)
}

// Validate checks the binding fields; artifact bytes are validated by
// ExtensionArtifact.Validate separately.
func (v *ExtensionVersion) Validate() error {
	if v == nil || v.Family == "" || len(v.Family) > 512 ||
		!extartifact.ValidVersion(v.Version) ||
		!extartifact.ValidSum(v.Sum) || v.ArtifactSource == "" ||
		len(v.ArtifactSource) > 512 ||
		strings.ContainsAny(v.ArtifactSource, "\x00\r\n\t ") ||
		v.CreatedAt.IsZero() {
		return ErrInvalidExtensionArtifact
	}
	return nil
}

func (a *ExtensionArtifact) Validate() error {
	if a == nil || a.Source == "" || len(a.Source) > 512 ||
		strings.ContainsAny(a.Source, "\x00\r\n\t ") || a.CreatedAt.IsZero() {
		return ErrInvalidExtensionArtifact
	}
	sum, err := extartifact.TreeHash(a.Files)
	if err != nil || sum != a.Sum {
		return ErrInvalidExtensionArtifact
	}
	return nil
}

func (a *ExtensionArtifact) Clone() *ExtensionArtifact {
	out := *a
	out.Files = maps.Clone(a.Files)
	for name, b := range out.Files {
		out.Files[name] = append([]byte(nil), b...)
	}
	return &out
}
