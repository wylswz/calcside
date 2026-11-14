package serving

import (
	"encoding/json"
	"strings"
	"testing"
)

func infer(t *testing.T, sample string) map[string]any {
	t.Helper()
	raw, err := InferSchema([]byte(sample))
	if err != nil {
		t.Fatalf("%s: %v", sample, err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestInferBasic(t *testing.T) {
	s := infer(t, `{"n":1,"f":1.5,"s":"x","b":true,"z":null,"big":9007199254740993}`)
	if s["type"] != "object" || s["additionalProperties"] != true {
		t.Fatalf("%v", s)
	}
	if len(s["required"].([]any)) != 6 {
		t.Fatalf("required: %v", s["required"])
	}
	p := s["properties"].(map[string]any)
	if p["n"].(map[string]any)["type"] != "integer" ||
		p["big"].(map[string]any)["type"] != "integer" ||
		p["f"].(map[string]any)["type"] != "number" ||
		p["z"].(map[string]any)["type"] != "null" {
		t.Fatalf("%v", p)
	}
}

func TestInferNestedAndEmpty(t *testing.T) {
	s := infer(t, `{"o":{"a":{"b":1}},"e":{},"arr":[],"tags":[1,2,3]}`)
	p := s["properties"].(map[string]any)
	if p["e"].(map[string]any)["type"] != "object" ||
		len(p["e"].(map[string]any)["required"].([]any)) != 0 {
		t.Fatalf("empty object: %v", p["e"])
	}
	if p["arr"].(map[string]any)["items"].(map[string]any) == nil {
		t.Fatalf("empty array items: %v", p["arr"])
	}
	if p["tags"].(map[string]any)["items"].(map[string]any)["type"] != "integer" {
		t.Fatalf("dedup items: %v", p["tags"])
	}
}

func TestInferHeteroArray(t *testing.T) {
	s := infer(t, `{"x":[1,"a",true,2,"b"]}`)
	items := s["properties"].(map[string]any)["x"].(map[string]any)["items"].(map[string]any)
	anyOf := items["anyOf"].([]any)
	if len(anyOf) != 3 { // integer, string, boolean deduped
		t.Fatalf("anyOf: %v", anyOf)
	}
}

func TestInferTooManyShapes(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(`{"x":[`)
	for i := 0; i < 17; i++ {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(`{"k` + strings.Repeat("a", i+1) + `":1}`)
	}
	sb.WriteString(`]}`)
	if _, err := InferSchema([]byte(sb.String())); err == nil {
		t.Fatal("17 distinct shapes accepted")
	}
}

func TestInferRejects(t *testing.T) {
	for _, bad := range []string{
		`[1,2]`, `"x"`, `1`, `null`, `{bad`, `{"a":1`,
		`{"` + strings.Repeat("x", 70<<10) + `":1}`, // oversized
		`{"a":{"b":{"c":{"d":{"e":{"f":{"g":{"h":{"i":{"j":{"k":{"l":{"m":{"n":{"o":{"p":{"q":{"r":1}}}}}}}}}}}}}}}}}}`, // depth
	} {
		if _, err := InferSchema([]byte(bad)); err == nil {
			t.Fatalf("accepted: %.40s", bad)
		}
	}
}

func TestInferNoSampleValues(t *testing.T) {
	raw, err := InferSchema([]byte(`{"secret":"hunter2-api-key","n":42}`))
	if err != nil {
		t.Fatal(err)
	}
	out := string(raw)
	if strings.Contains(out, "hunter2-api-key") || strings.Contains(out, "42") {
		t.Fatalf("sample values leaked: %s", out)
	}
}
