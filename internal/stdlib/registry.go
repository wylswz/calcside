package stdlib

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	starjson "go.starlark.net/lib/json"
	starmath "go.starlark.net/lib/math"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"calcside/internal/capability"
	"calcside/internal/completion"
)

const MaxBytes = 8 << 20
const MaxRows = 10000
const MaxCells = 100000
const MaxColumns = 256
const MaxFieldBytes = 256 << 10
const MaxURLBytes = 64 << 10
const MaxQueryPairs = 1024
const MaxMatches = 10000

type builtinFunc func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error)
type function struct {
	name   string
	params []string
	doc    string
	call   builtinFunc
}

var functions = []function{
	{"url.parse", []string{"text"}, "Parse a URL into scheme, host, hostname, port, path, raw_query, and fragment. Rejects userinfo; never authorizes or fetches a URL.", urlParse},
	{"url.resolve", []string{"base", "reference"}, "Resolve a URL reference; the result may have a different host. Inputs and output are limited to 64 KiB.", urlResolve},
	{"url.query_encode", []string{"values"}, "Encode a dict of strings or lists of strings. Keys are sorted, repeated values keep their order; at most 1,024 pairs.", urlQueryEncode},
	{"url.query_decode", []string{"text"}, "Decode a query into a dict of lists, preserving repeated values. At most 1,024 pairs.", urlQueryDecode},
	{"url.path_escape", []string{"text"}, "Escape one UTF-8 path segment, not an entire URL.", urlPath},
	{"url.path_unescape", []string{"text"}, "Decode a path segment; invalid escapes or invalid UTF-8 fail.", urlPath},
	{"csv.parse", []string{"text", "delimiter"}, "Parse CSV into string rows. delimiter defaults to comma; UTF-8/BOM, quoted newlines and CRLF are supported. Up to 10,000 records, 100,000 cells, and 256 columns.", csvParse},
	{"csv.parse_dicts", []string{"text", "delimiter"}, "Parse header-based CSV into dicts. Rejects empty/duplicate headers and inconsistent row widths; cells remain strings.", csvParse},
	{"csv.format", []string{"rows", "delimiter", "spreadsheet_safe"}, "Format list/tuple rows of strings with LF endings. delimiter defaults to comma; spreadsheet_safe defaults to False and explicitly prefixes formula-like cells.", csvFormat},
	{"csv.format_dicts", []string{"rows", "columns", "delimiter", "spreadsheet_safe"}, "Format dict rows with required explicit columns. Uses the same CSV bounds and optional spreadsheet_safe behavior as csv.format.", csvFormat},
	{"base64.encode", []string{"data", "url_safe", "padding"}, "Encode UTF-8 text or bytes. url_safe=False, padding=True; output is bounded to 8 MiB.", base64Call},
	{"base64.decode", []string{"text", "url_safe", "padding"}, "Strict canonical Base64 decoding to bytes, not text. url_safe=False, padding=True; decode bytes explicitly before using text utilities.", base64Call},
	{"hashlib.sha256", []string{"data"}, "Lowercase SHA-256 of supplied UTF-8 text or bytes (up to 8 MiB). Does not expand secret references.", sha256Call},
	{"regex.search", []string{"pattern", "text"}, "RE2 search: None or {match, start, end, groups}; offsets are UTF-8 bytes. Text is limited to 1 MiB; patterns to 8 KiB and 32 captures.", regexCall},
	{"regex.find_all", []string{"pattern", "text", "max_matches"}, "RE2 matched strings. max_matches defaults to 10,000; exceeding it is an error, never silent truncation.", regexCall},
	{"regex.replace", []string{"pattern", "text", "replacement", "max_matches"}, "Replace RE2 matches with a literal string (no replacement backreferences). Bounded matches and output.", regexCall},
	{"regex.split", []string{"pattern", "text", "max_matches"}, "Split text using RE2, with at most 10,000 matches and 10,001 output parts.", regexCall},
	{"datetime.parse_rfc3339", []string{"text"}, "Parse an explicit RFC3339 timestamp to Unix milliseconds; sub-millisecond precision is truncated.", datetimeCall},
	{"datetime.format_rfc3339", []string{"timestamp_ms"}, "Format Unix milliseconds as RFC3339 UTC. No current clock or local timezone access.", datetimeCall},
	{"datetime.parse_date", []string{"text"}, "Parse YYYY-MM-DD to Unix milliseconds at UTC midnight.", datetimeCall},
	{"datetime.format_date", []string{"timestamp_ms"}, "Format Unix milliseconds as YYYY-MM-DD in UTC. Supported years are 0001 through 9999.", datetimeCall},
}

