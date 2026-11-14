// Package chart validates, canonicalizes and summarizes the Vega-Lite
// spec subset produced by the chart stdlib module and io.display
// (EP-0024). It is pure Go — no Starlark — so the worker, the API tier
// and tests share one allowlist.
package chart

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Fixed ceilings for one exec's displays and one spec's tree.
const (
	MaxDisplays    = 8
	MaxSpecBytes   = 256 << 10
	MaxTotalBytes  = 1 << 20
	MaxRows        = 5000
	MaxDepth       = 32
	MaxStringBytes = 1024
	MaxCanvas      = 2000

	// Kind is the only display kind Phase 1 emits.
	Kind = "vega-lite"
	// SchemaURL is the $schema value chart.* builders stamp.
	SchemaURL = "https://vega.github.io/schema/vega-lite/v6.json"
)

const schemaPrefix = "https://vega.github.io/schema/vega-lite/v6"

// formatRe bounds `format` specifiers to d3 number/time format
// characters; anything else is rejected.
var formatRe = regexp.MustCompile(`^[%a-zA-Z0-9 ,.:/\-+$#~()\[\]_']{0,64}$`)

// viewKeys are the only keys allowed at the top level and inside every
// sub-view (layer/hconcat/vconcat elements and facet specs).
var viewKeys = map[string]bool{
	"$schema": true, "title": true, "description": true,
	"width": true, "height": true, "autosize": true, "padding": true,
	"background": true, "data": true, "mark": true, "encoding": true,
	"transform": true, "params": true, "layer": true, "hconcat": true,
	"vconcat": true, "facet": true, "spec": true, "resolve": true,
	"config": true,
}

// deniedKeys are rejected anywhere in the spec tree: fetch targets,
// expression/eval hooks and widget/signal machinery. Keys ending in
// "Expr" are denied separately.
var deniedKeys = map[string]bool{
	"url": true, "href": true, "expr": true, "signal": true,
	"calculate": true, "test": true, "usermeta": true, "datasets": true,
	"loader": true, "formatType": true, "lookup": true, "$ref": true,
	"on": true, "update": true, "encode": true,
}

func denied(k string) bool {
	return deniedKeys[k] || strings.HasSuffix(k, "Expr")
}

var markTypes = map[string]bool{
	"line": true, "area": true, "bar": true, "point": true,
	"circle": true, "square": true, "tick": true, "rect": true,
	"rule": true, "text": true, "boxplot": true, "errorbar": true,
	"errorband": true,
}

var encodingChannels = map[string]bool{
	"x": true, "y": true, "x2": true, "y2": true,
	"color": true, "opacity": true, "size": true, "shape": true,
	"strokeDash": true, "text": true, "tooltip": true, "detail": true,
	"order": true, "row": true, "column": true, "facet": true,
}

// transformOps maps each allowed transform op to its sibling keys.
// `on` is absent everywhere (globally denied), which also bars the
// regression/loess independent-variable key.
var transformOps = map[string]map[string]bool{
	"aggregate":     {"as": true, "drop": true, "field": true, "fields": true, "groupby": true, "op": true, "ops": true},
	"bin":           {"as": true, "base": true, "divide": true, "extent": true, "field": true, "maxbins": true, "nice": true, "step": true},
	"timeUnit":      {"as": true, "field": true, "interval": true, "timeUnit": true, "timeZone": true, "unit": true, "units": true},
	"fold":          {"as": true, "fields": true},
	"stack":         {"as": true, "field": true, "groupby": true, "offset": true, "sort": true},
	"window":        {"as": true, "frame": true, "groupby": true, "ignorePeers": true, "ops": true, "sort": true},
	"joinaggregate": {"groupby": true, "ops": true, "sort": true},
	"density":       {"bandwidth": true, "counts": true, "cumulative": true, "extent": true, "groupby": true, "maxsteps": true, "minsteps": true, "steps": true},
	"regression":    {"extent": true, "groupby": true, "loess": true, "method": true, "order": true, "params": true},
	"loess":         {"bandwidth": true, "extent": true, "groupby": true, "method": true, "order": true, "params": true},
	"quantile":      {"as": true, "groupby": true, "probs": true},
	"filter":        {},
}

