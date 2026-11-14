package serving

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

func compile(t *testing.T, doc string) *Schema {
	t.Helper()
	s, err := CompileSchema([]byte(doc), Limits{})
	if err != nil {
		t.Fatalf("CompileSchema(%s): %v", doc, err)
	}
	return s
}

func compileErr(t *testing.T, doc string) {
	t.Helper()
	if _, err := CompileSchema([]byte(doc), Limits{}); err == nil {
		t.Fatalf("CompileSchema(%s): expected error", doc)
	}
}

func valid(t *testing.T, s *Schema, doc string) {
	t.Helper()
	if err := s.Validate([]byte(doc)); err != nil {
		t.Fatalf("Validate(%s): %v", doc, err)
	}
}

func invalid(t *testing.T, s *Schema, doc string) {
	t.Helper()
	if err := s.Validate([]byte(doc)); err == nil {
		t.Fatalf("Validate(%s): expected error", doc)
	}
}

func TestSchemaEmptyAndTypes(t *testing.T) {
	s := compile(t, `{}`)
	valid(t, s, `null`)
	valid(t, s, `{"a":[1,2]}`)

	s = compile(t, `{"type":"object"}`)
	valid(t, s, `{}`)
	invalid(t, s, `[]`)

	s = compile(t, `{"type":["string","null"]}`)
	valid(t, s, `"x"`)
	valid(t, s, `null`)
	invalid(t, s, `3`)
}

func TestSchemaIntegerSemantics(t *testing.T) {
	s := compile(t, `{"type":"integer"}`)
	valid(t, s, `5`)
	valid(t, s, `1.0`)
	valid(t, s, `1e0`)
	invalid(t, s, `1.5`)
	invalid(t, s, `"5"`)

	s = compile(t, `{"type":"number"}`)
	valid(t, s, `1.5`)
	valid(t, s, `5`)
	invalid(t, s, `true`)
}

func TestSchemaPropertiesRequired(t *testing.T) {
	s := compile(t, `{
		"type": "object",
		"properties": {"a": {"type": "integer"}, "b": {"type": "string"}},
		"required": ["a"],
		"additionalProperties": false
	}`)
	valid(t, s, `{"a":1}`)
	valid(t, s, `{"a":1,"b":"x"}`)
	invalid(t, s, `{}`)
	invalid(t, s, `{"a":"x"}`)
	invalid(t, s, `{"a":1,"c":2}`)
	// required may name an undeclared property.
	s = compile(t, `{"required":["ghost"]}`)
	invalid(t, s, `{}`)
	valid(t, s, `{"ghost":1}`)
}

func TestSchemaAdditionalOmittedVsFalse(t *testing.T) {
	permissive := compile(t, `{"type":"object","properties":{"a":{}}}`)
	valid(t, permissive, `{"a":1,"extra":[1,2]}`)

	strict := compile(t, `{"type":"object","properties":{"a":{}},"additionalProperties":false}`)
	invalid(t, strict, `{"a":1,"extra":0}`)

	explicitTrue := compile(t, `{"type":"object","additionalProperties":true}`)
	valid(t, explicitTrue, `{"x":1}`)

	nested := compile(t, `{"type":"object","additionalProperties":{"type":"integer"}}`)
	valid(t, nested, `{"x":1,"y":2}`)
	invalid(t, nested, `{"x":"s"}`)
}

func TestSchemaItems(t *testing.T) {
	s := compile(t, `{"type":"array","items":{"type":"integer"},"minItems":1,"maxItems":3}`)
	valid(t, s, `[1,2]`)
	invalid(t, s, `[]`)
	invalid(t, s, `[1,2,3,4]`)
	invalid(t, s, `[1,"x"]`)
}

func TestSchemaNumericBounds(t *testing.T) {
	s := compile(t, `{"minimum":2,"maximum":4}`)
	valid(t, s, `3`)
	valid(t, s, `2`)
	valid(t, s, `4`)
	valid(t, s, `"not a number"`) // ignored for non-numbers
	invalid(t, s, `1`)
	invalid(t, s, `4.000001`)
}

func TestSchemaLengthInCodepoints(t *testing.T) {
	s := compile(t, `{"type":"string","minLength":2,"maxLength":3}`)
	valid(t, s, `"ab"`)
	// Escaped JSON form: 3 codepoints.
	valid(t, s, `"\u65e5\u672c\u8a9e"`)
	// Raw UTF-8 multibyte input built from Go escapes.
	valid(t, s, "\""+"\u65e5\u672c\u8a9e"+"\"")
	invalid(t, s, `"\u65e5\u672c\u8a9ex"`)
	invalid(t, s, `"a"`)
}

func TestSchemaEnumConst(t *testing.T) {
	s := compile(t, `{"enum":[1,"two",[3],{"k":true}]}`)
	valid(t, s, `1`)
	valid(t, s, `1.0`) // exact numeric equivalence
	valid(t, s, `1e0`)
	valid(t, s, `"two"`)
	valid(t, s, `[3]`)
	valid(t, s, `{"k":true}`)
	invalid(t, s, `2`)
	invalid(t, s, `{"k":false}`)

	s = compile(t, `{"const":{"a":[1,2]}}`)
	valid(t, s, `{"a":[1,2]}`)
	invalid(t, s, `{"a":[1]}`)
}

