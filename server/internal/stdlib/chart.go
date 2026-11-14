package stdlib

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	starjson "go.starlark.net/lib/json"
	"go.starlark.net/starlark"

	"calcside/internal/capability"
	"calcside/internal/chart"
)

// Vega-Lite spec primitives for io.display (EP-0024). Concrete chart
// types live in the contrib/charts extension; these builders only
// assemble and validate spec dicts.

var chartTypes = map[string]bool{
	"quantitative": true, "temporal": true, "ordinal": true, "nominal": true,
}

var chartDateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

func chartSpec(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var mark, data, encoding, transform, params, title, width, height starlark.Value
	if err := unpack(b, args, kwargs,
		"mark", &mark,
		"data?", &data,
		"encoding?", &encoding,
		"transform?", &transform,
		"params?", &params,
		"title?", &title,
		"width?", &width,
		"height?", &height,
	); err != nil {
		return nil, err
	}
	spec := map[string]any{
		"$schema": chart.SchemaURL,
		"width":   int64(480),
		"height":  int64(280),
		"config":  map[string]any{"font": "IBM Plex Sans"},
	}
	put := func(key string, v starlark.Value) error {
		if v == nil || v == starlark.None {
			return nil
		}
		gv, err := capability.JSONValue(v)
		if err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		spec[key] = gv
		return nil
	}
	if err := put("mark", mark); err != nil {
		return nil, err
	}
	if data != nil && data != starlark.None {
		rows, err := dataRows(data)
		if err != nil {
			return nil, fmt.Errorf("data: %w", err)
		}
		spec["data"] = map[string]any{"values": rows}
	} else {
		spec["data"] = map[string]any{"values": []any{}}
	}
	for _, kv := range []struct {
		key string
		v   starlark.Value
	}{
		{"encoding", encoding}, {"transform", transform}, {"params", params},
		{"title", title}, {"width", width}, {"height", height},
	} {
		if err := put(kv.key, kv.v); err != nil {
			return nil, err
		}
	}
	return chartDict(t, spec)
}

// dataRows converts a list/tuple of row dicts to []any of
// map[string]any; row shape is enforced by chart.Validate.
func dataRows(v starlark.Value) ([]any, error) {
	seq, err := sequence(v, chart.MaxRows)
	if err != nil {
		return nil, err
	}
	out := make([]any, 0, seq.Len())
	for i := 0; i < seq.Len(); i++ {
		row, err := capability.JSONValue(seq.Index(i))
		if err != nil {
			return nil, fmt.Errorf("row %d: %w", i, err)
		}
		out = append(out, row)
	}
	return out, nil
}

func chartField(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var name, typ string
	var title, aggregate, bin, timeUnit, scale, axis, legend, sort, format, stack starlark.Value
	if err := unpack(b, args, kwargs,
		"name", &name,
		"type", &typ,
		"title?", &title,
		"aggregate?", &aggregate,
		"bin?", &bin,
		"time_unit?", &timeUnit,
		"scale?", &scale,
		"axis?", &axis,
		"legend?", &legend,
		"sort?", &sort,
		"format?", &format,
		"stack?", &stack,
	); err != nil {
		return nil, err
	}
	if !chartTypes[typ] {
		return nil, fmt.Errorf("type must be quantitative, temporal, ordinal or nominal")
	}
	// A channel def is not a standalone spec, so it is not run through
	// chart.Validate; chart.spec/io.display validate the assembled spec.
	d := starlark.NewDict(12)
	_ = d.SetKey(starlark.String("field"), starlark.String(name))
	_ = d.SetKey(starlark.String("type"), starlark.String(typ))
	optional := []struct {
		key string
		v   starlark.Value
	}{
		{"title", title}, {"aggregate", aggregate}, {"bin", bin},
		{"timeUnit", timeUnit}, {"scale", scale}, {"axis", axis},
		{"legend", legend}, {"sort", sort}, {"format", format}, {"stack", stack},
	}
	for _, o := range optional {
		if o.v == nil || o.v == starlark.None {
			continue
		}
		_ = d.SetKey(starlark.String(o.key), o.v)
	}
	return d, nil
}

