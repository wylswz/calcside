package stdlib

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"calcside/internal/capability"
)

func TestUtilities(t *testing.T) {
	for _, tc := range []struct{ code, want string }{
		{`url.parse("https://example.com:8443/a%20b?q=x#f")["path"]`, `"/a b"`},
		{`url.parse("https://[::1]:8443/")["hostname"]`, `"::1"`},
		{`url.resolve("https://a.example/a/b", "//b.example/c")`, `"https://b.example/c"`},
		{`url.query_encode({"b": ["2", "1"], "a": "a b"})`, `"a=a+b&b=2&b=1"`},
		{`url.query_decode("x=1&x=2&empty=")["x"]`, `["1", "2"]`},
		{`url.path_unescape(url.path_escape("中文/a b+"))`, `"中文/a b+"`},
		{`url.path_escape("a/b c+")`, `"a%2Fb%20c+"`},
		{`csv.parse("x,y\r\n\"a,b\",\"line1\r\nline2\"\r\n")`, `[["x", "y"], ["a,b", "line1\nline2"]]`},
		{`csv.parse_dicts("\ufeffid,amount\nA,0012\n")`, `[{"id": "A", "amount": "0012"}]`},
		{`csv.parse("")`, `[]`},
		{`csv.parse("\n" * 12000)`, `[]`},
		{`csv.parse("a;b\nx;y\n", delimiter=";")`, `[["a", "b"], ["x", "y"]]`},
		{`csv.format([["a,b", "line1\nline2"]])`, `"\"a,b\",\"line1\nline2\"\n"`},
		{`csv.format_dicts([{"b": "2", "a": "1"}], columns=["a", "b"])`, `"a,b\n1,2\n"`},
		{`csv.parse(csv.format([[""]]))`, `[[""]]`},
		{`csv.format([["=1+1", "ok"]], spreadsheet_safe=True)`, `"'=1+1,ok\n"`},
		{`csv.format([["  =1+1"]], spreadsheet_safe=True)`, `"'  =1+1\n"`},
		{`base64.encode("hello")`, `"aGVsbG8="`},
		{`base64.encode(base64.decode("/w=="))`, `"/w=="`},
		{`type(base64.decode("AA=="))`, `"bytes"`},
		{`base64.encode(base64.decode("_w", url_safe=True, padding=False), url_safe=True, padding=False)`, `"_w"`},
		{`hashlib.sha256("abc")`, `"ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"`},
		{`regex.search("(a)(b)?", "中a")["start"]`, `3`},
		{`regex.search("(a)(b)?", "a")["groups"]`, `["a", None]`},
		{`regex.search("z", "abc")`, `None`},
		{`regex.find_all("[0-9]+", "a12b34")`, `["12", "34"]`},
		{`regex.replace("a", "aba", "$1")`, `"$1b$1"`},
		{`regex.split("[,;]", "a,b;c")`, `["a", "b", "c"]`},
		{`datetime.parse_rfc3339("1970-01-01T01:00:00+01:00")`, `0`},
		{`datetime.parse_rfc3339("1970-01-01T00:00:00.123456Z")`, `123`},
		{`datetime.format_rfc3339(123)`, `"1970-01-01T00:00:00.123Z"`},
		{`datetime.format_date(datetime.parse_date("2024-02-29"))`, `"2024-02-29"`},
		{`datetime.format_date(-1)`, `"1969-12-31"`},
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

func TestUtilityLimitsAndErrors(t *testing.T) {
	for _, code := range []string{
		`url.parse("https://secret-value@example.com/")`,
		`url.query_decode("secret-value=%xx")`,
		`url.path_unescape("%FF")`,
		`url.query_encode({"x": ["y"] * 1025})`,
		`url.query_encode({"x": "!" * 65536})`,
		`url.query_encode({"x": 1})`,
		`url.parse(base64.decode("YQ=="))`,
		`csv.parse_dicts("a,a\nx,y\n")`,
		`csv.parse_dicts(",a\nx,y\n")`,
		`csv.parse("a,b\nx\n")`,
		`csv.parse("secret-value,\"unclosed")`,
		`csv.parse("," * 256)`,
		`csv.parse("x" * 262145)`,
		`csv.parse("a\n" * 10001)`,
		`csv.parse("a", delimiter="\n")`,
		`csv.parse("a", delimiter="ab")`,
		`csv.format_dicts([{}], columns=["missing"])`,
		`csv.format_dicts([], columns=["x", "x"])`,
		`csv.format([[1]])`,
		`csv.format([["x"], ["x", "y"]])`,
		`csv.format([["\"" * 262144]] * 17)`,
		`base64.decode("/x==")`,
		`base64.decode("YQ==\n")`,
		`base64.decode("secret-value")`,
		`base64.encode("x" * 8388608)`,
		`hashlib.sha256("x" * 8388609)`,
		`regex.search("(?=a)", "a")`,
		`regex.search("(", "secret-value")`,
		`regex.search("(?:abcdef){1000}", "a")`,
		`regex.search("()" * 33, "a")`,
		`regex.find_all("a", "aaa", max_matches=2)`,
		`regex.replace("a", "aa", "x" * 8388608)`,
		`regex.search("a", "x" * 1048577)`,
		`datetime.parse_date("2024-02-30")`,
		`datetime.parse_rfc3339("2024-01-01T00:00:00")`,
		`datetime.parse_rfc3339("2024-01-01T00:00:00,123Z")`,
		`datetime.parse_rfc3339("2024-01-01T00:00:00+01:60")`,
		`datetime.format_date(253402300800000)`,
	} {
		t.Run(code, func(t *testing.T) {
			_, err := starlark.Eval(&starlark.Thread{}, "test", code, Modules())
			if err == nil {
				t.Fatal("expected error")
			}
			if strings.Contains(err.Error(), "secret-value") {
				t.Fatalf("input leaked in error: %v", err)
			}
		})
	}
}

func TestUtilityMetadataAndCancellation(t *testing.T) {
	modules := Modules()
	for _, s := range Symbols() {
		module, member, ok := strings.Cut(s.Name, ".")
		if !ok {
			continue
		}
		value := modules[module].(*starlarkstruct.Module).Members[member]
		if value == nil || len(s.Params) == 0 || s.Doc == "" {
			t.Fatalf("incomplete metadata: %+v", s)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	thread := &starlark.Thread{}
	thread.SetLocal(capability.ContextKey, ctx)
	if _, err := starlark.Eval(thread, "test", `csv.parse("a,b\n")`, modules); err == nil {
		t.Fatal("ignored cancellation")
	}
	for _, module := range []string{"datetime", "url", "csv", "base64", "hashlib", "regex"} {
		m := modules[module].(*starlarkstruct.Module)
		for _, name := range []string{"now", "open", "getenv", "sleep", "random"} {
			if m.Members[name] != nil {
				t.Fatalf("unexpected ambient operation: %s.%s", module, name)
			}
		}
	}
}

func FuzzCSV(f *testing.F) {
	for _, s := range []string{"", "a,b\n1,2\n", "\"\"\n", "\"line1\nline2\",x\n", "\"a\"\"b\"\n"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if len(s) > 1<<16 {
			return
		}
		v, err := starlark.Eval(&starlark.Thread{}, "fuzz", "csv.parse("+strconv.Quote(s)+")", Modules())
		if err != nil {
			return
		}
		env := Modules()
		env["rows"] = v
		again, err := starlark.Eval(&starlark.Thread{}, "fuzz", "csv.parse(csv.format(rows))", env)
		if err != nil {
			t.Fatal(err)
		}
		same, err := starlark.Equal(v, again)
		if err != nil || !same {
			t.Fatalf("CSV roundtrip differs: %v", err)
		}
	})
}
