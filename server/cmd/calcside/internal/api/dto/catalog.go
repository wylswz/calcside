package dto

import (
	"calcside/cmd/calcside/internal/service/catalog"
)

// CapabilityDoc describes one registered capability factory. Alias per
// the dto boundary rule (see package doc): pure metadata, serialized by
// the owning package.
type CapabilityDoc = catalog.CapabilityDoc

type CapabilitiesEnvelope struct {
	Capabilities []CapabilityDoc `json:"capabilities"`
}

// ExtensionsEnvelope reports the extension catalog plus whether
// remote/local sources are enabled. Alias per the dto boundary rule.
type ExtensionsEnvelope = catalog.ExtensionsView

type EditorMetadata = catalog.EditorMetadata
