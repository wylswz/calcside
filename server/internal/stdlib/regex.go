package stdlib

import (
	"errors"
	"regexp"
	"regexp/syntax"

	"go.starlark.net/starlark"
)

func regexpCost(r *syntax.Regexp) int {
	n := len(r.Rune) + 2
	for _, child := range r.Sub {
		n += regexpCost(child)
		if n > 4096 {
			return 4097
		}
	}
	if r.Op == syntax.OpRepeat {
		copies := r.Max
		if copies < 0 {
			copies = r.Min + 1
		}
		if copies > 0 && n > 4096/copies {
			return 4097
		}
		n *= copies
	}
	return n
}

func regexCall(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var pattern, s, replacement string
	max := MaxMatches
	pairs := []any{"pattern", &pattern, "text", &s}
	if b.Name() == "regex.replace" {
		pairs = append(pairs, "replacement", &replacement)
	}
	if b.Name() != "regex.search" {
		pairs = append(pairs, "max_matches?", &max)
	}
	if err := unpack(b, args, kwargs, pairs...); err != nil {
		return nil, err
	}
	if err := text(pattern, 8<<10); err != nil {
		return nil, err
	}
	if err := text(s, 1<<20); err != nil {
		return nil, err
	}
	if err := text(replacement, MaxBytes); err != nil {
		return nil, err
	}
	if max < 1 || max > MaxMatches {
		return nil, errors.New("invalid match limit")
	}
	ast, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return nil, errors.New("invalid RE2 pattern")
	}
	if regexpCost(ast) > 4096 || ast.MaxCap() > 32 {
		return nil, errors.New("pattern complexity limit exceeded")
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, errors.New("invalid RE2 pattern")
	}
	if b.Name() == "regex.search" {
		m := re.FindStringSubmatchIndex(s)
		if m == nil {
			return starlark.None, nil
		}
		d := starlark.NewDict(4)
		_ = d.SetKey(starlark.String("match"), starlark.String(s[m[0]:m[1]]))
		_ = d.SetKey(starlark.String("start"), starlark.MakeInt(m[0]))
		_ = d.SetKey(starlark.String("end"), starlark.MakeInt(m[1]))
		groups := starlark.NewList(nil)
		for i := 2; i < len(m); i += 2 {
			v := starlark.Value(starlark.None)
			if m[i] >= 0 {
				v = starlark.String(s[m[i]:m[i+1]])
			}
			_ = groups.Append(v)
		}
		_ = d.SetKey(starlark.String("groups"), groups)
		return d, nil
	}
	matches := re.FindAllStringIndex(s, max+1)
	if len(matches) > max {
		return nil, errors.New("match limit exceeded")
	}
	if err := checkpoint(t); err != nil {
		return nil, err
	}
	if b.Name() == "regex.split" {
		return stringsValue(re.Split(s, -1)), nil
	}
	if b.Name() == "regex.find_all" {
		values := make([]starlark.Value, len(matches))
		for i, m := range matches {
			values[i] = starlark.String(s[m[0]:m[1]])
		}
		return starlark.NewList(values), nil
	}
	out := &buffer{limit: MaxBytes}
	offset := 0
	for _, m := range matches {
		if err := checkpoint(t); err != nil {
			return nil, err
		}
		if err := out.add(s[offset:m[0]]); err != nil {
			return nil, err
		}
		if err := out.add(replacement); err != nil {
			return nil, err
		}
		offset = m[1]
	}
	if err := out.add(s[offset:]); err != nil {
		return nil, err
	}
	return starlark.String(out.String()), nil
}
