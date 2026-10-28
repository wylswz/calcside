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

type ExtensionArtifactStore interface {
	PutExtensionArtifact(context.Context, *ExtensionArtifact) error
	GetExtensionArtifact(ctx context.Context, source, sum string) (*ExtensionArtifact, error)
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