func Modules() starlark.StringDict {
	out := starlark.StringDict{"json": starjson.Module, "math": starmath.Module}
	for _, f := range functions {
		module, member, _ := strings.Cut(f.name, ".")
		if out[module] == nil {
			out[module] = &starlarkstruct.Module{Name: module, Members: starlark.StringDict{}}
		}
		out[module].(*starlarkstruct.Module).Members[member] = starlark.NewBuiltin(f.name, func(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			if err := checkpoint(t); err != nil {
				return nil, err
			}
			value, err := f.call(t, b, args, kwargs)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", b.Name(), err)
			}
			if err := checkpoint(t); err != nil {
				return nil, err
			}
			return value, nil
		})
	}
	for _, module := range out {
		module.Freeze()
	}
	return out
}

func Symbols() []completion.Symbol {
	var out []completion.Symbol
	seen := map[string]bool{}
	for _, f := range functions {
		module, _, _ := strings.Cut(f.name, ".")
		if !seen[module] {
			out = append(out, completion.Symbol{Name: module, Kind: "namespace", Detail: "bounded pure utilities"})
			seen[module] = true
		}
		out = append(out, completion.Symbol{Name: f.name, Kind: "function", Params: append([]string(nil), f.params...), Detail: "(" + strings.Join(f.params, ", ") + ")", Doc: f.doc})
	}
	return out
}

func checkpoint(t *starlark.Thread) error { return capability.ThreadContext(t).Err() }

func unpack(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple, pairs ...any) error {
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, pairs...); err != nil {
		return errors.New("invalid arguments")
	}
	return nil
}

func text(s string, limit int) error {
	if len(s) > limit {
		return errors.New("input exceeds byte limit")
	}
	if !utf8.ValidString(s) {
		return errors.New("invalid UTF-8")
	}
	return nil
}

func bytesOf(v starlark.Value) (string, error) {
	switch v := v.(type) {
	case starlark.String:
		s := string(v)
		return s, text(s, MaxBytes)
	case starlark.Bytes:
		if len(v) > MaxBytes {
			return "", errors.New("input exceeds byte limit")
		}
		return string(v), nil
	default:
		return "", errors.New("expected text or bytes")
	}
}

type buffer struct {
	strings.Builder
	limit int
}

func (b *buffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, errors.New("output exceeds byte limit")
	}
	return b.Builder.Write(p)
}
func (b *buffer) add(s string) error {
	if len(s) > b.limit-b.Len() {
		return errors.New("output exceeds byte limit")
	}
	_, err := b.Builder.WriteString(s)
	return err
}

func sequence(v starlark.Value, max int) (starlark.Indexable, error) {
	switch x := v.(type) {
	case *starlark.List:
		if x.Len() <= max {
			return x, nil
		}
	case starlark.Tuple:
		if x.Len() <= max {
			return x, nil
		}
	default:
		return nil, errors.New("expected a list or tuple")
	}
	return nil, errors.New("collection exceeds item limit")
}

func stringsValue(values []string) *starlark.List {
	items := make([]starlark.Value, len(values))
	for i, v := range values {
		items[i] = starlark.String(v)
	}
	return starlark.NewList(items)
}