// filterOps are the comparison keys of a field predicate.
var filterOps = map[string]bool{
	"equal": true, "lt": true, "lte": true, "gt": true, "gte": true,
	"range": true, "oneOf": true, "valid": true,
}

// selectKeys bounds a params select object; `on`, `value`, `expr`,
// `update` and `react` are not representable.
var selectKeys = map[string]bool{
	"type": true, "encodings": true, "fields": true, "clear": true,
}

var paramKeys = map[string]bool{"name": true, "select": true, "bind": true}

var configKeys = map[string]bool{
	"axis": true, "axisX": true, "axisY": true, "axisBand": true,
	"legend": true, "view": true, "range": true, "font": true,
	"title": true, "header": true,
	"mark": true, "line": true, "bar": true, "point": true,
	"area": true, "rect": true, "text": true, "tick": true,
	"rule": true, "circle": true, "square": true,
	"background": true, "padding": true, "autosize": true, "style": true,
}

// facetKeys bounds a facet field def / row-column mapping; spec and
// data recurse into the view/data rules.
var facetKeys = map[string]bool{
	"field": true, "type": true, "bin": true, "timeUnit": true,
	"header": true, "sort": true,
	"row": true, "column": true,
	"data": true, "spec": true, "name": true, "resolve": true,
}

// Validate decodes spec, enforces the size/depth/string/row bounds and
// the key allowlist, and returns the canonical re-marshaled JSON.
// Errors name the JSON path of the offending node.
func Validate(spec []byte) ([]byte, error) {
	if len(spec) == 0 {
		return nil, fmt.Errorf("spec: empty")
	}
	if len(spec) > MaxSpecBytes {
		return nil, fmt.Errorf("spec: %d bytes exceeds the %d byte limit", len(spec), MaxSpecBytes)
	}
	if d := bracketDepth(spec); d > MaxDepth {
		return nil, fmt.Errorf("spec: nesting deeper than %d levels", MaxDepth)
	}
	dec := json.NewDecoder(bytes.NewReader(spec))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("spec: %v", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("spec: trailing data after the JSON value")
	}
	w := &walker{}
	if err := w.view(v, "spec", true); err != nil {
		return nil, err
	}
	if w.rows > MaxRows {
		return nil, fmt.Errorf("spec.data: %d rows exceeds the %d row limit", w.rows, MaxRows)
	}
	out, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("spec: %v", err)
	}
	return out, nil
}

