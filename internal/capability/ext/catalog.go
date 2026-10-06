package ext

import "calcside/internal/types"

// Info describes one loadable extension for the catalog endpoint.
type Info struct {
	Source       string                 `json:"source"`
	Name         string                 `json:"name"`
	Version      string                 `json:"version"`
	Description  string                 `json:"description"`
	IconURL      string                 `json:"icon_url,omitempty"`
	Icon         []byte                 `json:"-"`
	Dependencies []types.CapabilityName `json:"dependencies"`
	Ops          []OpSpec               `json:"ops"`
	Config       []ConfigField          `json:"config"`
}

// Catalog is the response of GET /api/v1/extensions.
type Catalog struct {
	Extensions    []Info `json:"extensions"`
	RemoteEnabled bool   `json:"remote_enabled"`
	LocalEnabled  bool   `json:"local_enabled"`
}

type CatalogProvider interface {
	Available() Catalog
}
