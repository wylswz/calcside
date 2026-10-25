package stdlib

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"go.starlark.net/starlark"
)

// moduleDocs is the one-line summary per utility module, kept next to
// the function table it describes; prompt, help() and completion all
// read from it.
var moduleDocs = map[string]string{
	"json":     "JSON encode/decode (Starlark builtin module)",
	"math":     "math functions (Starlark builtin module)",
	"url":      "URL parse/resolve/query encode-decode and path escaping",
	"csv":      "CSV parse and format, row- and dict-oriented, spreadsheet-safe option",
	"base64":   "strict Base64 encode/decode for text and bytes",
	"hashlib":  "SHA-256 of text or bytes",
	"regex":    "RE2 search, find_all, replace, split — byte offsets",
	"datetime": "RFC3339 and YYYY-MM-DD timestamps to/from Unix ms; no clock access",
	"stats":    "descriptive stats, correlation, regression, tests, intervals",
	"dist":     "cdf/sf/pdf/ppf for common distributions",
	"series":   "diff, pct_change, log_return, shift, cumsum/cumprod, drawdown, rolling, ewma",
	"decimal":  "exact decimal arithmetic on strings/ints; minor-unit conversion",
	"linalg":   "matrix matmul, solve, lstsq, inv, det",
}

// help returns module docs on demand: no argument lists modules, a
// module name lists one signature line per function, module.function
// gives the full signature and description. Unknown names suggest up
// to three close matches.
func help(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var name string
	if err := starlark.UnpackArgs("help", args, kwargs, "name?", &name); err != nil {
		return nil, errors.New("usage: help() | help(\"module\") | help(\"module.function\")")
	}
	if name == "" {
		var b strings.Builder
		for _, m := range sortedModules() {
			fmt.Fprintf(&b, "%s — %s\n", m, moduleDocs[m])
		}
		b.WriteString("help(\"module\") lists functions; help(\"module.function\") shows signature and doc.\n")
		return starlark.String(b.String()), nil
	}
	if strings.Contains(name, ".") {
		for _, f := range functions {
			if f.name == name {
				return starlark.String(fmt.Sprintf("%s(%s) — %s\n", f.name, strings.Join(f.params, ", "), f.doc)), nil
			}
		}
		return nil, fmt.Errorf("no such function %q%s", name, suggest(name))
	}
	if _, ok := moduleDocs[name]; ok {
		var b strings.Builder
		fmt.Fprintf(&b, "%s — %s\n", name, moduleDocs[name])
		for _, f := range functions {
			if m, _, _ := strings.Cut(f.name, "."); m == name {
				fmt.Fprintf(&b, "%s(%s)\n", f.name, strings.Join(f.params, ", "))
			}
		}
		return starlark.String(b.String()), nil
	}
	return nil, fmt.Errorf("no such module %q%s", name, suggest(name))
}

func sortedModules() []string {
	var out []string
	for m := range moduleDocs {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// suggest returns "did you mean ..." for the closest names, using a
// bounded prefix/edit-distance scan over known names.
func suggest(name string) string {
	var names []string
	for m := range moduleDocs {
		names = append(names, m)
	}
	for _, f := range functions {
		names = append(names, f.name)
	}
	var best []string
	for _, n := range names {
		if strings.HasPrefix(n, name) || strings.HasPrefix(name, n) || editDistance(name, n) <= 2 {
			best = append(best, n)
			if len(best) == 3 {
				break
			}
		}
	}
	if len(best) == 0 {
		return ""
	}
	return "; did you mean " + strings.Join(best, ", ") + "?"
}

func editDistance(a, b string) int {
	d := make([]int, len(b)+1)
	for j := range d {
		d[j] = j
	}
	for i := 1; i <= len(a); i++ {
		prev := d[0]
		d[0] = i
		for j := 1; j <= len(b); j++ {
			cur := d[j]
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			d[j] = min(d[j]+1, d[j-1]+1, prev+cost)
			prev = cur
		}
	}
	return d[len(b)]
}
