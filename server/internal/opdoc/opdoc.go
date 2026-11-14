package opdoc

import (
	"encoding/json"
	"strconv"
	"strings"
)

// ParamDoc documents one operation parameter — or one key of a dict
// parameter — for help text, editor completion and the console.
type ParamDoc struct {
	Name     string     `json:"name"`
	Type     string     `json:"type,omitempty"`
	Doc      string     `json:"doc,omitempty"`
	Optional bool       `json:"optional,omitempty"`
	Default  any        `json:"default,omitempty"`
	Fields   []ParamDoc `json:"fields,omitempty"`
	// HasDefault marks an explicitly declared default, including an
	// explicit null (rendered as None). It is decode state, not wire
	// data: MarshalJSON emits "default": null for it so consumers can
	// tell declared-null apart from undeclared.
	HasDefault bool `json:"-"`
}

// MarshalJSON emits the documented fields in order; `default` appears
// whenever a default was declared (explicit null → null), matching the
// omitempty shape for undeclared defaults.
func (p ParamDoc) MarshalJSON() ([]byte, error) {
	var b strings.Builder
	b.WriteByte('{')
	comma := false
	put := func(name string, v any) error {
		raw, err := json.Marshal(v)
		if err != nil {
			return err
		}
		if comma {
			b.WriteByte(',')
		}
		comma = true
		b.WriteString(strconv.Quote(name))
		b.WriteByte(':')
		b.Write(raw)
		return nil
	}
	if err := put("name", p.Name); err != nil {
		return nil, err
	}
	if p.Type != "" {
		if err := put("type", p.Type); err != nil {
			return nil, err
		}
	}
	if p.Doc != "" {
		if err := put("doc", p.Doc); err != nil {
			return nil, err
		}
	}
	if p.Optional {
		if err := put("optional", true); err != nil {
			return nil, err
		}
	}
	if p.HasDefault || p.Default != nil {
		if err := put("default", p.Default); err != nil {
			return nil, err
		}
	}
	if len(p.Fields) > 0 {
		if err := put("fields", p.Fields); err != nil {
			return nil, err
		}
	}
	b.WriteByte('}')
	return []byte(b.String()), nil
}

func (p *ParamDoc) UnmarshalJSON(data []byte) error {
	type plain ParamDoc
	var aux struct {
		plain
		Default json.RawMessage `json:"default"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	*p = ParamDoc(aux.plain)
	p.HasDefault = len(aux.Default) > 0
	if p.HasDefault {
		return json.Unmarshal(aux.Default, &p.Default)
	}
	return nil
}
