package ext

import (
	"bytes"
	"fmt"
	"regexp"

	"go.yaml.in/yaml/v3"

	"calcside/internal/types"
)

var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// parseManifest decodes capability.yaml (unknown fields rejected).
func parseManifest(data []byte) (*CapabilityManifest, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var m CapabilityManifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("ext: %s: %w", manifestFN, err)
	}
	if err := validateManifest(&m); err != nil {
		return nil, err
	}
	return &m, nil
}

func validateManifest(m *CapabilityManifest) error {
	if m.Icon != "" && !ValidIconPath(m.Icon) {
		return fmt.Errorf("ext: invalid icon path")
	}
	for _, d := range m.Dependencies {
		// Extensions may only depend on base capabilities (net/fs/io) —
		// never on other extensions.
		if !d.Valid() || d == types.CapExt {
			return fmt.Errorf("ext: %s: invalid base dependency %q", m.Name, d)
		}
	}
	seen := map[string]bool{}
	for _, op := range m.Ops {
		if !identRe.MatchString(op.Name) {
			return fmt.Errorf("ext: %s: invalid op name %q", m.Name, op.Name)
		}
		if seen[op.Name] {
			return fmt.Errorf("ext: %s: duplicate op %q", m.Name, op.Name)
		}
		seen[op.Name] = true
		for _, p := range op.Params {
			if !identRe.MatchString(p) {
				return fmt.Errorf("ext: %s op %s: invalid param %q", m.Name, op.Name, p)
			}
		}
	}
	for _, cf := range m.Config {
		if !identRe.MatchString(cf.Name) {
			return fmt.Errorf("ext: %s: invalid config name %q", m.Name, cf.Name)
		}
		// "secret" is an ext-local type: the value must be a
		// {{secrets.NAME}} reference; not part of types.FieldType.
		if cf.Type != "secret" && !types.FieldType(cf.Type).Valid() {
			return fmt.Errorf("ext: %s config %s: invalid type %q", m.Name, cf.Name, cf.Type)
		}
	}
	return nil
}
