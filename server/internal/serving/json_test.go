package serving

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodePrimitives(t *testing.T) {
	v, err := DecodeJSON([]byte(` {"a":[1,-2.5e3,true,false,null,"x"],"b":{}} `), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("expected object, got %T", v)
	}
	a := m["a"].([]any)
	if a[0].(json.Number).String() != "1" {
		t.Fatalf("number token lost: %v", a[0])
	}
	if a[1].(json.Number).String() != "-2.5e3" {
		t.Fatalf("number token lost: %v", a[1])
	}
	if a[2] != true || a[3] != false || a[4] != nil || a[5] != "x" {
		t.Fatalf("bad scalars: %v", a)
	}
	if got := len(m["b"].(map[string]any)); got != 0 {
		t.Fatalf("bad empty object")
	}
}

func TestDecodeIntegerPrecision(t *testing.T) {
	// Beyond float64 integer precision: token must round-trip exactly.
	v, err := DecodeJSON([]byte(`9007199254740993`), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	n, ok := v.(json.Number)
	if !ok || n.String() != "9007199254740993" {
		t.Fatalf("precision lost: %#v", v)
	}
}

func TestDecodeEscapes(t *testing.T) {
	// JSON-escaped multibyte and surrogate-pair input.
	v, err := DecodeJSON([]byte(`"a\nbA\u4e2d\ud83d\ude00"`), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if v != "a\nbA\u4e2d\U0001F600" {
		t.Fatalf("bad decode: %q", v)
	}
	// Raw UTF-8 multibyte input, constructed with Go escapes.
	v, err = DecodeJSON([]byte("\"\u65e5\u672c\""), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if v != "\u65e5\u672c" {
		t.Fatalf("bad decode: %q", v)
	}
}

func TestDecodeBudgets(t *testing.T) {
	cases := []struct {
		name   string
		input  string
		limits Limits
	}{
		{"bytes", `{"a":1}`, Limits{MaxBytes: 5}},
		{"depth", `[[[1]]]`, Limits{MaxDepth: 2}},
		{"nodes", `[1,2,3]`, Limits{MaxNodes: 3}},
		{"nodes object", `{"a":1,"b":2}`, Limits{MaxNodes: 2}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecodeJSON([]byte(tc.input), tc.limits); err == nil {
				t.Fatal("expected error")
			}
		})
	}
	// Exact fits succeed.
	if _, err := DecodeJSON([]byte(`[[1]]`), Limits{MaxDepth: 2, MaxNodes: 3}); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeRejects(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"trailing", `{} {}`},
		{"trailing char", `{}x`},
		{"duplicate key", `{"a":1,"a":2}`},
		{"duplicate key nested", `{"o":{"a":1,"a":2}}`},
		{"number too long", `[` + strings.Repeat("1", 300) + `]`},
		{"exponent too big", `1e1025`},
		{"exponent huge", `1e99999999`},
		{"leading zero", `01`},
		{"bare minus", `-`},
		{"trailing dot", `1.`},
		{"empty exponent", `1e`},
		{"unpaired high surrogate", `"\ud800"`},
		{"unpaired low surrogate", `"\udc00"`},
		{"high surrogate then char", `"\ud800x"`},
		{"high surrogate then non-low", `"\ud800A"`},
		{"bad escape", `"\x"`},
		{"unescaped control", "\"a\x01b\""},
		{"unterminated string", `"abc`},
		{"unterminated object", `{"a":1`},
		{"unterminated array", `[1,2`},
		{"bad literal", `tru`},
		{"empty", ``},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecodeJSON([]byte(tc.input), Limits{}); err == nil {
				t.Fatal("expected error")
			}
		})
	}
	// Boundary: exponent magnitude 1024 and a 256-char token pass.
	if _, err := DecodeJSON([]byte(`1e1024`), Limits{}); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeJSON([]byte(strings.Repeat("9", 256)), Limits{}); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeJSON([]byte(strings.Repeat("9", 257)), Limits{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestDecodeInvalidUTF8(t *testing.T) {
	if _, err := DecodeJSON([]byte("\"\xff\xfe\""), Limits{}); err == nil {
		t.Fatal("expected error")
	}
	if _, err := DecodeJSON([]byte("\xff"), Limits{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestDecodeErrorsDoNotEchoValues(t *testing.T) {
	// Keys and values may carry secrets; errors must only report
	// structure (offsets, member indexes).
	secret := "s3kr3t-token"
	input := `{"` + secret + `":1,"` + secret + `":2}`
	_, err := DecodeJSON([]byte(input), Limits{})
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked input value: %v", err)
	}
}
