package stdlib

import (
	"strings"
	"testing"

	"go.starlark.net/starlark"
)

func TestChartPrimitives(t *testing.T) {
	for _, tc := range []struct{ code, want string }{
		{`chart.spec("point")["mark"]`, `"point"`},
		{`chart.spec("point")["width"]`, `480`},
		{`chart.spec("point", width="container", height=100)["height"]`, `100`},
		{`chart.spec("point")["config"]["font"]`, `"IBM Plex Sans"`},
		{`chart.spec("point")["$schema"]`, `"https://vega.github.io/schema/vega-lite/v6.json"`},
		{`chart.spec({"type": "line", "point": True})["mark"]["point"]`, `True`},
		{`len(chart.spec("point", data=[{"a": 1}, {"a": 2}])["data"]["values"])`, `2`},
		{`chart.spec("point", data=[{"a": 1}])["data"]["values"][0]["a"]`, `1`},
		{`chart.spec("point")["data"]`, `{"values": []}`},
		{`chart.spec("point", encoding={"x": chart.field("a", "quantitative")})["encoding"]["x"]["type"]`, `"quantitative"`},
		{`chart.spec("point", transform=[{"bin": "a", "as": "b"}])["transform"][0]["as"]`, `"b"`},
		{`chart.spec("point", params=[{"name": "p", "select": "interval", "bind": "scales"}])["params"][0]["bind"]`, `"scales"`},
		{`chart.spec("point", title="T")["title"]`, `"T"`},
		{`chart.field("a", "quantitative")`, `{"field": "a", "type": "quantitative"}`},
		{`chart.field("d", "temporal", time_unit="yearmonth", bin=True)["timeUnit"]`, `"yearmonth"`},
		{`chart.field("d", "temporal", bin=True)["bin"]`, `True`},
		{`chart.infer_type([{"v": 1.5}], "v")`, `"quantitative"`},
		{`chart.infer_type([{"v": 2}], "v")`, `"quantitative"`},
		{`chart.infer_type([{"v": "2026-01-01"}], "v")`, `"temporal"`},
		{`chart.infer_type([{"v": "2026-01-01T10:00:00Z"}], "v")`, `"temporal"`},
		{`chart.infer_type([{"v": "abc"}], "v")`, `"nominal"`},
		{`chart.infer_type([{"v": None}, {"v": 2}], "v")`, `"quantitative"`},
		{`chart.infer_type([{"v": None}], "v")`, `"nominal"`},
		{`chart.infer_type([], "v")`, `"nominal"`},
		{`chart.infer_type([{"v": True}], "v")`, `"nominal"`},
		{`chart.infer_type([{"w": 1}], "v")`, `"nominal"`},
		{`chart.layer(chart.spec("point", width=100), chart.spec("line"))["width"]`, `100`},
		{`len(chart.layer(chart.spec("point"), chart.spec("line"))["layer"])`, `2`},
		{`chart.layer(chart.spec("point"), chart.spec("line"))["layer"][0]["mark"]`, `"point"`},
		{`chart.layer(chart.spec("point"), chart.spec("line"))["layer"][0].get("$schema")`, `None`},
		{`chart.layer(chart.spec("point"), chart.spec("line"))["layer"][0].get("config")`, `None`},
		{`chart.layer(chart.spec("point"), chart.spec("line"))["$schema"]`, `"https://vega.github.io/schema/vega-lite/v6.json"`},
		{`chart.layer(chart.spec("point"), chart.spec("line"), title="T")["title"]`, `"T"`},
		{`len(chart.hconcat(chart.spec("point"), chart.spec("line"))["hconcat"])`, `2`},
		{`chart.hconcat(chart.spec("point"), chart.spec("line"))["width"]`, `480`},
		{`chart.vconcat(chart.spec("point"))["vconcat"][0]["mark"]`, `"point"`},
		{`chart.validate(chart.spec("point"))["mark"]`, `"point"`},
		{`chart.validate({"$schema": "https://vega.github.io/schema/vega-lite/v6", "mark": "point", "data": {"values": []}})["mark"]`, `"point"`},
		{`chart.summary(chart.spec("line", data=[{"d": "2026-01-01", "v": 1}, {"d": "2026-01-02", "v": 5}], encoding={"x": chart.field("d", "temporal"), "y": chart.field("v", "quantitative")}))`, `"line, 2 rows; x=d [2026-01-01 .. 2026-01-02]; y=v [1 .. 5]"`},
		{`chart.summary(chart.hconcat(chart.spec("point"), chart.spec("line")))`, `"hconcat(2)"`},
	} {
		t.Run(tc.code, func(t *testing.T) {
			v, err := starlark.Eval(&starlark.Thread{}, "test", tc.code, Modules())
			if err != nil {
				t.Fatal(err)
			}
			if v.String() != tc.want {
				t.Fatalf("got %s, want %s", v, tc.want)
			}
		})
	}
}

func TestChartErrors(t *testing.T) {
	for _, tc := range []struct{ code, want string }{
		{`chart.spec("image")`, `mark type "image" is not allowed`},
		{`chart.spec("point", data=[{"a": {"b": 1}}])`, `row values must be scalar`},
		{`chart.spec("point", data="x")`, `expected a list or tuple`},
		{`chart.spec("point", encoding={"z": {"field": "a"}})`, `encoding channel "z" is not allowed`},
		{`chart.spec("point", transform=[{"filter": "datum.x > 1"}])`, `filter must be an object predicate`},
		{`chart.spec("point", params=[{"name": "p", "expr": "1"}])`, `key "expr" is not allowed`},
		{`chart.spec("point", params=[{"select": "point"}])`, `requires a name`},
		{`chart.spec("point", data={"url": "http://x"})`, `expected a list or tuple`},
		{`chart.spec("point", width=99999)`, `width/height`},
		{`chart.spec("point", bogus=1)`, `invalid arguments`},
		{`chart.spec()`, `invalid arguments`},
		{`chart.field("a", "weird")`, `type must be`},
		{`chart.field("a")`, `invalid arguments`},
		{`chart.infer_type("x", "v")`, `expected a list or tuple`},
		{`chart.layer()`, `at least one spec`},
		{`chart.layer(chart.spec("point"), bogus=1)`, `unexpected keyword`},
		{`chart.layer([1])`, `expected a spec dict`},
		{`chart.hconcat()`, `at least one spec`},
		{`chart.validate("x")`, `expected an object`},
		{`chart.validate(chart.spec("image"))`, `mark type`},
	} {
		t.Run(tc.code, func(t *testing.T) {
			_, err := starlark.Eval(&starlark.Thread{}, "test", tc.code, Modules())
			if tc.want == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}
