package ext

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.starlark.net/starlark"

	"calcside/internal/capability"
	"calcside/internal/chart"
)

// loadCharts initializes contrib/charts@0.1.0 with no base bindings —
// the extension builds specs purely on stdlib chart.* primitives.
func loadCharts(t *testing.T) starlark.StringDict {
	t.Helper()
	l := &CapabilityLoader{
		Identifier: CapabilityIdentifier("contrib/charts@0.1.0"),
		Options:    &Options{LocalRoots: []string{filepath.Join("..", "..", "..", "contrib")}},
	}
	m, err := l.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	g, err := m.init(context.Background(), nil, nil, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func eval(t *testing.T, src string) starlark.Value {
	t.Helper()
	v, err := starlark.Eval(&starlark.Thread{Name: "test"}, "<test>", src, nil)
	if err != nil {
		t.Fatalf("eval %q: %v", src, err)
	}
	return v
}

func callCharts(t *testing.T, g starlark.StringDict, name string, args ...starlark.Value) (starlark.Value, error) {
	t.Helper()
	return starlark.Call(&starlark.Thread{Name: "test"}, g[name], starlark.Tuple(args), nil)
}

// specMap decodes a returned spec dict and requires chart.Validate to
// accept the canonical JSON — every op must produce a displayable spec.
func specMap(t *testing.T, v starlark.Value) map[string]any {
	t.Helper()
	gv, err := capability.JSONValue(v)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(gv)
	if err != nil {
		t.Fatal(err)
	}
	canon, err := chart.Validate(raw)
	if err != nil {
		t.Fatalf("spec rejected by chart.Validate: %v\n%s", err, raw)
	}
	var m map[string]any
	if err := json.Unmarshal(canon, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func markType(t *testing.T, spec map[string]any) string {
	t.Helper()
	mark, ok := spec["mark"].(map[string]any)
	if !ok {
		s, _ := spec["mark"].(string)
		return s
	}
	typ, _ := mark["type"].(string)
	return typ
}

func TestChartsOpsProduceValidSpecs(t *testing.T) {
	g := loadCharts(t)
	data := eval(t, `[{"x": 1, "y": 2, "c": "a", "s": 3}, {"x": 3, "y": 5, "c": "b", "s": 7}, {"x": 2, "y": 4, "c": "a", "s": 1}]`)
	hdays := eval(t, `[{"d": "2026-01-01", "v": 1}, {"d": "2026-01-03", "v": 4}]`)

	cases := []struct {
		op    string
		args  []starlark.Value
		mark  string
		check func(t *testing.T, spec map[string]any)
	}{
		{"line", []starlark.Value{data, starlark.String("x"), starlark.String("y"), eval(t, `{"color": "c", "zoom": True, "legend_filter": True}`)}, "line", func(t *testing.T, s map[string]any) {
			params, _ := s["params"].([]any)
			if len(params) != 2 {
				t.Fatalf("params: %v", s["params"])
			}
			enc := s["encoding"].(map[string]any)
			if _, ok := enc["opacity"]; !ok {
				t.Fatal("legend_filter missing opacity condition")
			}
		}},
		{"area", []starlark.Value{data, starlark.String("x"), starlark.String("y"), starlark.None}, "area", nil},
		{"bar", []starlark.Value{data, starlark.String("c"), starlark.String("y"), starlark.None}, "bar", func(t *testing.T, s map[string]any) {
			enc := s["encoding"].(map[string]any)
			if enc["x"].(map[string]any)["type"] != "nominal" {
				t.Fatalf("bar x type: %v", enc["x"])
			}
		}},
		{"scatter", []starlark.Value{data, starlark.String("x"), starlark.String("y"), eval(t, `{"size": "s"}`)}, "point", nil},
		{"histogram", []starlark.Value{data, starlark.String("x"), eval(t, `{"bins": 10}`)}, "bar", func(t *testing.T, s map[string]any) {
			enc := s["encoding"].(map[string]any)
			x := enc["x"].(map[string]any)
			if x["bin"].(map[string]any)["maxbins"] != float64(10) {
				t.Fatalf("histogram bins: %v", x)
			}
			if enc["y"].(map[string]any)["aggregate"] != "count" {
				t.Fatalf("histogram y: %v", enc["y"])
			}
		}},
		{"heatmap", []starlark.Value{hdays, starlark.String("d"), starlark.String("v"), starlark.String("v"), starlark.None}, "rect", nil},
	}
	for _, tc := range cases {
		t.Run(tc.op, func(t *testing.T) {
			v, err := callCharts(t, g, tc.op, tc.args...)
			if err != nil {
				t.Fatal(err)
			}
			spec := specMap(t, v)
			if got := markType(t, spec); got != tc.mark {
				t.Fatalf("mark = %q, want %q", got, tc.mark)
			}
			if tc.check != nil {
				tc.check(t, spec)
			}
		})
	}
}

func TestChartsOptionErrors(t *testing.T) {
	g := loadCharts(t)
	data := eval(t, `[{"x": 1, "y": 2, "c": "a"}]`)

	cases := []struct {
		name string
		op   string
		args []starlark.Value
		want string
	}{
		{"unknown option", "line", []starlark.Value{data, starlark.String("x"), starlark.String("y"), eval(t, `{"bogus": 1}`)}, "unknown option"},
		{"size on line", "line", []starlark.Value{data, starlark.String("x"), starlark.String("y"), eval(t, `{"size": "x"}`)}, "unknown option"},
		{"options not dict", "line", []starlark.Value{data, starlark.String("x"), starlark.String("y"), starlark.String("nope")}, "options must be a dict"},
		{"legend_filter needs color", "line", []starlark.Value{data, starlark.String("x"), starlark.String("y"), eval(t, `{"legend_filter": True}`)}, "requires color"},
		{"histogram bins type", "histogram", []starlark.Value{data, starlark.String("x"), eval(t, `{"bins": "lots"}`)}, "bins must be an int"},
		{"heatmap unknown", "heatmap", []starlark.Value{data, starlark.String("x"), starlark.String("y"), starlark.String("y"), eval(t, `{"size": "x"}`)}, "unknown option"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := callCharts(t, g, tc.op, tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestChartsTooltipAndTypes(t *testing.T) {
	g := loadCharts(t)
	data := eval(t, `[{"x": "2026-01-01", "y": 2}, {"x": "2026-01-03", "y": 5}]`)

	// tooltip=False yields a bare string mark; temporal x is inferred.
	v, err := callCharts(t, g, "line", data, starlark.String("x"), starlark.String("y"), eval(t, `{"tooltip": False}`))
	if err != nil {
		t.Fatal(err)
	}
	spec := specMap(t, v)
	if spec["mark"] != "line" {
		t.Fatalf("mark: %v", spec["mark"])
	}
	enc := spec["encoding"].(map[string]any)
	if enc["x"].(map[string]any)["type"] != "temporal" {
		t.Fatalf("x type: %v", enc["x"])
	}

	// explicit type wins over inference; bad types are rejected.
	v, err = callCharts(t, g, "bar", data, starlark.String("x"), starlark.String("y"), eval(t, `{"x_type": "ordinal"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := specMap(t, v)["encoding"].(map[string]any)["x"].(map[string]any)["type"]; got != "ordinal" {
		t.Fatalf("x_type override: %v", got)
	}
	if _, err = callCharts(t, g, "line", data, starlark.String("x"), starlark.String("y"), eval(t, `{"y_type": "bogus"}`)); err == nil {
		t.Fatal("bad y_type accepted")
	}
}

// chartsOpArgs builds the positional args for an op call whose last
// argument is the options dict (starlark.None when nil).
func chartsOpArgs(op string, opts starlark.Value, data, hdays starlark.Value) []starlark.Value {
	switch op {
	case "histogram":
		return []starlark.Value{data, starlark.String("x"), opts}
	case "heatmap":
		return []starlark.Value{hdays, starlark.String("d"), starlark.String("v"), starlark.String("v"), opts}
	default:
		return []starlark.Value{data, starlark.String("x"), starlark.String("y"), opts}
	}
}

func sampleOption(name string) starlark.Value {
	switch name {
	case "bins":
		return starlark.MakeInt(12)
	case "width":
		return starlark.MakeInt(500)
	case "height":
		return starlark.MakeInt(320)
	case "tooltip", "zoom", "legend_filter":
		return starlark.True
	case "x_type", "y_type":
		return starlark.String("quantitative")
	case "color":
		return starlark.String("c")
	case "size":
		return starlark.String("s")
	default: // title and anything else
		return starlark.String("sample")
	}
}

// TestChartsManifestParamDocs keeps capability.yaml honest: every op
// documents every param, every documented options key is accepted by
// main.star, and undocumented keys still fail.
func TestChartsManifestParamDocs(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "contrib", "charts", "0.1.0", "capability.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := ParseManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Ops) != 6 {
		t.Fatalf("ops: %d", len(m.Ops))
	}

	g := loadCharts(t)
	data := eval(t, `[{"x": 1, "y": 2, "c": "a", "s": 3}, {"x": 3, "y": 5, "c": "b", "s": 7}]`)
	hdays := eval(t, `[{"d": "2026-01-01", "v": 1}, {"d": "2026-01-03", "v": 4}]`)

	for _, op := range m.Ops {
		t.Run(op.Name, func(t *testing.T) {
			if len(op.ParamDocs) != len(op.Params) {
				t.Fatalf("param docs %d != params %d", len(op.ParamDocs), len(op.Params))
			}
			for i, pd := range op.ParamDocs {
				if pd.Name != op.Params[i] {
					t.Fatalf("param doc %d = %q, want %q", i, pd.Name, op.Params[i])
				}
			}
			if op.Returns == "" || op.Example == "" {
				t.Fatal("returns/example missing")
			}
			var options *capability.ParamDoc
			for i := range op.ParamDocs {
				if op.ParamDocs[i].Name == "options" {
					options = &op.ParamDocs[i]
				}
			}
			if options == nil || len(options.Fields) == 0 {
				t.Fatal("options param docs missing")
			}
			for _, f := range options.Fields {
				t.Run("option/"+f.Name, func(t *testing.T) {
					opts := starlark.NewDict(1)
					if err := opts.SetKey(starlark.String(f.Name), sampleOption(f.Name)); err != nil {
						t.Fatal(err)
					}
					_, err := callCharts(t, g, op.Name, chartsOpArgs(op.Name, opts, data, hdays)...)
					if err != nil && strings.Contains(err.Error(), "unknown option") {
						t.Fatalf("documented option %q rejected: %v", f.Name, err)
					}
				})
			}
			t.Run("unknown option", func(t *testing.T) {
				opts := starlark.NewDict(1)
				if err := opts.SetKey(starlark.String("no_such_option"), starlark.MakeInt(1)); err != nil {
					t.Fatal(err)
				}
				_, err := callCharts(t, g, op.Name, chartsOpArgs(op.Name, opts, data, hdays)...)
				if err == nil || !strings.Contains(err.Error(), "unknown option") {
					t.Fatalf("want unknown option error, got %v", err)
				}
			})
		})
	}
}

func TestChartsCompletionUsageDocs(t *testing.T) {
	l := &CapabilityLoader{
		Identifier: CapabilityIdentifier("contrib/charts@0.1.1"),
		Options:    &Options{LocalRoots: []string{filepath.Join("..", "..", "..", "contrib")}},
	}
	m, err := l.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if m.Manifest.Version != "0.1.1" {
		t.Fatalf("version = %q, want 0.1.1", m.Manifest.Version)
	}
	old, err := os.ReadFile(filepath.Join("..", "..", "..", "contrib", "charts", "0.1.0", "main.star"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(old, m.Sources["main.star"]) {
		t.Fatal("0.1.1 main.star differs from 0.1.0")
	}

	docs := map[string]map[string]string{}
	for _, op := range m.Manifest.Ops {
		docs[op.Name] = map[string]string{}
		for _, p := range op.ParamDocs {
			docs[op.Name][p.Name] = p.Doc
		}
	}
	for _, op := range []string{"line", "area", "bar", "scatter", "heatmap"} {
		for _, want := range []string{"Horizontal-axis", "each data row", `x="day"`, "key as a string"} {
			if doc := docs[op]["x"]; !strings.Contains(doc, want) {
				t.Fatalf("%s x doc missing %q: %q", op, want, doc)
			}
		}
		for _, want := range []string{"Vertical-axis", "each data row", `y="sales"`, "key as a string"} {
			if doc := docs[op]["y"]; !strings.Contains(doc, want) {
				t.Fatalf("%s y doc missing %q: %q", op, want, doc)
			}
		}
	}
	for _, want := range []string{"Numeric field name", "each data row", `field="sales"`, "key as a string", "grouped into bins"} {
		if doc := docs["histogram"]["field"]; !strings.Contains(doc, want) {
			t.Fatalf("histogram field doc missing %q: %q", want, doc)
		}
	}
	for _, want := range []string{"Numeric field name", "cell colour", `color="count"`, "key as a string"} {
		if doc := docs["heatmap"]["color"]; !strings.Contains(doc, want) {
			t.Fatalf("heatmap color doc missing %q: %q", want, doc)
		}
	}

	g, err := m.init(context.Background(), nil, nil, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	data := eval(t, `[{"day": "2026-11-01", "sales": 120}]`)
	v, err := callCharts(t, g, "line", data, starlark.String("day"), starlark.String("sales"), starlark.None)
	if err != nil {
		t.Fatal(err)
	}
	spec := specMap(t, v)
	enc, ok := spec["encoding"].(map[string]any)
	if !ok {
		t.Fatalf("encoding: %v", spec["encoding"])
	}
	if got := enc["x"].(map[string]any)["field"]; got != "day" {
		t.Fatalf("x field = %v, want day", got)
	}
	if got := enc["y"].(map[string]any)["field"]; got != "sales" {
		t.Fatalf("y field = %v, want sales", got)
	}
}