func chartInferType(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var rows starlark.Value
	var field string
	if err := unpack(b, args, kwargs, "rows", &rows, "field", &field); err != nil {
		return nil, err
	}
	seq, err := sequence(rows, MaxRows)
	if err != nil {
		return nil, err
	}
	typ := "nominal"
	for i := 0; i < seq.Len(); i++ {
		row, ok := seq.Index(i).(*starlark.Dict)
		if !ok {
			continue
		}
		v, found, err := row.Get(starlark.String(field))
		if err != nil || !found || v == starlark.None {
			continue
		}
		switch v := v.(type) {
		case starlark.Int, starlark.Float:
			typ = "quantitative"
		case starlark.String:
			s := string(v)
			if chartDateRe.MatchString(s) {
				if _, err := time.Parse("2006-01-02", s); err == nil {
					typ = "temporal"
				} else {
					typ = "nominal"
				}
			} else if _, err := time.Parse(time.RFC3339, s); err == nil {
				typ = "temporal"
			} else {
				typ = "nominal"
			}
		default:
			typ = "nominal"
		}
		break
	}
	return starlark.String(typ), nil
}

// chartCompose builds layer/hconcat/vconcat specs from child spec
// dicts, stripping their $schema/config and re-adding them at top.
func chartCompose(kind string) builtinFunc {
	return func(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		var title starlark.Value
		for _, kw := range kwargs {
			name, _ := kw[0].(starlark.String)
			if string(name) != "title" {
				return nil, fmt.Errorf("%s: unexpected keyword argument %q", b.Name(), name)
			}
			title = kw[1]
		}
		if len(args) == 0 {
			return nil, fmt.Errorf("%s: at least one spec is required", b.Name())
		}
		specs := make([]any, 0, len(args))
		for i, a := range args {
			gv, err := capability.JSONValue(a)
			if err != nil {
				return nil, fmt.Errorf("spec %d: %w", i, err)
			}
			m, ok := gv.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("spec %d: expected a spec dict", i)
			}
			delete(m, "$schema")
			delete(m, "config")
			specs = append(specs, m)
		}
		out := map[string]any{
			"$schema": chart.SchemaURL,
			"config":  map[string]any{"font": "IBM Plex Sans"},
			kind:      specs,
		}
		if title != nil && title != starlark.None {
			gv, err := capability.JSONValue(title)
			if err != nil {
				return nil, fmt.Errorf("title: %w", err)
			}
			out["title"] = gv
		}
		if kind == "layer" {
			if first, ok := specs[0].(map[string]any); ok {
				if w, ok := first["width"]; ok {
					out["width"] = w
				}
				if h, ok := first["height"]; ok {
					out["height"] = h
				}
			}
		} else {
			out["width"] = int64(480)
			out["height"] = int64(280)
		}
		return chartDict(t, out)
	}
}

func chartValidate(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var spec starlark.Value
	if err := unpack(b, args, kwargs, "spec", &spec); err != nil {
		return nil, err
	}
	gv, err := capability.JSONValue(spec)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(gv)
	if err != nil {
		return nil, fmt.Errorf("spec: %w", err)
	}
	canon, err := chart.Validate(raw)
	if err != nil {
		return nil, err
	}
	return decodeJSON(t, canon)
}

func chartSummary(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var spec starlark.Value
	if err := unpack(b, args, kwargs, "spec", &spec); err != nil {
		return nil, err
	}
	gv, err := capability.JSONValue(spec)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(gv)
	if err != nil {
		return nil, fmt.Errorf("spec: %w", err)
	}
	return starlark.String(chart.Summary(raw)), nil
}

// chartDict validates spec and returns it as a starlark dict decoded
// from the canonical JSON, so callers always see exactly what
// io.display will store.
func chartDict(t *starlark.Thread, spec map[string]any) (starlark.Value, error) {
	raw, err := json.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("spec: %w", err)
	}
	canon, err := chart.Validate(raw)
	if err != nil {
		return nil, err
	}
	return decodeJSON(t, canon)
}

func decodeJSON(t *starlark.Thread, raw []byte) (starlark.Value, error) {
	dec, ok := starjson.Module.Members["decode"].(*starlark.Builtin)
	if !ok {
		return nil, errors.New("json.decode unavailable")
	}
	return starlark.Call(t, dec, starlark.Tuple{starlark.String(string(raw))}, nil)
}
