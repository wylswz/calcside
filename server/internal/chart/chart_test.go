package chart

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func spec(t *testing.T, parts ...string) []byte {
	t.Helper()
	return []byte(strings.Join(parts, ""))
}

const head = `{"$schema":"` + SchemaURL + `","width":480,"height":280,"data":{"values":[{"day":"2026-01-01","latency_ms":12},{"day":"2026-01-02","latency_ms":480}]},"mark":"line","encoding":{"x":{"field":"day","type":"temporal"},"y":{"field":"latency_ms","type":"quantitative"}}}`

func TestValidateValid(t *testing.T) {
	canon, err := Validate([]byte(head))
	if err != nil {
		t.Fatalf("valid spec rejected: %v", err)
	}
	// Canonical re-marshal: same content, normalized whitespace.
	var got, want any
	if err := json.Unmarshal(canon, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(head), &want); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("canonical mismatch:\n got %v\nwant %v", got, want)
	}
}

func TestValidateRejects(t *testing.T) {
	cases := []struct {
		name string
		spec string
		want string // substring expected in the error
	}{
		{"empty", ``, "empty"},
		{"not json", `{`, "spec:"},
		{"trailing", `{}` + `{}`, "trailing"},
		{"root not object", `[1,2]`, "expected an object"},
		{"bad schema", `{"$schema":"https://evil.example/x","mark":"point","data":{"values":[]}}`, "$schema"},
		{"schema nested", `{"mark":"point","layer":[{"$schema":"` + SchemaURL + `"}],"data":{"values":[]}}`, "$schema is only valid"},
		{"schema prefix ok", `{"$schema":"` + schemaPrefix + `","mark":"point","data":{"values":[]}}`, ""},
		{"width string", `{"width":"wide","mark":"point","data":{"values":[]}}`, "width/height"},
		{"width huge", `{"width":99999,"mark":"point","data":{"values":[]}}`, "width/height"},
		{"width container", `{"width":"container","mark":"point","data":{"values":[]}}`, ""},
		{"config nested", `{"config":{"font":"x"},"layer":[{"config":{"font":"x"}}],"data":{"values":[]}}`, "config is only valid"},
		{"spec without facet", `{"spec":{"mark":"point"},"data":{"values":[]}}`, "spec is only valid alongside facet"},
		{"facet spec ok", `{"facet":{"field":"a","type":"nominal"},"spec":{"mark":"point","data":{"values":[]}},"data":{"values":[]}}`, ""},
		{"unknown view key", `{"bogus":1,"mark":"point","data":{"values":[]}}`, `key "bogus" is not allowed`},
		{"datasets denied", `{"datasets":{"a":[]},"mark":"point","data":{"values":[]}}`, `key "datasets"`},
		{"data url", `{"data":{"url":"http://x"},"mark":"point"}`, `data must be an object with only "values"`},
		{"data name", `{"data":{"name":"foo","values":[]},"mark":"point"}`, `data must be an object`},
		{"data sequence", `{"data":{"sequence":{"start":0,"stop":5},"values":[]},"mark":"point"}`, `data must be an object`},
		{"data format", `{"data":{"values":[],"format":{"type":"json"}},"mark":"point"}`, `data must be an object`},
		{"data graticule", `{"data":{"graticule":true,"values":[]},"mark":"point"}`, `data must be an object`},
		{"row nested", `{"data":{"values":[{"a":{"b":1}}]},"mark":"point"}`, "row values must be scalar"},
		{"row denied-name keys ok", `{"data":{"values":[{"url":"http://x","test":1,"on":true}]},"mark":"point"}`, ""},
		{"row expr-suffix key ok", `{"data":{"values":[{"labelExpr":"v"}]},"mark":"point"}`, ""},
		{"encoding field named url ok", `{"encoding":{"x":{"field":"url","type":"nominal"}},"mark":"point","data":{"values":[{"url":"a"}]}}`, ""},
		{"mark image", `{"mark":"image","data":{"values":[]}}`, `mark type "image" is not allowed`},
		{"mark bad obj", `{"mark":{"type":"image"},"data":{"values":[]}}`, `mark type "image" is not allowed`},
		{"mark no type", `{"mark":{"color":"red"},"data":{"values":[]}}`, "mark type"},
		{"mark ok types", `{"mark":{"type":"boxplot","tooltip":true},"data":{"values":[]}}`, ""},
		{"encoding bad channel", `{"encoding":{"z":{"field":"a"}},"mark":"point","data":{"values":[]}}`, `encoding channel "z" is not allowed`},
		{"condition test", `{"encoding":{"color":{"condition":{"test":"datum.x>1","value":"red"}}},"mark":"point","data":{"values":[]}}`, `key "test" is not allowed`},
		{"condition no param", `{"encoding":{"color":{"condition":{"value":"red"}}},"mark":"point","data":{"values":[]}}`, "condition requires param"},
		{"condition param ok", `{"params":[{"name":"p","select":"point"}],"encoding":{"opacity":{"condition":{"param":"p","value":1},"value":0.2}},"mark":"point","data":{"values":[]}}`, ""},
		{"transform string filter", `{"transform":[{"filter":"datum.x > 1"}],"mark":"point","data":{"values":[]}}`, "filter must be an object predicate"},
		{"transform no op", `{"transform":[{"foo":1}],"mark":"point","data":{"values":[]}}`, "exactly one operation"},
		{"transform two ops", `{"transform":[{"bin":{"field":"a"},"aggregate":{"op":"count"}}],"mark":"point","data":{"values":[]}}`, "exactly one operation"},
		{"transform bad sibling", `{"transform":[{"bin":"a","bogus":1}],"mark":"point","data":{"values":[]}}`, `key "bogus" is not allowed for bin`},
		{"transform on denied", `{"transform":[{"regression":"y","on":"x"}],"mark":"point","data":{"values":[]}}`, `key "on" is not allowed for regression`},
		{"transform filter field ok", `{"transform":[{"filter":{"field":"x","gt":1}}],"mark":"point","data":{"values":[]}}`, ""},
		{"transform filter and ok", `{"transform":[{"filter":{"and":[{"field":"x","gt":1},{"field":"y","valid":true}]}}],"mark":"point","data":{"values":[]}}`, ""},
		{"transform filter not ok", `{"transform":[{"filter":{"not":{"field":"x","oneOf":[1,2]}}}],"mark":"point","data":{"values":[]}}`, ""},
		{"transform filter bad", `{"transform":[{"filter":{"field":"x","bogus":1}}],"mark":"point","data":{"values":[]}}`, `key "bogus" is not allowed in a filter predicate`},
		{"transform filter field only", `{"transform":[{"filter":{"field":"x"}}],"mark":"point","data":{"values":[]}}`, "requires a comparison"},
		{"transform filter param ok", `{"params":[{"name":"p","select":"interval"}],"transform":[{"filter":{"param":"p"}}],"mark":"point","data":{"values":[]}}`, ""},
		{"transform expr op", `{"transform":[{"calculate":"1+1"}],"mark":"point","data":{"values":[]}}`, "exactly one operation"},
		{"param expr", `{"params":[{"name":"p","expr":"1+1"}],"mark":"point","data":{"values":[]}}`, `key "expr" is not allowed`},
		{"param ok", `{"params":[{"name":"p","select":{"type":"point","fields":["a"],"clear":false},"bind":"legend"}],"mark":"point","data":{"values":[]}}`, ""},
		{"param bad select", `{"params":[{"name":"p","select":"brush"}],"mark":"point","data":{"values":[]}}`, "select must be"},
		{"param select on", `{"params":[{"name":"p","select":{"type":"point","on":"click"}}],"mark":"point","data":{"values":[]}}`, `key "on" is not allowed`},
		{"param clear true", `{"params":[{"name":"p","select":{"type":"point","clear":true}}],"mark":"point","data":{"values":[]}}`, "only false is allowed"},
		{"param bind bad", `{"params":[{"name":"p","bind":"marks"}],"mark":"point","data":{"values":[]}}`, "must be"},
		{"param unnamed", `{"params":[{"select":"point"}],"mark":"point","data":{"values":[]}}`, "requires a name"},
		{"layer ok", `{"layer":[{"mark":"line","data":{"values":[]},"encoding":{"x":{"field":"a"},"y":{"field":"b"}}},{"mark":"point","data":{"values":[]},"encoding":{"x":{"field":"a"},"y":{"field":"c"}}}],"data":{"values":[]}}`, ""},
		{"layer bad nested", `{"layer":[{"mark":"image","data":{"values":[]}}],"data":{"values":[]}}`, "mark type"},
		{"config bad key", `{"config":{"bogus":1},"mark":"point","data":{"values":[]}}`, `config key "bogus"`},
		{"config ok", `{"config":{"font":"IBM Plex Sans","axis":{"labelColor":"red"}},"mark":"point","data":{"values":[]}}`, ""},
		{"config expr", `{"config":{"view":{"strokeWidth":{"expr":"1"}}},"mark":"point","data":{"values":[]}}`, `key "expr" is not allowed`},
		{"expr suffix key", `{"mark":"point","encoding":{"x":{"field":"a","labelExpr":"1"}},"data":{"values":[]}}`, `key "labelExpr" is not allowed`},
		{"format ok", `{"encoding":{"x":{"field":"a","axis":{"format":"%Y-%m-%d"}}},"mark":"point","data":{"values":[]}}`, ""},
		{"format bad", `{"encoding":{"x":{"field":"a","axis":{"format":"{evil}"}}},"mark":"point","data":{"values":[]}}`, "not an allowed d3 format"},
		{"href denied", `{"mark":{"type":"text","href":"http://x"},"data":{"values":[]}}`, `key "href" is not allowed`},
		{"signal denied", `{"params":[{"name":"p","signal":"1"}],"mark":"point","data":{"values":[]}}`, `key "signal" is not allowed`},
		{"lookup denied", `{"transform":[{"lookup":"a"}],"mark":"point","data":{"values":[]}}`, "exactly one operation"},
		{"facet row col", `{"facet":{"row":{"field":"a","type":"nominal"},"column":{"field":"b","type":"nominal"},"spec":{"mark":"point","data":{"values":[]}}},"data":{"values":[]}}`, ""},
		{"facet bad key", `{"facet":{"url":"http://x"},"spec":{"mark":"point"},"data":{"values":[]}}`, `key "url" is not allowed`},
		{"resolve ok", `{"resolve":{"scale":{"color":"independent"}},"mark":"point","data":{"values":[]}}`, ""},
		{"tooltip array ok", `{"encoding":{"tooltip":[{"field":"a"},{"field":"b","type":"quantitative"}]},"mark":"point","data":{"values":[]}}`, ""},
		{"tooltip string elem", `{"encoding":{"tooltip":["a","b"]},"mark":"point","data":{"values":[]}}`, ""},
		{"tooltip bad elem", `{"encoding":{"tooltip":[1]},"mark":"point","data":{"values":[]}}`, "channel def must be an object or field name"},
		{"title object", `{"title":{"text":"hello","anchor":"start"},"mark":"point","data":{"values":[]}}`, ""},
		{"nan number", `{"width":NaN,"mark":"point","data":{"values":[]}}`, "spec:"}, // invalid JSON literal
		{"deep nesting", deepSpec(40), "nesting deeper"},
		{"long string", `{"title":"` + strings.Repeat("x", MaxStringBytes+1) + `","mark":"point","data":{"values":[]}}`, "string exceeds"},
		{"rows limit", rowsSpec(MaxRows + 1), "rows exceeds"},
		{"rows ok", rowsSpec(MaxRows), ""},
		{"spec too big", bigSpec(), "byte limit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Validate([]byte(tc.spec))
			if tc.want == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}

func deepSpec(n int) string {
	// n nested facet.spec wrappers around a minimal view.
	open, close := "", ""
	for i := 0; i < n; i++ {
		open += `{"facet":{"field":"a","type":"nominal"},"spec":`
		close += `}`
	}
	return open + `{"mark":"point","data":{"values":[]}}` + close
}

func rowsSpec(n int) string {
	var b strings.Builder
	b.WriteString(`{"data":{"values":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"a":%d}`, i)
	}
	b.WriteString(`]},"mark":"point"}`)
	return b.String()
}

func bigSpec() string {
	// Pad title under MaxStringBytes but push total size over MaxSpecBytes.
	return `{"title":"` + strings.Repeat("x", MaxSpecBytes) + `","mark":"point","data":{"values":[]}}`
}

func TestValidateInfNumber(t *testing.T) {
	// JSON has no Inf literal; use 1e999 which parses to +Inf.
	s := `{"width":1e999,"mark":"point","data":{"values":[]}}`
	_, err := Validate([]byte(s))
	if err == nil || !strings.Contains(err.Error(), "width/height") {
		t.Fatalf("expected width/height error, got %v", err)
	}
	s = `{"encoding":{"x":{"field":"a","scale":{"domain":[1e999]}}},"mark":"point","data":{"values":[]}}`
	_, err = Validate([]byte(s))
	if err == nil || !strings.Contains(err.Error(), "not finite") {
		t.Fatalf("expected finite error, got %v", err)
	}
}

func TestMapStrings(t *testing.T) {
	in := map[string]any{
		"keep": "value",
		"sec":  "secret-value",
		"nested": map[string]any{
			"sec":  []any{"a", "secret-value", 1.5},
			"num":  json.Number("42"),
			"bool": true,
			"nil":  nil,
		},
	}
	redact := func(s string) string {
		return strings.ReplaceAll(s, "secret-value", "***")
	}
	out, changed := MapStrings(in, redact)
	if !changed {
		t.Fatal("expected changed=true")
	}
	om := out.(map[string]any)
	if om["keep"] != "value" {
		t.Fatalf("keep = %v", om["keep"])
	}
	nested := om["nested"].(map[string]any)
	arr := nested["sec"].([]any)
	if arr[0] != "a" || arr[1] != "***" || arr[2] != 1.5 {
		t.Fatalf("nested = %v", arr)
	}
	if nested["num"] != json.Number("42") {
		t.Fatalf("number mutated: %v", nested["num"])
	}

	// Key rewrite.
	out2, changed2 := MapStrings(map[string]any{"secret-key": 1}, func(s string) string {
		return strings.ReplaceAll(s, "secret", "***")
	})
	if !changed2 {
		t.Fatal("expected key change")
	}
	if _, ok := out2.(map[string]any)["***-key"]; !ok {
		t.Fatalf("key not rewritten: %v", out2)
	}

	// No change.
	_, changed3 := MapStrings(in, func(s string) string { return s })
	if changed3 {
		t.Fatal("expected changed=false for identity")
	}
}

func TestSummary(t *testing.T) {
	got := Summary([]byte(head))
	want := "line, 2 rows; x=day [2026-01-01 .. 2026-01-02]; y=latency_ms [12 .. 480]"
	if got != want {
		t.Fatalf("Summary = %q, want %q", got, want)
	}
	if got := Summary([]byte(`not json`)); got != "invalid spec" {
		t.Fatalf("bad spec summary = %q", got)
	}
	// Layer kind + no encodings.
	layered := `{"layer":[{"mark":"line","data":{"values":[{"a":1}]}},{"mark":"point","data":{"values":[{"a":2}]}}]}`
	if got := Summary([]byte(layered)); got != "layer(2), 2 rows" {
		t.Fatalf("layer summary = %q", got)
	}
	// String field range is lexicographic.
	strs := `{"data":{"values":[{"k":"b"},{"k":"a"}]},"mark":"point","encoding":{"x":{"field":"k","type":"nominal"}}}`
	if got := Summary([]byte(strs)); !strings.Contains(got, "x=k [a .. b]") {
		t.Fatalf("string range summary = %q", got)
	}
	// Numeric-looking strings stay strings.
	mixed := `{"data":{"values":[{"k":"10"},{"k":"9"}]},"mark":"point","encoding":{"x":{"field":"k","type":"nominal"}}}`
	if got := Summary([]byte(mixed)); !strings.Contains(got, "[10 .. 9]") {
		t.Fatalf("lexicographic summary = %q", got)
	}
}

func TestKindRows(t *testing.T) {
	kind, rows := KindRows([]byte(head))
	if kind != "line" || rows != 2 {
		t.Fatalf("KindRows = %q,%d", kind, rows)
	}
	kind, rows = KindRows([]byte(`{"layer":[{},{}],"data":{"values":[{"a":1}]}}`))
	if kind != "layer(2)" || rows != 1 {
		t.Fatalf("KindRows layer = %q,%d", kind, rows)
	}
	kind, rows = KindRows([]byte(`{"facet":{"field":"a"},"spec":{"mark":"point"},"data":{"values":[{"a":1},{"a":2}]}}`))
	if kind != "facet" || rows != 2 {
		t.Fatalf("KindRows facet = %q,%d", kind, rows)
	}
	kind, rows = KindRows([]byte(`bad`))
	if kind != "" || rows != 0 {
		t.Fatalf("KindRows bad = %q,%d", kind, rows)
	}
}
