package serving

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// schemaURI is the only accepted $schema value: the pinned draft.
const schemaURI = "https://json-schema.org/draft/2020-12/schema"

// contractURL is the synthetic, unresolvable location the schema
// document is registered under inside the compiler.
const contractURL = "https://calcside.invalid/contract.json"

// errCompile is the only compile failure reported to callers: library
// errors embed schema values, keys and URLs, any of which may carry
// secrets.
var errCompile = errors.New("schema: document does not satisfy the contract schema rules")

// errMismatch is the only validation failure reported to callers.
var errMismatch = errors.New("schema: input does not match contract")

// Schema is a compiled, immutable JSON Schema in the pinned EP-0014
// subset of draft 2020-12, backed by
// github.com/santhosh-tekuri/jsonschema/v6. It is safe for concurrent
// validation.
type Schema struct {
	raw    json.RawMessage
	sch    *jsonschema.Schema
	limits Limits // input budgets applied by Validate
}

// JSON returns the schema document as compiled; the result is a copy.
func (s *Schema) JSON() json.RawMessage {
	out := make(json.RawMessage, len(s.raw))
	copy(out, s.raw)
	return out
}

// Validate decodes raw under the compile-time limits and checks it
// against the schema. Input is never coerced or defaulted. Any
// non-match reports the same generic error; library details never
// leak input or schema content.
func (s *Schema) Validate(raw []byte) error {
	v, err := DecodeJSON(raw, s.limits)
	if err != nil {
		return err
	}
	if err := s.sch.Validate(v); err != nil {
		return errMismatch
	}
	return nil
}

// denyLoader refuses every URL load, so compilation can never fetch a
// remote or local resource: only the embedded metaschemas and the
// explicitly registered contract document are reachable.
type denyLoader struct{}

func (denyLoader) Load(string) (any, error) {
	return nil, errors.New("schema: external resources are not allowed")
}

func newCompiler() *jsonschema.Compiler {
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.UseLoader(denyLoader{})
	return c
}

// CompileSchema parses and compiles the pinned schema subset. The
// document is additionally bounded by fixed caps (64 KiB bytes, depth
// 32, 2048 nodes, 16 anyOf members) on top of the caller's limits, and
// every keyword outside the subset -- including any $ref, $defs,
// format, pattern, or composition other than anyOf -- is rejected
// before the document reaches the library. No URL is ever fetched.
// All failures return the same generic error.
func CompileSchema(raw []byte, limits Limits) (*Schema, error) {
	l := limits.Effective()
	cap := Limits{
		MaxBytes: min(l.MaxBytes, schemaMaxBytes),
		MaxDepth: min(l.MaxDepth, schemaMaxDepth),
		MaxNodes: min(l.MaxNodes, schemaMaxNodes),
	}
	if len(raw) > cap.MaxBytes {
		return nil, fmt.Errorf("schema: document exceeds byte limit (%d > %d)", len(raw), cap.MaxBytes)
	}
	v, err := DecodeJSON(raw, cap)
	if err != nil {
		return nil, err
	}
	if _, ok := v.(map[string]any); !ok {
		return nil, errors.New("schema: root must be an object")
	}
	if err := checkSubset(v, 0); err != nil {
		return nil, err
	}
	c := newCompiler()
	if err := c.AddResource(contractURL, v); err != nil {
		return nil, errCompile
	}
	sch, err := c.Compile(contractURL)
	if err != nil {
		return nil, errCompile
	}
	rc := make(json.RawMessage, len(raw))
	copy(rc, raw)
	return &Schema{raw: rc, sch: sch, limits: l}, nil
}

// allowedKeywords is the pinned 2020-12 subset; anything else --
// including $ref, $defs, format, pattern and composition beyond anyOf
// -- is rejected by checkSubset.
var allowedKeywords = map[string]bool{
	"$schema":              true,
	"title":                true,
	"description":          true,
	"type":                 true,
	"properties":           true,
	"required":             true,
	"additionalProperties": true,
	"items":                true,
	"enum":                 true,
	"const":                true,
	"minimum":              true,
	"maximum":              true,
	"minLength":            true,
	"maxLength":            true,
	"minItems":             true,
	"maxItems":             true,
	"anyOf":                true,
}

// countKeywords are the four bounds the library stores as platform int
// via int64 truncation; they get the fixed 32-bit cap from
// schemaMaxCount.
var countKeywords = map[string]bool{
	"minLength": true,
	"maxLength": true,
	"minItems":  true,
	"maxItems":  true,
}

var maxCount = big.NewRat(schemaMaxCount, 1)

// checkSubset enforces the product subset shape: allowed keywords only,
// schema positions always objects (boolean schemas exist nowhere except
// additionalProperties), items a single schema, anyOf bounded, enum
// non-empty, $schema pinned, and the four count keywords within the
// fixed cap. Payload positions (properties keys, enum, const) are data
// and are not walked. Semantic keyword validity -- type names,
// required contents, numeric types -- is the library's metaschema job.
// Errors are generic by position, never echoing content.
func checkSubset(v any, depth int) error {
	m, ok := v.(map[string]any)
	if !ok {
		return errors.New("schema: schema positions must be objects")
	}
	if depth >= schemaMaxDepth {
		return errors.New("schema: depth limit exceeded")
	}
	for k, val := range m {
		if !allowedKeywords[k] {
			return errors.New("schema: unsupported keyword")
		}
		switch k {
		case "$schema":
			s, ok := val.(string)
			if !ok || s != schemaURI {
				return errors.New("schema: $schema must be the pinned draft 2020-12 URI")
			}
		case "properties":
			pm, ok := val.(map[string]any)
			if !ok {
				return errors.New("schema: properties must be an object")
			}
			for _, sub := range pm {
				if err := checkSubset(sub, depth+1); err != nil {
					return err
				}
			}
		case "additionalProperties":
			switch t := val.(type) {
			case bool:
			case map[string]any:
				if err := checkSubset(t, depth+1); err != nil {
					return err
				}
			default:
				return errors.New("schema: additionalProperties must be a boolean or schema object")
			}
		case "items":
			sub, ok := val.(map[string]any)
			if !ok {
				return errors.New("schema: items must be a single schema object")
			}
			if err := checkSubset(sub, depth+1); err != nil {
				return err
			}
		case "anyOf":
			a, ok := val.([]any)
			if !ok || len(a) == 0 || len(a) > schemaMaxAnyOf {
				return errors.New("schema: anyOf must have between 1 and 16 members")
			}
			for _, e := range a {
				if err := checkSubset(e, depth+1); err != nil {
					return err
				}
			}
		case "enum":
			a, ok := val.([]any)
			if !ok || len(a) == 0 {
				return errors.New("schema: enum must be a non-empty array")
			}
		default:
			if countKeywords[k] {
				if err := checkCountBound(val); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// checkCountBound caps minLength/maxLength/minItems/maxItems at
// schemaMaxCount: the library stores these as platform int via int64
// truncation, so values beyond the cap would silently wrap. Wrong
// types, fractions and negatives are left to the library's metaschema
// validation; only a mathematically integral, non-negative value can
// reach the range check.
func checkCountBound(v any) error {
	num, ok := v.(json.Number)
	if !ok {
		return nil // library rejects the wrong type
	}
	r, ok := new(big.Rat).SetString(num.String())
	if !ok || !r.IsInt() || r.Sign() < 0 {
		return nil // library rejects
	}
	if r.Cmp(maxCount) > 0 {
		return errors.New("schema: count bound exceeds fixed limit")
	}
	return nil
}
