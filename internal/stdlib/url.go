package stdlib

import (
	"errors"
	"net/url"
	"sort"
	"strings"
	"unicode/utf8"

	"go.starlark.net/starlark"
)

func parseURL(s string) (*url.URL, error) {
	if err := text(s, MaxURLBytes); err != nil {
		return nil, err
	}
	u, err := url.Parse(s)
	if err != nil {
		return nil, errors.New("invalid URL")
	}
	if u.User != nil {
		return nil, errors.New("URL userinfo is not supported")
	}
	if !utf8.ValidString(u.Path) || !utf8.ValidString(u.Fragment) {
		return nil, errors.New("invalid UTF-8 URL component")
	}
	return u, nil
}

func urlParse(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var s string
	if err := unpack(b, args, kwargs, "text", &s); err != nil {
		return nil, err
	}
	u, err := parseURL(s)
	if err != nil {
		return nil, err
	}
	d := starlark.NewDict(7)
	for _, kv := range [][2]string{{"scheme", u.Scheme}, {"host", u.Host}, {"hostname", u.Hostname()}, {"port", u.Port()}, {"path", u.Path}, {"raw_query", u.RawQuery}, {"fragment", u.Fragment}} {
		_ = d.SetKey(starlark.String(kv[0]), starlark.String(kv[1]))
	}
	return d, nil
}

func urlResolve(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var base, ref string
	if err := unpack(b, args, kwargs, "base", &base, "reference", &ref); err != nil {
		return nil, err
	}
	u, err := parseURL(base)
	if err != nil {
		return nil, err
	}
	v, err := parseURL(ref)
	if err != nil {
		return nil, err
	}
	out := u.ResolveReference(v).String()
	if len(out) > MaxURLBytes {
		return nil, errors.New("output exceeds byte limit")
	}
	return starlark.String(out), nil
}

func urlQueryEncode(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var d *starlark.Dict
	if err := unpack(b, args, kwargs, "values", &d); err != nil {
		return nil, err
	}
	if d.Len() > MaxQueryPairs {
		return nil, errors.New("too many query pairs")
	}
	values := url.Values{}
	pairs, output := 0, 0
	add := func(k string, v starlark.Value) error {
		s, ok := starlark.AsString(v)
		if !ok {
			return errors.New("query values must be strings or lists of strings")
		}
		if err := text(s, MaxURLBytes); err != nil {
			return err
		}
		pairs++
		if pairs > MaxQueryPairs {
			return errors.New("too many query pairs")
		}
		output += escapedQueryLen(k) + escapedQueryLen(s) + 1
		if pairs > 1 {
			output++
		}
		if output > MaxURLBytes {
			return errors.New("output exceeds byte limit")
		}
		values[k] = append(values[k], s)
		return nil
	}
	for _, item := range d.Items() {
		if err := checkpoint(t); err != nil {
			return nil, err
		}
		k, ok := starlark.AsString(item[0])
		if !ok {
			return nil, errors.New("query keys must be strings")
		}
		if err := text(k, MaxURLBytes); err != nil {
			return nil, err
		}
		if _, ok := item[1].(starlark.String); ok {
			if err := add(k, item[1]); err != nil {
				return nil, err
			}
			continue
		}
		list, err := sequence(item[1], MaxQueryPairs)
		if err != nil {
			return nil, err
		}
		for i := 0; i < list.Len(); i++ {
			if err := add(k, list.Index(i)); err != nil {
				return nil, err
			}
		}
	}
	return starlark.String(values.Encode()), nil
}

func escapedQueryLen(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-_.~ ", rune(c)) {
			n++
		} else {
			n += 3
		}
	}
	return n
}

func urlQueryDecode(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var s string
	if err := unpack(b, args, kwargs, "text", &s); err != nil {
		return nil, err
	}
	if err := text(s, MaxURLBytes); err != nil {
		return nil, err
	}
	if strings.Count(s, "&")+1 > MaxQueryPairs {
		return nil, errors.New("too many query pairs")
	}
	values, err := url.ParseQuery(s)
	if err != nil {
		return nil, errors.New("invalid query encoding")
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	d := starlark.NewDict(len(keys))
	for _, k := range keys {
		if !utf8.ValidString(k) {
			return nil, errors.New("invalid UTF-8 query")
		}
		for _, v := range values[k] {
			if !utf8.ValidString(v) {
				return nil, errors.New("invalid UTF-8 query")
			}
		}
		_ = d.SetKey(starlark.String(k), stringsValue(values[k]))
	}
	return d, nil
}

func urlPath(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var s string
	if err := unpack(b, args, kwargs, "text", &s); err != nil {
		return nil, err
	}
	if err := text(s, MaxURLBytes); err != nil {
		return nil, err
	}
	var out string
	if b.Name() == "url.path_escape" {
		out = url.PathEscape(s)
	} else {
		var err error
		out, err = url.PathUnescape(s)
		if err != nil || !utf8.ValidString(out) {
			return nil, errors.New("invalid path encoding")
		}
	}
	if len(out) > MaxURLBytes {
		return nil, errors.New("output exceeds byte limit")
	}
	return starlark.String(out), nil
}
