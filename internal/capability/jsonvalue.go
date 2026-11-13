package capability

import (
	"encoding/json"
	"fmt"

	"go.starlark.net/starlark"
)

// JSONValue converts a Starlark value into a JSON-able Go value:
// map[string]any, []any, string, int64/json.Number, float64, bool or
// nil. Dict keys must be strings; bytes, functions and other types are
// rejected. Used by capability ops and stdlib constructors that need a
// faithful JSON view of script data (never for audit metadata, which
// must stay small — see ext.gateArgValue).
func JSONValue(v starlark.Value) (any, error) {
	switch t := v.(type) {
	case starlark.NoneType:
		return nil, nil
	case starlark.Bool:
		return bool(t), nil
	case starlark.String:
		return string(t), nil
	case starlark.Int:
		if i, ok := t.Int64(); ok {
			return i, nil
		}
		return json.Number(t.String()), nil
	case starlark.Float:
		return float64(t), nil
	case *starlark.Dict:
		out := make(map[string]any, t.Len())
		for _, item := range t.Items() {
			k, ok := item[0].(starlark.String)
			if !ok {
				return nil, fmt.Errorf("dict key must be a string, got %s", item[0].Type())
			}
			ev, err := JSONValue(item[1])
			if err != nil {
				return nil, err
			}
			out[string(k)] = ev
		}
		return out, nil
	case *starlark.List:
		out := make([]any, 0, t.Len())
		for i := 0; i < t.Len(); i++ {
			ev, err := JSONValue(t.Index(i))
			if err != nil {
				return nil, err
			}
			out = append(out, ev)
		}
		return out, nil
	case starlark.Tuple:
		out := make([]any, 0, len(t))
		for _, e := range t {
			ev, err := JSONValue(e)
			if err != nil {
				return nil, err
			}
			out = append(out, ev)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unsupported value type %s", v.Type())
	}
}
