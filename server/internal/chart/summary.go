package chart

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Summary renders a one-line human description of a validated spec:
// mark/composition kind, total inline rows, and the min/max range of
// the top-level x/y fields. It never echoes row content.
func Summary(spec []byte) string {
	v, datas, ok := decode(spec)
	if !ok {
		return "invalid spec"
	}
	m, ok := v.(map[string]any)
	if !ok {
		return "invalid spec"
	}
	rows := 0
	for _, vals := range datas {
		rows += len(vals)
	}
	var b strings.Builder
	b.WriteString(kindOf(m))
	if rows > 0 {
		fmt.Fprintf(&b, ", %d rows", rows)
	}
	enc, _ := m["encoding"].(map[string]any)
	for _, ch := range []string{"x", "y"} {
		field := channelField(enc[ch])
		if field == "" {
			continue
		}
		lo, hi, ok := fieldRange(datas, field)
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "; %s=%s [%s .. %s]", ch, truncate(field, 40), lo, hi)
	}
	return b.String()
}

// KindRows reports the spec's mark/composition name and total inline
// row count, for capability call metadata. Both degrade to ""/0 on
// invalid JSON.
func KindRows(spec []byte) (kind string, rows int) {
	v, datas, ok := decode(spec)
	if !ok {
		return "", 0
	}
	m, ok := v.(map[string]any)
	if !ok {
		return "", 0
	}
	for _, vals := range datas {
		rows += len(vals)
	}
	return kindOf(m), rows
}

func decode(spec []byte) (v any, datas [][]any, ok bool) {
	dec := json.NewDecoder(bytes.NewReader(spec))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, nil, false
	}
	collectData(v, &datas)
	return v, datas, true
}

func kindOf(m map[string]any) string {
	if mark, ok := m["mark"]; ok {
		return markName(mark)
	}
	for _, k := range []string{"layer", "hconcat", "vconcat"} {
		if arr, ok := m[k].([]any); ok {
			return fmt.Sprintf("%s(%d)", k, len(arr))
		}
	}
	if _, ok := m["facet"]; ok {
		return "facet"
	}
	return "chart"
}

func markName(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case map[string]any:
		if s, ok := t["type"].(string); ok {
			return s
		}
	case []any:
		if len(t) > 0 {
			return markName(t[0])
		}
	}
	return "mark"
}

// collectData gathers every data.values array in the tree.
func collectData(v any, out *[][]any) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if k == "data" {
				if dm, ok := val.(map[string]any); ok {
					if vals, ok := dm["values"].([]any); ok {
						*out = append(*out, vals)
					}
				}
			}
			collectData(val, out)
		}
	case []any:
		for _, e := range t {
			collectData(e, out)
		}
	}
}

// channelField extracts the field name from a channel def (object,
// field-name string inside an array, or nested condition-free def).
func channelField(v any) string {
	get := func(m map[string]any) string {
		if f, ok := m["field"].(string); ok {
			return f
		}
		return ""
	}
	switch t := v.(type) {
	case map[string]any:
		return get(t)
	case []any:
		for _, e := range t {
			if m, ok := e.(map[string]any); ok {
				if f := get(m); f != "" {
					return f
				}
			}
		}
	}
	return ""
}

// fieldRange returns the min/max of field across all inline rows.
// Numbers compare numerically and render shortest-form; strings
// compare lexicographically. Mixed numeric+string data prefers
// numeric; booleans/nulls are skipped.
func fieldRange(datas [][]any, field string) (lo, hi string, ok bool) {
	var numLo, numHi float64
	var strLo, strHi string
	hasNum, hasStr := false, false
	for _, vals := range datas {
		for _, row := range vals {
			rm, isObj := row.(map[string]any)
			if !isObj {
				continue
			}
			rv, present := rm[field]
			if !present {
				continue
			}
			switch t := rv.(type) {
			case json.Number:
				f, err := strconv.ParseFloat(t.String(), 64)
				if err != nil {
					continue
				}
				if !hasNum || f < numLo {
					numLo = f
				}
				if !hasNum || f > numHi {
					numHi = f
				}
				hasNum = true
			case string:
				if !hasStr || t < strLo {
					strLo = t
				}
				if !hasStr || t > strHi {
					strHi = t
				}
				hasStr = true
			}
		}
	}
	switch {
	case hasNum:
		return formatNum(numLo), formatNum(numHi), true
	case hasStr:
		return truncate(strLo, 48), truncate(strHi, 48), true
	}
	return "", "", false
}

func formatNum(f float64) string {
	return truncate(strconv.FormatFloat(f, 'g', -1, 64), 24)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
