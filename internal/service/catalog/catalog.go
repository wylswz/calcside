// Package catalog serves capability and extension metadata for the
// console: the capability list and the extension catalog.
package catalog

import (
	"calcside/internal/capability"
	"calcside/internal/capability/ext"
	"calcside/internal/types"
)

type Service struct {
	reg *capability.Registry
}

func New(reg *capability.Registry) *Service {
	return &Service{reg: reg}
}

// CapabilityDoc describes one registered capability factory.
type CapabilityDoc struct {
	Name         string                `json:"name"`
	Ops          []capability.OpInfo   `json:"ops"`
	ConfigFields []capability.FieldDoc `json:"config_fields"`
}

// Capabilities describes every registered capability factory.
func (s *Service) Capabilities() []CapabilityDoc {
	var out []CapabilityDoc
	for _, name := range s.reg.Names() {
		f, _ := s.reg.Get(name)
		out = append(out, CapabilityDoc{
			Name:         string(name),
			Ops:          f.Ops(),
			ConfigFields: f.ConfigFields(),
		})
	}
	return out
}

// ExtensionsView is the extension catalog plus whether remote/local
// sources are enabled on this server.
type ExtensionsView struct {
	Extensions    []ext.Info `json:"extensions"`
	RemoteEnabled bool       `json:"remote_enabled"`
	LocalEnabled  bool       `json:"local_enabled"`
}

// Extensions reports the extension catalog for this server.
func (s *Service) Extensions() ExtensionsView {
	out := ExtensionsView{Extensions: []ext.Info{}}
	if f, ok := s.reg.Get(types.CapExt); ok {
		if av, ok := f.(interface{ Available() ext.Catalog }); ok {
			c := av.Available()
			out.Extensions = c.Extensions
			out.RemoteEnabled = c.RemoteEnabled
			out.LocalEnabled = c.LocalEnabled
		}
	}
	return out
}
