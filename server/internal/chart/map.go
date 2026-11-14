package chart

// MapStrings walks a decoded JSON tree (map[string]any /
// []any / scalars, numbers as json.Number) and applies f to every
// string key and string value. It returns the rebuilt tree and whether
// anything changed. Used to redact known secrets from chart specs —
// replacing inside the decoded tree, never inside the JSON text, so
// escaped secrets cannot slip through.
func MapStrings(v any, f func(string) string) (any, bool) {
	changed := false
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			nk := f(k)
			if nk != k {
				changed = true
			}
			nv, ch := MapStrings(val, f)
			changed = changed || ch
			out[nk] = nv
		}
		return out, changed
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			ne, ch := MapStrings(e, f)
			changed = changed || ch
			out[i] = ne
		}
		return out, changed
	case string:
		if ns := f(t); ns != t {
			return ns, true
		}
		return t, false
	default:
		return v, false
	}
}
