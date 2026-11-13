package ext

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"

	"go.yaml.in/yaml/v3"

	"calcside/internal/capability"
	"calcside/internal/llm"
	"calcside/internal/types"
)

var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ParseManifest decodes capability.yaml strictly (unknown fields
// rejected) and validates it — the read surface for manifest metadata
// without loading or initializing the extension.
func ParseManifest(data []byte) (*CapabilityManifest, error) {
	return parseManifest(data)
}

// UnmarshalYAML decodes one ops[] entry strictly (unknown keys
// rejected). Each params item is either a plain name string — the
// legacy form — or a mapping {name, type, doc, optional, default,
// fields} documenting the parameter. Params always collects the names
// in order; ParamDocs is populated only when at least one item is a
// mapping, with plain items contributing a name-only entry.
func (op *OpSpec) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.MappingNode {
		return fmt.Errorf("ext: op must be a mapping")
	}
	seen := map[string]bool{}
	for i := 0; i+1 < len(value.Content); i += 2 {
		key := value.Content[i].Value
		if seen[key] {
			return fmt.Errorf("ext: op: duplicate key %q", key)
		}
		seen[key] = true
		v := value.Content[i+1]
		var err error
		switch key {
		case "name":
			err = v.Decode(&op.Name)
		case "doc":
			err = v.Decode(&op.Doc)
		case "returns":
			err = v.Decode(&op.Returns)
		case "example":
			err = v.Decode(&op.Example)
		case "params":
			err = op.decodeParams(v)
		default:
			return fmt.Errorf("ext: op: unknown key %q", key)
		}
		if err != nil {
			return fmt.Errorf("ext: op: %s: %w", key, err)
		}
	}
	return nil
}

func (op *OpSpec) decodeParams(v *yaml.Node) error {
	if v.Kind == yaml.ScalarNode && v.Tag == "!!null" {
		return nil // `params:` empty is the legacy no-params form
	}
	if v.Kind != yaml.SequenceNode {
		return fmt.Errorf("params must be a list")
	}
	mapped := false
	for _, item := range v.Content {
		if item.Kind == yaml.MappingNode {
			mapped = true
			pd, err := decodeParamDoc(item, true)
			if err != nil {
				return err
			}
			op.Params = append(op.Params, pd.Name)
			op.ParamDocs = append(op.ParamDocs, *pd)
			continue
		}
		var name string
		if err := item.Decode(&name); err != nil {
			return fmt.Errorf("params item must be a name or a {name, ...} mapping: %w", err)
		}
		op.Params = append(op.Params, name)
		op.ParamDocs = append(op.ParamDocs, capability.ParamDoc{Name: name})
	}
	if !mapped {
		op.ParamDocs = nil
	}
	return nil
}

// decodeParamDoc decodes one params mapping item (or one fields item
// when allowFields is false — fields is one level deep only).
func decodeParamDoc(node *yaml.Node, allowFields bool) (*capability.ParamDoc, error) {
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("param doc must be a mapping")
	}
	pd := &capability.ParamDoc{}
	seen := map[string]bool{}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i].Value
		if seen[key] {
			return nil, fmt.Errorf("param doc %q: duplicate key %q", pd.Name, key)
		}
		seen[key] = true
		v := node.Content[i+1]
		var err error
		switch key {
		case "name":
			err = v.Decode(&pd.Name)
		case "type":
			err = v.Decode(&pd.Type)
		case "doc":
			err = v.Decode(&pd.Doc)
		case "optional":
			err = v.Decode(&pd.Optional)
		case "default":
			var dv any
			if err = v.Decode(&dv); err == nil {
				pd.Default = dv
				pd.HasDefault = true
			}
		case "fields":
			if !allowFields {
				return nil, fmt.Errorf("param doc %q: fields cannot nest", pd.Name)
			}
			if v.Kind == yaml.ScalarNode && v.Tag == "!!null" {
				continue
			}
			if v.Kind != yaml.SequenceNode {
				return nil, fmt.Errorf("param doc %q: fields must be a list", pd.Name)
			}
			for _, item := range v.Content {
				sub, derr := decodeParamDoc(item, false)
				if derr != nil {
					return nil, derr
				}
				pd.Fields = append(pd.Fields, *sub)
			}
		default:
			return nil, fmt.Errorf("param doc %q: unknown key %q", pd.Name, key)
		}
		if err != nil {
			return nil, fmt.Errorf("param doc %q: %s: %w", pd.Name, key, err)
		}
	}
	if pd.Name == "" {
		return nil, fmt.Errorf("param doc requires a name")
	}
	return pd, nil
}

// validateParamDoc enforces the manifest bounds on one documented
// parameter; isField marks dict-field entries (which cannot nest
// further fields).
func validateParamDoc(mname, opname string, p capability.ParamDoc, isField bool) error {
	at := fmt.Sprintf("ext: %s op %s param %s", mname, opname, p.Name)
	if !identRe.MatchString(p.Name) {
		return fmt.Errorf("%s: invalid name %q", at, p.Name)
	}
	if len(p.Type) > 64 {
		return fmt.Errorf("%s: type exceeds 64 bytes", at)
	}
	if len(p.Doc) > 1024 {
		return fmt.Errorf("%s: doc exceeds 1 KiB", at)
	}
	if len(p.Fields) > 0 {
		if isField {
			return fmt.Errorf("%s: fields cannot nest", at)
		}
		if len(p.Fields) > 32 {
			return fmt.Errorf("%s: more than 32 fields", at)
		}
		seen := map[string]bool{}
		for _, f := range p.Fields {
			if seen[f.Name] {
				return fmt.Errorf("%s: duplicate field %q", at, f.Name)
			}
			seen[f.Name] = true
			if err := validateParamDoc(mname, opname, f, true); err != nil {
				return err
			}
		}
	}
	if (p.HasDefault || p.Default != nil) && !jsonScalar(p.Default) {
		return fmt.Errorf("%s: default must be a JSON scalar", at)
	}
	return nil
}

// jsonScalar reports whether v marshals to a JSON scalar (string,
// number, bool or null) — manifest defaults must be scalars.
func jsonScalar(v any) bool {
	if v == nil {
		return true
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return false
	}
	return raw[0] != '{' && raw[0] != '['
}

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
	if m.Interface != "" && m.Interface != llm.Interface {
		return fmt.Errorf("ext: %s: invalid interface %q", m.Name, m.Interface)
	}
	if m.Capability != "" && m.Capability != llm.Capability {
		return fmt.Errorf("ext: %s: invalid capability %q", m.Name, m.Capability)
	}
	if m.Interface != llm.Interface && len(m.Models) > 0 {
		return fmt.Errorf("ext: %s: models are only valid for interface %q", m.Name, llm.Interface)
	}
	if _, err := llm.NormalizeModels(m.Models); err != nil {
		return fmt.Errorf("ext: %s: %w", m.Name, err)
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
		if len(op.Returns) > 512 {
			return fmt.Errorf("ext: %s op %s: returns exceeds 512 bytes", m.Name, op.Name)
		}
		if len(op.Example) > 4096 {
			return fmt.Errorf("ext: %s op %s: example exceeds 4 KiB", m.Name, op.Name)
		}
		if len(op.ParamDocs) > 0 {
			seenParams := map[string]bool{}
			for _, p := range op.ParamDocs {
				if seenParams[p.Name] {
					return fmt.Errorf("ext: %s op %s: duplicate param doc %q", m.Name, op.Name, p.Name)
				}
				seenParams[p.Name] = true
				if err := validateParamDoc(m.Name, op.Name, p, false); err != nil {
					return err
				}
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
