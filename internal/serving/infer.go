package serving

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

// inferLimits bounds the sample document — small by design since a
// schema skeleton needs only shape, not data volume.
var inferLimits = Limits{MaxBytes: 64 << 10, MaxDepth: 16, MaxNodes: 1024}

var errInfer = errors.New("serving: unsupported sample")

const maxShapeVariants = 16

// InferSchema drafts a JSON Schema from a raw JSON sample. The root
// must be an object; observed keys become required properties and the
// object stays open (additionalProperties: true) since a sample is not
// a closed-world guarantee. Array items deduplicate canonical shapes:
// one shape -> items, 2..16 -> items.anyOf in first-seen order, more
// reject rather than silently widening. json.Number tokens keep their
// lexical form: no fraction/exponent => integer, otherwise number —
// nothing is ever converted through float64, so large integers stay
// exact. The result is canonicalized and validated against CompileSchema
// before being returned.
func InferSchema(raw []byte) (json.RawMessage, error) {
	v, err := DecodeJSON(raw, inferLimits)
	if err != nil {
		return nil, errInfer
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, errInfer
	}
	s, ok := inferObject(obj)
	if !ok {
		return nil, errInfer
	}
	out, err := json.Marshal(s)
	if err != nil {
		return nil, errInfer
	}
	// Prove the draft itself compiles under schema bounds — never
	// hand callers something validation would reject later.
	if _, err := CompileSchema(out, Limits{MaxBytes: 64 << 10, MaxDepth: 32, MaxNodes: 2048}); err != nil {
		return nil, errInfer
	}
	return json.RawMessage(out), nil
}

func inferObject(m map[string]any) (map[string]any, bool) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	props := make(map[string]any, len(m))
	for _, k := range keys {
		s, ok := inferValue(m[k])
		if !ok {
			return nil, false
		}
		props[k] = s
	}
	return map[string]any{
		"type":                 "object",
		"properties":           props,
		"required":             keys,
		"additionalProperties": true,
	}, true
}

func inferValue(v any) (map[string]any, bool) {
	switch t := v.(type) {
	case nil:
		return map[string]any{"type": "null"}, true
	case bool:
		return map[string]any{"type": "boolean"}, true
	case string:
		return map[string]any{"type": "string"}, true
	case json.Number:
		if strings.ContainsAny(t.String(), ".eE") {
			return map[string]any{"type": "number"}, true
		}
		return map[string]any{"type": "integer"}, true
	case map[string]any:
		return inferObject(t)
	case []any:
		return inferArray(t)
	default:
		return nil, false
	}
}

func inferArray(items []any) (map[string]any, bool) {
	if len(items) == 0 {
		return map[string]any{"type": "array", "items": map[string]any{}}, true
	}
	var shapes []map[string]any
	seen := map[string]bool{}
	for _, it := range items {
		s, ok := inferValue(it)
		if !ok {
			return nil, false
		}
		if len(seen) > maxShapeVariants {
			return nil, false // stop early; never widen
		}
		enc, err := json.Marshal(s)
		if err != nil {
			return nil, false
		}
		if !seen[string(enc)] {
			seen[string(enc)] = true
			shapes = append(shapes, s)
		}
	}
	out := map[string]any{"type": "array"}
	if len(shapes) > maxShapeVariants {
		return nil, false // reject, never silently widen
	}
	if len(shapes) == 1 {
		out["items"] = shapes[0]
	} else {
		anyOf := make([]any, len(shapes))
		for i, s := range shapes {
			anyOf[i] = s
		}
		out["items"] = map[string]any{"anyOf": anyOf}
	}
	return out, true
}