func TestSchemaEnumDuplicatesAccepted(t *testing.T) {
	// The standard and the backing library do not require unique enum
	// elements; the product subset accepts duplicates.
	s := compile(t, `{"enum":[1,1,1.0]}`)
	valid(t, s, `1`)
	invalid(t, s, `2`)

	s = compile(t, `{"enum":[{"a":1},{"a":1}]}`)
	valid(t, s, `{"a":1}`)
}

func TestSchemaBigNumericEquivalence(t *testing.T) {
	// Integer equivalence beyond float64 precision.
	s := compile(t, `{"const":9007199254740993}`)
	valid(t, s, `9007199254740993`)
	valid(t, s, `9007199254740993.0`)
	invalid(t, s, `9007199254740992`)
	// In a float64 world both inputs below are "equal"; not here.
	s = compile(t, `{"enum":[9007199254740993]}`)
	invalid(t, s, `9007199254740994`)
}

func TestSchemaAnyOf(t *testing.T) {
	s := compile(t, `{"type":"string","anyOf":[{"minLength":3},{"enum":["ok"]}]}`)
	valid(t, s, `"abc"`)
	valid(t, s, `"ok"`)
	invalid(t, s, `"no"`)
	invalid(t, s, `3`) // sibling type constraint still applies

	// Nested anyOf inside properties.
	s = compile(t, `{"properties":{"v":{"anyOf":[{"type":"integer"},{"type":"boolean"}]}}}`)
	valid(t, s, `{"v":true}`)
	invalid(t, s, `{"v":"x"}`)
}