// bracketDepth is a cheap pre-decode bound on JSON nesting so a
// deeply nested payload cannot overflow the recursive decoder.
func bracketDepth(b []byte) int {
	depth, max := 0, 0
	inStr, esc := false, false
	for _, c := range b {
		if inStr {
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{', '[':
			depth++
			if depth > max {
				max = depth
			}
		case '}', ']':
			depth--
		}
	}
	return max
}

type walker struct{ rows int }

func join(path, key string) string { return path + "." + key }

func index(path string, i int) string { return fmt.Sprintf("%s[%d]", path, i) }

// view validates one spec/view object. top marks the document root,
// the only place $schema and config may appear.
func (w *walker) view(v any, path string, top bool) error {
	m, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("%s: expected an object", path)
	}
	if _, hasSpec := m["spec"]; hasSpec {
		if _, hasFacet := m["facet"]; !hasFacet {
			return fmt.Errorf("%s.spec: spec is only valid alongside facet", path)
		}
	}
	for k, val := range m {
		child := join(path, k)
		if denied(k) || !viewKeys[k] {
			return fmt.Errorf("%s: key %q is not allowed", child, k)
		}
		var err error
		switch k {
		case "$schema":
			err = schemaCheck(val, child, top)
		case "width", "height":
			err = sizeCheck(val, child)
		case "data":
			err = w.data(val, child)
		case "mark":
			err = w.mark(val, child)
		case "encoding":
			err = w.encoding(val, child)
		case "transform":
			err = w.transform(val, child)
		case "params":
			err = w.params(val, child)
		case "layer", "hconcat", "vconcat":
			err = w.views(val, child)
		case "facet":
			err = w.facet(val, child)
		case "spec":
			err = w.view(val, child, false)
		case "config":
			err = w.config(val, child, top)
		default:
			err = w.generic(val, child)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func schemaCheck(v any, path string, top bool) error {
	if !top {
		return fmt.Errorf("%s: $schema is only valid at the top level", path)
	}
	s, ok := v.(string)
	if !ok || !strings.HasPrefix(s, schemaPrefix) {
		return fmt.Errorf("%s: $schema must be %s", path, SchemaURL)
	}
	return nil
}

func sizeCheck(v any, path string) error {
	switch t := v.(type) {
	case string:
		if t != "container" {
			return fmt.Errorf("%s: width/height must be a number or \"container\"", path)
		}
	case json.Number:
		f, err := strconv.ParseFloat(t.String(), 64)
		if err != nil || math.IsInf(f, 0) || math.IsNaN(f) || f < 0 || f > MaxCanvas {
			return fmt.Errorf("%s: width/height must be a finite number in [0, %d]", path, MaxCanvas)
		}
	default:
		return fmt.Errorf("%s: width/height must be a number or \"container\"", path)
	}
	return nil
}

// views validates an array of sub-views (layer/hconcat/vconcat).
func (w *walker) views(v any, path string) error {
	arr, ok := v.([]any)
	if !ok {
		return fmt.Errorf("%s: expected an array of views", path)
	}
	for i, e := range arr {
		if err := w.view(e, index(path, i), false); err != nil {
			return err
		}
	}
	return nil
}

// data enforces data.values-only inline data and counts rows.
func (w *walker) data(v any, path string) error {
	m, ok := v.(map[string]any)
	if !ok || len(m) != 1 {
		return fmt.Errorf("%s: data must be an object with only \"values\"", path)
	}
	raw, hasValues := m["values"]
	if !hasValues {
		return fmt.Errorf("%s: data must be an object with only \"values\"", path)
	}
	vals, ok := raw.([]any)
	if !ok {
		return fmt.Errorf("%s.values: expected an array", join(path, "values"))
	}
	for i, row := range vals {
		rp := index(join(path, "values"), i)
		if rm, isObj := row.(map[string]any); isObj {
			// Row keys are inert column names, not spec structure —
			// the global deny list does not apply (real data has
			// columns named url, test, on, ...).
			for k, rv := range rm {
				if err := scalar(rv, join(rp, k)); err != nil {
					return err
				}
			}
			continue
		}
		if err := scalar(row, rp); err != nil {
			return err
		}
	}
	w.rows += len(vals)
	return nil
}

// scalar bounds one leaf value inside a data row.
func scalar(v any, path string) error {
	switch t := v.(type) {
	case string:
		if len(t) > MaxStringBytes {
			return fmt.Errorf("%s: string exceeds %d bytes", path, MaxStringBytes)
		}
	case json.Number:
		f, err := strconv.ParseFloat(t.String(), 64)
		if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
			return fmt.Errorf("%s: number %q is not finite", path, t.String())
		}
	case map[string]any, []any:
		return fmt.Errorf("%s: row values must be scalar", path)
	}
	return nil
}

func (w *walker) mark(v any, path string) error {
	switch t := v.(type) {
	case string:
		if !markTypes[t] {
			return fmt.Errorf("%s: mark type %q is not allowed", path, t)
		}
	case map[string]any:
		typ, ok := t["type"].(string)
		if !ok || !markTypes[typ] {
			return fmt.Errorf("%s.type: mark type %q is not allowed", path, typ)
		}
		for k, val := range t {
			if k == "type" {
				continue
			}
			child := join(path, k)
			if denied(k) {
				return fmt.Errorf("%s: key %q is not allowed", child, k)
			}
			if err := w.generic(val, child); err != nil {
				return err
			}
		}
	case []any:
		for i, e := range t {
			if err := w.mark(e, index(path, i)); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("%s: mark must be a string or object", path)
	}
	return nil
}

func (w *walker) encoding(v any, path string) error {
	m, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("%s: expected an object of channels", path)
	}
	for k, val := range m {
		if !encodingChannels[k] {
			return fmt.Errorf("%s: encoding channel %q is not allowed", join(path, k), k)
		}
		if err := w.channel(val, join(path, k)); err != nil {
			return err
		}
	}
	return nil
}

// channel validates one encoding channel def: an object, or an array
// of objects/field-name strings (tooltip/detail style).
func (w *walker) channel(v any, path string) error {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			child := join(path, k)
			if denied(k) {
				return fmt.Errorf("%s: key %q is not allowed", child, k)
			}
			if err := w.generic(val, child); err != nil {
				return err
			}
			if k == "condition" {
				if err := w.condition(val, child); err != nil {
					return err
				}
			}
		}
	case []any:
		for i, e := range t {
			ep := index(path, i)
			switch e.(type) {
			case string:
				if err := scalar(e, ep); err != nil {
					return err
				}
			case map[string]any:
				if err := w.channel(e, ep); err != nil {
					return err
				}
			default:
				return fmt.Errorf("%s: channel def must be an object or field name", ep)
			}
		}
	default:
		return fmt.Errorf("%s: channel def must be an object", path)
	}
	return nil
}

// condition requires a param-driven condition; `test` is caught by the
// global deny during the generic walk.
func (w *walker) condition(v any, path string) error {
	check := func(x any, p string) error {
		m, ok := x.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: condition must be an object", p)
		}
		if _, ok := m["param"]; !ok {
			return fmt.Errorf("%s: condition requires param", p)
		}
		return nil
	}
	if arr, ok := v.([]any); ok {
		for i, e := range arr {
			if err := check(e, index(path, i)); err != nil {
				return err
			}
		}
		return nil
	}
	return check(v, path)
}

