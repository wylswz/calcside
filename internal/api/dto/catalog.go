package dto

import (
	"calcside/internal/service/catalog"
)

// CapabilityDoc describes one registered capability factory.
type CapabilityDoc = catalog.CapabilityDoc

type CapabilitiesEnvelope struct {
	Capabilities []CapabilityDoc `json:"capabilities"`
}

// ExtensionsEnvelope reports the extension catalog plus whether
// remote/local sources are enabled.
type ExtensionsEnvelope = catalog.ExtensionsView