func TestSchemaCompileRejects(t *testing.T) {
	cases := []struct {
		name string
		doc  string
	}{
		{"root array", `[]`},
		{"ref local", `{"$ref":"#/definitions/x"}`},
		{"ref remote", `{"$ref":"https://example.com/s.json"}`},
		{"ref file", `{"$ref":"file:///etc/passwd"}`},
		{"ref nested property", `{"properties":{"a":{"$ref":"#/definitions/x"}}}`},
		{"ref nested items", `{"items":{"$ref":"#"}}`},
		{"ref nested anyOf", `{"anyOf":[{"$ref":"#"}]}`},
		{"defs", `{"$defs":{"a":{"type":"integer"}}}`},
		{"format", `{"format":"email"}`},
		{"pattern", `{"pattern":"^a"}`},
		{"allOf", `{"allOf":[{}]}`},
		{"oneOf", `{"oneOf":[{}]}`},
		{"not", `{"not":{}}`},
		{"default", `{"default":1}`},
		{"additionalItems", `{"additionalItems":{}}`},
		{"dependentRequired", `{"dependentRequired":{"a":["b"]}}`},
		{"type number", `{"type":5}`},
		{"type bad name", `{"type":"objectt"}`},
		{"type empty list", `{"type":[]}`},
		{"type dup list", `{"type":["string","string"]}`},
		{"properties array", `{"properties":[]}`},
		{"properties value non-object", `{"properties":{"a":5}}`},
		{"properties value boolean schema", `{"properties":{"a":true}}`},
		{"required string", `{"required":"a"}`},
		{"required non-string", `{"required":[1]}`},
		{"required dup", `{"required":["a","a"]}`},
		{"additionalProps number", `{"additionalProperties":1}`},
		{"items array", `{"items":[{"type":"integer"}]}`},
		{"items boolean schema", `{"items":true}`},
		{"items scalar", `{"items":5}`},
		{"enum empty", `{"enum":[]}`},
		{"enum non-array", `{"enum":5}`},
		{"minimum string", `{"minimum":"2"}`},
		{"minLength negative", `{"minLength":-1}`},
		{"minLength fractional", `{"minLength":1.5}`},
		{"minLength string", `{"minLength":"2"}`},
		{"anyOf empty", `{"anyOf":[]}`},
		{"anyOf too many", `{"anyOf":[` + strings.Repeat("{},", 16) + `{}]}`},
		{"anyOf member non-object", `{"anyOf":[5]}`},
		{"anyOf member boolean schema", `{"anyOf":[true]}`},
		{"title number", `{"title":1}`},
		{"description bool", `{"description":true}`},
		{"schema wrong uri", `{"$schema":"http://json-schema.org/draft-07/schema#"}`},
		{"schema non-string", `{"$schema":5}`},
		{"non-object schema doc", `"string"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			compileErr(t, tc.doc)
		})
	}
}

func TestSchemaCountKeywordCap(t *testing.T) {
	// The library stores these as platform int via int64 truncation;
	// unbounded values would silently wrap, so they are capped at
	// schemaMaxCount (MaxInt32).
	for _, k := range []string{"minLength", "maxLength", "minItems", "maxItems"} {
		t.Run(k, func(t *testing.T) {
			compileErr(t, fmt.Sprintf(`{"%s":18446744073709551616}`, k))
			compileErr(t, fmt.Sprintf(`{"%s":1e20}`, k))
			compileErr(t, fmt.Sprintf(`{"%s":2147483648}`, k))
			compile(t, fmt.Sprintf(`{"%s":2147483647}`, k))
			compile(t, fmt.Sprintf(`{"%s":0}`, k))
		})
	}
}

func TestSchemaPropertyNamedRef(t *testing.T) {
	// "$ref" is an ordinary property name in data positions; only as a
	// schema keyword is it rejected.
	s := compile(t, `{"properties":{"$ref":{"type":"string"}},"required":["$ref"]}`)
	valid(t, s, `{"$ref":"anything"}`)
	invalid(t, s, `{"$ref":1}`)
	// enum/const values may also contain "$ref" keys.
	s = compile(t, `{"const":{"$ref":"#/x"}}`)
	valid(t, s, `{"$ref":"#/x"}`)
}

func TestSchemaKeywordLimits(t *testing.T) {
	// Hard caps bind on top of caller limits.
	deep := strings.Repeat(`{"properties":{"a":`, 40) + `{}` + strings.Repeat("}", 40)
	compileErr(t, deep)

	var sb strings.Builder
	sb.WriteString(`{"properties":{`)
	for i := 0; i < 2100; i++ {
		if i > 0 {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, `"k%d":{}`, i)
	}
	sb.WriteString(`}}`)
	compileErr(t, sb.String())

	if _, err := CompileSchema([]byte(`{"type":"object"}`), Limits{MaxBytes: 10}); err == nil {
		t.Fatal("caller byte limit not honored")
	}
}

func TestSchemaJSONReturnsCopy(t *testing.T) {
	doc := `{"type":"integer","title":"t","description":"d","$schema":"https://json-schema.org/draft/2020-12/schema"}`
	raw := []byte(doc)
	s, err := CompileSchema(raw, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	got := s.JSON()
	if string(got) != doc {
		t.Fatalf("round trip mismatch: %s", got)
	}
	got[0] = '[' // mutating the returned copy must not affect the schema
	if string(s.JSON()) != doc {
		t.Fatal("JSON() is not defensive")
	}
	// Mutating the caller's original slice must not affect the schema
	// either.
	raw[0] = '['
	if string(s.JSON()) != doc {
		t.Fatal("original slice mutation leaked into schema")
	}
	if err := s.Validate([]byte(`5`)); err != nil {
		t.Fatalf("validate after caller mutation: %v", err)
	}
}

func TestSchemaAnnotationsAndURI(t *testing.T) {
	s := compile(t, `{"title":"T","description":"D","$schema":"https://json-schema.org/draft/2020-12/schema"}`)
	valid(t, s, `42`)
}

func TestSchemaErrorsDoNotEcho(t *testing.T) {
	secret := "s3kr3t-contract-token"

	// Compile failures are generic: a secret-looking keyword value
	// rejected by the library metaschema must not surface.
	_, err := CompileSchema([]byte(`{"type":"`+secret+`"}`), Limits{})
	if err == nil {
		t.Fatal("expected compile error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("compile error leaked schema content: %v", err)
	}

	// Same for an unsupported keyword whose name is secret-looking.
	_, err = CompileSchema([]byte(`{"`+secret+`":{}}`), Limits{})
	if err == nil {
		t.Fatal("expected compile error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("compile error leaked schema content: %v", err)
	}

	// Validation failures are generic: const/enum mismatches and
	// forbidden extra keys must not echo input or schema content.
	s := compile(t, `{"const":"`+secret+`"}`)
	err = s.Validate([]byte(`"other"`))
	if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "other") {
		t.Fatalf("validation error leaked content: %v", err)
	}

	s = compile(t, `{"type":"object","properties":{"a":{}},"additionalProperties":false}`)
	err = s.Validate([]byte(`{"a":1,"` + secret + `":1}`))
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("validation error leaked key: %v", err)
	}
}

func TestDenyLoader(t *testing.T) {
	if _, err := (denyLoader{}).Load("https://example.com/x.json"); err == nil {
		t.Fatal("deny loader must refuse every URL")
	}
	// A schema without refs compiles fine under the deny loader: the
	// embedded metaschema and registered document are not URL loads.
	compile(t, `{"type":"object","properties":{"a":{"anyOf":[{"type":"integer"}]}}}`)
}

func TestSchemaConcurrentValidate(t *testing.T) {
	s := compile(t, `{
		"type":"object",
		"properties":{"n":{"type":"integer","minimum":0},"s":{"enum":["a","b"]}},
		"required":["n"],
		"anyOf":[{"required":["s"]},{"properties":{"n":{"maximum":0}}}]
	}`)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				if err := s.Validate([]byte(`{"n":0}`)); err != nil {
					t.Errorf("validate: %v", err)
					return
				}
				if err := s.Validate([]byte(`{"n":1,"s":"a"}`)); err != nil {
					t.Errorf("validate: %v", err)
					return
				}
				if err := s.Validate([]byte(`{"n":-1}`)); err == nil {
					t.Error("expected error")
					return
				}
			}
		}(i)
	}
	wg.Wait()
}