func (w *walker) transform(v any, path string) error {
	arr, ok := v.([]any)
	if !ok {
		return fmt.Errorf("%s: expected an array", path)
	}
	for i, item := range arr {
		ip := index(path, i)
		m, ok := item.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: transform must be an object", ip)
		}
		op := ""
		ops := 0
		for k := range m {
			if _, isOp := transformOps[k]; isOp {
				op, ops = k, ops+1
			}
		}
		if ops != 1 {
			return fmt.Errorf("%s: transform item must have exactly one operation", ip)
		}
		for k, val := range m {
			child := join(ip, k)
			if k == op {
				if op == "filter" {
					if err := w.filterPred(val, child); err != nil {
						return err
					}
				} else if err := w.generic(val, child); err != nil {
					return err
				}
				continue
			}
			if !transformOps[op][k] {
				return fmt.Errorf("%s: key %q is not allowed for %s", child, k, op)
			}
			if err := w.generic(val, child); err != nil {
				return err
			}
		}
	}
	return nil
}

// filterPred accepts only structured predicates: {param}, field
// comparisons, and and/or/not compositions — never a string.
func (w *walker) filterPred(v any, path string) error {
	m, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("%s: filter must be an object predicate", path)
	}
	if p, hasParam := m["param"]; hasParam {
		for k := range m {
			if k != "param" {
				return fmt.Errorf("%s: key %q is not allowed in a param predicate", join(path, k), k)
			}
		}
		if _, ok := p.(string); !ok {
			return fmt.Errorf("%s.param: expected a param name", join(path, "param"))
		}
		return w.generic(p, join(path, "param"))
	}
	logic := false
	for _, lk := range []string{"and", "or", "not"} {
		if val, ok := m[lk]; ok {
			logic = true
			if lk == "not" {
				if err := w.filterPred(val, join(path, lk)); err != nil {
					return err
				}
			} else {
				arr, ok := val.([]any)
				if !ok {
					return fmt.Errorf("%s: %s must be an array of predicates", join(path, lk), lk)
				}
				for i, e := range arr {
					if err := w.filterPred(e, index(join(path, lk), i)); err != nil {
						return err
					}
				}
			}
		}
	}
	if logic {
		for k := range m {
			if k != "and" && k != "or" && k != "not" {
				return fmt.Errorf("%s: key %q is not allowed in a logical predicate", join(path, k), k)
			}
		}
		return nil
	}
	if _, ok := m["field"]; !ok {
		return fmt.Errorf("%s: filter predicate requires field", path)
	}
	hasOp := false
	for k, val := range m {
		child := join(path, k)
		switch {
		case k == "field" || k == "timeUnit":
		case filterOps[k]:
			hasOp = true
		default:
			return fmt.Errorf("%s: key %q is not allowed in a filter predicate", child, k)
		}
		if err := w.generic(val, child); err != nil {
			return err
		}
	}
	if !hasOp {
		return fmt.Errorf("%s: filter predicate requires a comparison", path)
	}
	return nil
}

