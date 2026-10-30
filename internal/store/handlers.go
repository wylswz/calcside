package store

import (
	"encoding/json"
	"fmt"
	"regexp"

	"calcside/internal/runtime"
	"calcside/internal/serving"
)

// handlers.go is the single validator for EP-0016 handler
// declarations, shared by application drafts (raw JSON) and serving
// releases (typed map inside the program bundle).

const (
	maxHandlers       = 32
	maxHandlersDocLen = 256 << 10
)

var handlerNameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

// validHandlerName enforces the declared-handler name rule: a
// Starlark identifier without a leading underscore, never "main".
func validHandlerName(name string) bool {
	return name != "main" && handlerNameRe.MatchString(name)
}

// contextSchemaOK checks one context_schema document: an object schema
// with "type":"object" that compiles under the pinned subset.
func contextSchemaOK(raw json.RawMessage) error {
	obj, err := serving.DecodeJSON(raw, serving.Limits{MaxBytes: 64 << 10, MaxDepth: 32, MaxNodes: 2048})
	if err != nil {
		return fmt.Errorf("%w: invalid handler context schema", ErrInvalidServingRecord)
	}
	m, ok := obj.(map[string]any)
	if !ok || m["type"] != "object" {
		return fmt.Errorf("%w: invalid handler context schema", ErrInvalidServingRecord)
	}
	if _, err := serving.CompileSchema(raw, serving.Limits{MaxBytes: 64 << 10, MaxDepth: 32, MaxNodes: 2048}); err != nil {
		return fmt.Errorf("%w: invalid handler context schema", ErrInvalidServingRecord)
	}
	return nil
}

// ValidateHandlerDeclarations checks the raw JSON handlers table an
// application draft stores: a JSON object {name:
// {"context_schema": {...}?}} with bounded size and count. Nil/empty
// means no declared handlers.
func ValidateHandlerDeclarations(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	if len(raw) > maxHandlersDocLen {
		return fmt.Errorf("%w: handlers over 256 KiB", ErrInvalidServingRecord)
	}
	v, err := serving.DecodeJSON(raw, serving.Limits{MaxBytes: maxHandlersDocLen, MaxDepth: 32, MaxNodes: 4096})
	if err != nil {
		return fmt.Errorf("%w: handlers must be a JSON object", ErrInvalidServingRecord)
	}
	m, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("%w: handlers must be a JSON object", ErrInvalidServingRecord)
	}
	if len(m) > maxHandlers {
		return fmt.Errorf("%w: too many declared handlers", ErrInvalidServingRecord)
	}
	for name, spec := range m {
		if !validHandlerName(name) {
			return fmt.Errorf("%w: invalid handler name", ErrInvalidServingRecord)
		}
		specObj, ok := spec.(map[string]any)
		if !ok {
			return fmt.Errorf("%w: handler spec must be an object", ErrInvalidServingRecord)
		}
		for k, val := range specObj {
			if k != "context_schema" {
				return fmt.Errorf("%w: unknown handler spec field", ErrInvalidServingRecord)
			}
			cs, ok := val.(map[string]any)
			if !ok {
				return fmt.Errorf("%w: invalid handler context schema", ErrInvalidServingRecord)
			}
			csRaw, err := json.Marshal(cs)
			if err != nil {
				return fmt.Errorf("%w: invalid handler context schema", ErrInvalidServingRecord)
			}
			if err := contextSchemaOK(csRaw); err != nil {
				return err
			}
		}
	}
	return nil
}

// canonicalizeHandlerSpecs validates a release's typed handler table
// and returns it with canonical context_schema bytes, so identical
// declarations hash identically.
func canonicalizeHandlerSpecs(h map[string]runtime.HandlerSpec) (map[string]runtime.HandlerSpec, error) {
	if len(h) == 0 {
		return nil, nil
	}
	if len(h) > maxHandlers {
		return nil, fmt.Errorf("%w: too many declared handlers", ErrInvalidServingRecord)
	}
	out := make(map[string]runtime.HandlerSpec, len(h))
	for name, spec := range h {
		if !validHandlerName(name) {
			return nil, fmt.Errorf("%w: invalid handler name", ErrInvalidServingRecord)
		}
		if len(spec.ContextSchema) == 0 {
			out[name] = runtime.HandlerSpec{}
			continue
		}
		if err := contextSchemaOK(spec.ContextSchema); err != nil {
			return nil, err
		}
		obj, err := serving.DecodeJSON(spec.ContextSchema, serving.DefaultLimits())
		if err != nil {
			return nil, fmt.Errorf("%w: invalid handler context schema", ErrInvalidServingRecord)
		}
		canon, err := json.Marshal(obj)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid handler context schema", ErrInvalidServingRecord)
		}
		out[name] = runtime.HandlerSpec{ContextSchema: canon}
	}
	return out, nil
}
