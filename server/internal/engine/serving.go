package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"unicode/utf8"

	"go.starlark.net/starlark"

	"calcside/internal/serving"
)

// MarshalJSONValue converts a Starlark value to bounded JSON by direct
// traversal -- never via String() -- enforcing depth, node and encoded
// byte budgets during conversion, before any unbounded Go value or
// encoding is produced. Supported: None, bool, string (valid UTF-8
// only), int (arbitrary precision, size-compared against a bounded
// threshold before decimal rendering), finite float, list, tuple and
// dict with string keys. Non-finite floats, cycles (same container on
// the active path; repeated acyclic aliases are allowed), sets, bytes,
// functions and every other opaque value or non-string key are
// rejected. Every emitted number token is capped at
// serving.MaxNumberTokenBytes so converter output always decodes under
// the same limits.
//
// Secret redaction is deliberately NOT integrated here: the caller
// must redact known secrets from the returned raw JSON and validate
// the redacted output against the output schema -- a redaction that
// breaks the schema is an invalid_output error, not a success.
func MarshalJSONValue(v starlark.Value, limits serving.Limits) (json.RawMessage, error) {
	l := limits.Effective()
	w := &jwriter{max: l.MaxBytes, maxDepth: l.MaxDepth, maxNodes: l.MaxNodes, active: map[any]bool{}}
	if err := w.value(v, 0); err != nil {
		return nil, err
	}
	return json.RawMessage(w.buf), nil
}

type jwriter struct {
	buf      []byte
	max      int
	nodes    int
	maxDepth int
	maxNodes int
	// active holds containers on the current traversal path for cycle
	// detection. Keys are *starlark.List and *starlark.Dict identities
	// plus tuple backing-store addresses (all cycles pass through a
	// mutable container, so tuple tracking is belt-and-braces).
	active map[any]bool
}

func (w *jwriter) emit(b []byte) error {
	if len(w.buf)+len(b) > w.max {
		return errors.New("json output exceeds byte limit")
	}
	w.buf = append(w.buf, b...)
	return nil
}

func (w *jwriter) node() error {
	w.nodes++
	if w.nodes > w.maxNodes {
		return errors.New("json output exceeds node limit")
	}
	return nil
}

func (w *jwriter) value(v starlark.Value, depth int) error {
	if err := w.node(); err != nil {
		return err
	}
	switch t := v.(type) {
	case starlark.NoneType:
		return w.emit([]byte("null"))
	case starlark.Bool:
		if t {
			return w.emit([]byte("true"))
		}
		return w.emit([]byte("false"))
	case starlark.String:
		s := string(t)
		if len(s) > w.max-len(w.buf) {
			return errors.New("json output exceeds byte limit")
		}
		if !utf8.ValidString(s) {
			return errors.New("string is not valid utf-8")
		}
		enc, err := json.Marshal(s)
		if err != nil {
			return err
		}
		return w.emit(enc)
	case starlark.Int:
		if i, ok := t.Int64(); ok {
			return w.emit(strconv.AppendInt(nil, i, 10))
		}
		// Certain-rejection preflight without copying the operand:
		// |v| >= 2^(4*rem) needs more than rem decimal digits
		// (digits >= 0.3*(bitlen-1)). Cmp compares against a threshold
		// instead of BigInt, which would copy the whole value. rem is
		// capped at the fixed number-token bound so the threshold
		// stays <= 1024 bits per value.
		rem := min(w.max-len(w.buf), serving.MaxNumberTokenBytes)
		if rem <= 0 {
			return errors.New("json output exceeds byte limit")
		}
		lim := new(big.Int).Lsh(big.NewInt(1), uint(4*rem))
		if t.Sign() >= 0 {
			c, _ := t.Cmp(starlark.MakeBigInt(lim), 0)
			if c >= 0 {
				return errors.New("json output exceeds byte limit")
			}
		} else {
			c, _ := t.Cmp(starlark.MakeBigInt(new(big.Int).Neg(lim)), 0)
			if c <= 0 {
				return errors.New("json output exceeds byte limit")
			}
		}
		text := t.BigInt().Text(10)
		if len(text) > rem {
			return errors.New("json output exceeds byte limit")
		}
		return w.emit([]byte(text))
	case starlark.Float:
		f := float64(t)
		if math.IsInf(f, 0) || math.IsNaN(f) {
			return errors.New("non-finite float")
		}
		return w.emit(strconv.AppendFloat(nil, f, 'g', -1, 64))
	case *starlark.List:
		return w.array(t, t.Len(), func(i int) starlark.Value { return t.Index(i) }, depth)
	case starlark.Tuple:
		return w.array(tupleKey(t), t.Len(), func(i int) starlark.Value { return t[i] }, depth)
	case *starlark.Dict:
		return w.dict(t, depth)
	default:
		return fmt.Errorf("non-JSON value of type %s", v.Type())
	}
}

// tupleKey identifies a tuple's backing store for active-path tracking;
// empty tuples cannot participate in cycles and return nil.
func tupleKey(t starlark.Tuple) any {
	if len(t) == 0 {
		return nil
	}
	return &t[0]
}

// array writes a JSON array for list/tuple containers. key is the
// cycle-detection identity, nil when the value cannot cycle.
func (w *jwriter) array(key any, n int, at func(int) starlark.Value, depth int) error {
	if depth >= w.maxDepth {
		return errors.New("json output exceeds depth limit")
	}
	if n > w.maxNodes-w.nodes {
		return errors.New("json output exceeds node limit")
	}
	if key != nil {
		if w.active[key] {
			return errors.New("cyclic value")
		}
		w.active[key] = true
		defer delete(w.active, key)
	}
	if err := w.emit([]byte("[")); err != nil {
		return err
	}
	for i := 0; i < n; i++ {
		if i > 0 {
			if err := w.emit([]byte(",")); err != nil {
				return err
			}
		}
		if err := w.value(at(i), depth+1); err != nil {
			return err
		}
	}
	return w.emit([]byte("]"))
}

func (w *jwriter) dict(d *starlark.Dict, depth int) error {
	if depth >= w.maxDepth {
		return errors.New("json output exceeds depth limit")
	}
	// Cardinality lower bound before traversal: every member costs at
	// least one node, so an oversized dict fails before any work.
	if d.Len() > w.maxNodes-w.nodes {
		return errors.New("json output exceeds node limit")
	}
	if w.active[d] {
		return errors.New("cyclic value")
	}
	w.active[d] = true
	defer delete(w.active, d)
	if err := w.emit([]byte("{")); err != nil {
		return err
	}
	i := 0
	for k, v := range d.Entries() {
		ks, ok := k.(starlark.String)
		if !ok {
			return fmt.Errorf("non-string dict key of type %s", k.Type())
		}
		if len(ks) > w.max-len(w.buf) {
			return errors.New("json output exceeds byte limit")
		}
		if !utf8.ValidString(string(ks)) {
			return errors.New("dict key is not valid utf-8")
		}
		if i > 0 {
			if err := w.emit([]byte(",")); err != nil {
				return err
			}
		}
		i++
		enc, err := json.Marshal(string(ks))
		if err != nil {
			return err
		}
		if err := w.emit(enc); err != nil {
			return err
		}
		if err := w.emit([]byte(":")); err != nil {
			return err
		}
		if err := w.value(v, depth+1); err != nil {
			return err
		}
	}
	return w.emit([]byte("}"))
}