func (w *walker) params(v any, path string) error {
	arr, ok := v.([]any)
	if !ok {
		return fmt.Errorf("%s: expected an array", path)
	}
	for i, item := range arr {
		ip := index(path, i)
		m, ok := item.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: param must be an object", ip)
		}
		for k := range m {
			if !paramKeys[k] {
				return fmt.Errorf("%s: key %q is not allowed", join(ip, k), k)
			}
		}
		name, _ := m["name"].(string)
		if name == "" {
			return fmt.Errorf("%s.name: param requires a name", join(ip, "name"))
		}
		if sel, ok := m["select"]; ok {
			if err := selectDef(sel, join(ip, "select")); err != nil {
				return err
			}
		}
		if b, ok := m["bind"]; ok {
			s, ok := b.(string)
			if !ok || (s != "scales" && s != "legend") {
				return fmt.Errorf("%s.bind: must be \"scales\" or \"legend\"", join(ip, "bind"))
			}
		}
	}
	return nil
}

func selectDef(v any, path string) error {
	switch t := v.(type) {
	case string:
		if t != "point" && t != "interval" {
			return fmt.Errorf("%s: select must be \"point\" or \"interval\"", path)
		}
		return nil
	case map[string]any:
		for k := range t {
			if !selectKeys[k] {
				return fmt.Errorf("%s: key %q is not allowed", join(path, k), k)
			}
		}
		typ, _ := t["type"].(string)
		if typ != "point" && typ != "interval" {
			return fmt.Errorf("%s.type: select type must be \"point\" or \"interval\"", join(path, "type"))
		}
		for _, k := range []string{"encodings", "fields"} {
			if val, ok := t[k]; ok {
				arr, ok := val.([]any)
				if !ok {
					return fmt.Errorf("%s: %s must be an array of strings", join(path, k), k)
				}
				for i, e := range arr {
					if _, ok := e.(string); !ok {
						return fmt.Errorf("%s: %s[%d] must be a string", index(join(path, k), i), k, i)
					}
				}
			}
		}
		if c, ok := t["clear"]; ok {
			if b, ok := c.(bool); !ok || b {
				return fmt.Errorf("%s.clear: only false is allowed", join(path, "clear"))
			}
		}
		return nil
	default:
		return fmt.Errorf("%s: select must be \"point\", \"interval\" or an object", path)
	}
}

func (w *walker) config(v any, path string, top bool) error {
	if !top {
		return fmt.Errorf("%s: config is only valid at the top level", path)
	}
	m, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("%s: expected an object", path)
	}
	for k, val := range m {
		if !configKeys[k] {
			return fmt.Errorf("%s: config key %q is not allowed", join(path, k), k)
		}
		if err := w.generic(val, join(path, k)); err != nil {
			return err
		}
	}
	return nil
}

// facet validates a facet field def or row/column mapping; nested
// spec/data recurse into the view/data rules.
func (w *walker) facet(v any, path string) error {
	m, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("%s: facet must be an object", path)
	}
	for k, val := range m {
		child := join(path, k)
		if denied(k) || !facetKeys[k] {
			return fmt.Errorf("%s: key %q is not allowed", child, k)
		}
		var err error
		switch k {
		case "row", "column":
			err = w.facet(val, child)
		case "data":
			err = w.data(val, child)
		case "spec":
			err = w.view(val, child, false)
		default:
			err = w.generic(val, child)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// generic walks a free-form subtree: only the global denies, the
// format-string rule, scalar bounds and depth apply.
func (w *walker) generic(v any, path string) error {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			child := join(path, k)
			if denied(k) {
				return fmt.Errorf("%s: key %q is not allowed", child, k)
			}
			if k == "format" {
				if s, ok := val.(string); ok {
					if !formatRe.MatchString(s) {
						return fmt.Errorf("%s: format %q is not an allowed d3 format", child, s)
					}
					continue
				}
			}
			if err := w.generic(val, child); err != nil {
				return err
			}
		}
	case []any:
		for i, e := range t {
			if err := w.generic(e, index(path, i)); err != nil {
				return err
			}
		}
	case string:
		if len(t) > MaxStringBytes {
			return fmt.Errorf("%s: string exceeds %d bytes", path, MaxStringBytes)
		}
	case json.Number:
		f, err := strconv.ParseFloat(t.String(), 64)
		if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
			return fmt.Errorf("%s: number %q is not finite", path, t.String())
		}
	}
	return nil
}
