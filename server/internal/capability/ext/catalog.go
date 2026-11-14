package ext

import (
	"context"

	"calcside/internal/types"
)

// Info describes one bound extension version for the catalog endpoint.
// Source is the canonical family@version identifier; Version is the
// identifier version (the ref for remote entries), not manifest display
// text.
type Info struct {
	Source       string                 `json:"source"`
	Family       string                 `json:"family"`
	Version      string                 `json:"version"`
	Sum          string                 `json:"sum"`
	Name         string                 `json:"name"`
	Description  string                 `json:"description"`
	IconURL      string                 `json:"icon_url,omitempty"`
	Icon         []byte                 `json:"-"`
	Interface    string                 `json:"interface,omitempty"`
	Models       []string               `json:"models,omitempty"`
	Dependencies []types.CapabilityName `json:"dependencies"`
	Ops          []OpSpec               `json:"ops"`
	Config       []ConfigField          `json:"config"`
}

// CatalogDiagnostic is one admission failure: a filesystem candidate
// that could not be bound (changed content, bad manifest, invalid
// version directory). Messages never contain host filesystem paths.
type CatalogDiagnostic struct {
	Source  string `json:"source"`
	Message string `json:"message"`
}

// Catalog is the response of GET /api/v1/extensions.
type Catalog struct {
	Extensions    []Info              `json:"extensions"`
	RemoteEnabled bool                `json:"remote_enabled"`
	LocalEnabled  bool                `json:"local_enabled"`
	Diagnostics   []CatalogDiagnostic `json:"diagnostics"`
}

type CatalogProvider interface {
	Available(ctx context.Context) Catalog
}
