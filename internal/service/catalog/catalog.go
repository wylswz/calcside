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

// Capabilities describes every registered capability factory.
func (s *Service) Capabilities() []map[string]any {
	var out []map[string]any
	for _, name := range s.reg.Names() {
		f, _ := s.reg.Get(name)
		out = append(out, map[string]any{
			"name":          string(name),
			"ops":           f.Ops(),
			"config_fields": f.ConfigFields(),
		})
	}
	return out
}

// Extensions reports the extension catalog plus whether remote/local
// sources are enabled on this server.
func (s *Service) Extensions() map[string]any {
	out := map[string]any{"extensions": []any{}, "remote_enabled": false, "local_enabled": false}
	if f, ok := s.reg.Get(types.CapExt); ok {
		if av, ok := f.(interface{ Available() ext.Catalog }); ok {
			c := av.Available()
			out["extensions"] = c.Extensions
			out["remote_enabled"] = c.RemoteEnabled
			out["local_enabled"] = c.LocalEnabled
		}
	}
	return out
}
