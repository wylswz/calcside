package ext

import (
	"context"
	"encoding/json"
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
