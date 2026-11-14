// Package serving provides the bounded JSON contract foundation for
// persistent instances (EP-0014): a defensive JSON decoder with strict
// resource budgets, and a compiler/validator for the pinned subset of
// JSON Schema draft 2020-12 backed by
// github.com/santhosh-tekuri/jsonschema/v6, restricted by a thin
// subset walker.
//
// This package is pure Go with no dependency on Starlark, the runtime,
// or the store; it never executes user code and never performs network
// or file access. Bounding is by size and structure only -- input
// bytes, depth, node count and fixed schema caps; there is no
// execution/work budget. Error messages describe structure (byte
// offsets, member/array indexes) but never echo input values, because
// keys and values may carry secrets.
package serving

// Limits bounds JSON decoding. A non-positive field selects the
// DefaultLimits value for that field.
type Limits struct {
	// MaxBytes bounds the raw input length checked before parsing.
	MaxBytes int
	// MaxDepth bounds container nesting.
	MaxDepth int
	// MaxNodes bounds the total number of JSON values produced.
	MaxNodes int
}

// DefaultLimits returns the default budgets: 1 MiB of input, 32 levels
// of nesting and 10k nodes.
func DefaultLimits() Limits {
	return Limits{
		MaxBytes: 1 << 20,
		MaxDepth: 32,
		MaxNodes: 10_000,
	}
}

// Effective returns l with every non-positive field replaced by the
// DefaultLimits value.
func (l Limits) Effective() Limits {
	return l.withDefaults()
}

func (l Limits) withDefaults() Limits {
	d := DefaultLimits()
	if l.MaxBytes <= 0 {
		l.MaxBytes = d.MaxBytes
	}
	if l.MaxDepth <= 0 {
		l.MaxDepth = d.MaxDepth
	}
	if l.MaxNodes <= 0 {
		l.MaxNodes = d.MaxNodes
	}
	return l
}

// MaxNumberTokenBytes is the fixed cap on a JSON number token's length,
// enforced by DecodeJSON and by producers (e.g. the engine's Starlark
// to JSON converter) so that every supported output also decodes under
// the same limits.
const MaxNumberTokenBytes = 256

// Fixed caps for compiled schemas, applied on top of (never above) the
// caller's limits. Numbers are additionally capped per token: a number
// token may not exceed MaxNumberTokenBytes characters and its exponent
// may not exceed 1024 in magnitude, checked before any math/big
// allocation.
const (
	schemaMaxBytes = 64 << 10
	schemaMaxDepth = 32
	schemaMaxNodes = 2048
	schemaMaxAnyOf = 16

	numberMaxExponent = 1024

	// schemaMaxCount caps minLength/maxLength/minItems/maxItems: the
	// schema library stores them as platform int via int64
	// truncation, so unbounded values could silently wrap.
	schemaMaxCount = 1<<31 - 1
)
