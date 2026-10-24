// Package catalog serves capability and extension metadata for the
// console: the capability list and the extension catalog.
package catalog

import (
	"crypto/sha256"
	"fmt"
	"sync"

	"calcside/internal/capability"
	"calcside/internal/capability/ext"
	"calcside/internal/completion"
	"calcside/internal/stdlib"
	"calcside/internal/types"
)

type Service struct {
	reg        *capability.Registry
	extCatalog ext.CatalogProvider
	mu         sync.Mutex
	icons      map[string][]byte
}

func New(reg *capability.Registry, extCatalog ext.CatalogProvider) *Service {
	return &Service{reg: reg, extCatalog: extCatalog, icons: map[string][]byte{}}
}

// CapabilityDoc describes one registered capability factory. This is the
// public wire shape (contract-bound); api/dto aliases it — see the dto
// package doc for the boundary rule.
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
// sources are enabled on this server. This is the public wire shape
// (contract-bound); api/dto aliases it — see the dto package doc.
type ExtensionsView struct {
	Extensions    []ext.Info `json:"extensions"`
	RemoteEnabled bool       `json:"remote_enabled"`
	LocalEnabled  bool       `json:"local_enabled"`
}

// Extensions reports the extension catalog for this server.
func (s *Service) Extensions() ExtensionsView {
	out := ExtensionsView{Extensions: []ext.Info{}}
	if s.extCatalog == nil {
		return out
	}
	c := s.extCatalog.Available()
	// Copy the provider's slice: fill IconURL and nil Icon only on our
	// DTO copies — the provider-owned Info values are never mutated.
	out.Extensions = append(out.Extensions, c.Extensions...)
	out.RemoteEnabled = c.RemoteEnabled
	out.LocalEnabled = c.LocalEnabled
	// Build one complete snapshot for every icon in this catalog, then
	// swap the whole map under the lock — concurrent readers always see
	// a coherent full map, and retiring the map replaces any stale
	// entries wholesale (no per-request growth, no FIFO cap).
	next := make(map[string][]byte, len(out.Extensions))
	for i := range out.Extensions {
		if icon := out.Extensions[i].Icon; len(icon) > 0 {
			id := fmt.Sprintf("%x", sha256.Sum256(icon))
			next[id] = icon
			out.Extensions[i].IconURL = "/api/v1/extensions/icons/" + id
			out.Extensions[i].Icon = nil
		}
	}
	s.mu.Lock()
	s.icons = next
	s.mu.Unlock()
	return out
}

type EditorMetadata struct {
	Utilities    []completion.Symbol `json:"utilities"`
	Rego         []completion.Symbol `json:"rego"`
	Capabilities []CapabilityDoc     `json:"capabilities"`
}

func (s *Service) Editor() EditorMetadata {
	caps := s.Capabilities()
	for i := range caps {
		if caps[i].Name == string(types.CapIO) {
			for _, op := range caps[i].Ops {
				if op.Name == "println" {
					op.Name = "print"
					caps[i].Ops = append(caps[i].Ops, op)
					break
				}
			}
		}
	}
	return EditorMetadata{Rego: EditorSymbols(), Capabilities: caps, Utilities: stdlib.Symbols()}
}

func (s *Service) Icon(id string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.icons[id]
}
