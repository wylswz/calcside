package engine

import (
	"context"
	"maps"
	"slices"
	"strings"
	"time"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"calcside/internal/capability"
	"calcside/internal/completion"
	"calcside/internal/stdlib"
)

const maxCompletionSymbols = 2048
const maxCompletionName = 256

func (s *Session) Completions(ctx context.Context, reg *capability.Registry) (*completion.Context, error) {
	if !s.ExecMu.TryLock() {
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		for !s.ExecMu.TryLock() {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-tick.C:
			}
		}
	}
	defer s.ExecMu.Unlock()
	out := &completion.Context{Symbols: []completion.Symbol{}, EnvKeys: []string{}}
	bindings := maps.Clone(starlark.Universe)
	maps.Copy(bindings, s.Globals)
	maps.Copy(bindings, s.Predeclared)
	docs := map[string]capability.OpInfo{}
	utilities := map[string]completion.Symbol{}
	for _, symbol := range stdlib.Symbols() {
		utilities[symbol.Name] = symbol
	}
	for _, name := range reg.Names() {
		cfg, granted := s.Capabilities[string(name)]
		if !granted {
			continue
		}
		f, _ := reg.Get(name)
		ops := f.Ops()
		if dynamic, ok := f.(interface{ CompletionOps(any) []capability.OpInfo }); ok {
			ops = dynamic.CompletionOps(cfg)
		}
		for _, op := range ops {
			docs[string(name)+"."+string(op.Name)] = op
		}
	}
	var visit func(string, starlark.Value, int)
	visit = func(name string, value starlark.Value, depth int) {
		if len(out.Symbols) >= maxCompletionSymbols || len(name) > maxCompletionName {
			out.Truncated = true
			return
		}
		symbol := completion.Symbol{Name: name, Kind: "variable", Detail: value.Type()}
		if _, ok := value.(starlark.Callable); ok {
			symbol.Kind = "function"
		}
		if fn, ok := value.(*starlark.Function); ok {
			if fn.NumParams() > 64 {
				out.Truncated = true
			} else {
				symbol.Params = functionParams(fn)
				symbol.Detail = "(" + strings.Join(symbol.Params, ", ") + ")"
			}
		}
		if utility, ok := utilities[name]; ok {
			symbol = utility
		}
		if op, ok := docs[name]; ok {
			symbol.Params = op.Params
			symbol.Doc = op.Doc
			symbol.Detail = "(...)"
			if op.Params != nil {
				symbol.Detail = "(" + strings.Join(op.Params, ", ") + ")"
			}
		}
		if len(symbol.Detail) > 4096 || len(symbol.Params) > 64 || slices.ContainsFunc(symbol.Params, func(p string) bool { return len(p) > maxCompletionName }) {
			symbol.Params = nil
			symbol.Detail = value.Type()
			out.Truncated = true
		}
		if len(symbol.Doc) > 4096 {
			symbol.Doc = ""
			out.Truncated = true
		}
		var attrs starlark.HasAttrs
		switch v := value.(type) {
		case *starlarkstruct.Module:
			symbol.Kind, attrs = "namespace", v
		case *starlarkstruct.Struct:
			symbol.Kind, attrs = "namespace", v
		case *starlark.Dict:
			attrs = v
		case *starlark.List:
			attrs = v
		case starlark.String:
			attrs = v
		}
		out.Symbols = append(out.Symbols, symbol)
		if attrs == nil || depth >= 2 {
			return
		}
		for _, attr := range attrs.AttrNames() {
			if len(out.Symbols) >= maxCompletionSymbols {
				out.Truncated = true
				break
			}
			if member, err := attrs.Attr(attr); err == nil && member != nil {
				visit(name+"."+attr, member, depth+1)
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(bindings)) {
		visit(name, bindings[name], 0)
		if len(out.Symbols) >= maxCompletionSymbols {
			out.Truncated = true
			break
		}
	}
	if env, ok := bindings["env"].(*starlark.Dict); ok {
		iter := env.Iterate()
		defer iter.Done()
		var key starlark.Value
		for iter.Next(&key) {
			if len(out.EnvKeys) >= maxCompletionSymbols {
				out.Truncated = true
				break
			}
			if name, ok := starlark.AsString(key); ok && len(name) <= maxCompletionName {
				out.EnvKeys = append(out.EnvKeys, name)
			}
		}
		slices.Sort(out.EnvKeys)
	}
	return out, nil
}

func functionParams(fn *starlark.Function) []string {
	n := fn.NumParams()
	kwargs, varargs := n, n
	if fn.HasKwargs() {
		kwargs = n - 1
		varargs--
	}
	if fn.HasVarargs() {
		varargs--
	}
	out := []string{}
	ordinary := n
	if fn.HasKwargs() {
		ordinary--
	}
	if fn.HasVarargs() {
		ordinary--
	}
	for i := 0; i < ordinary && i < 64; i++ {
		if i == ordinary-fn.NumKwonlyParams() && fn.NumKwonlyParams() > 0 {
			star := "*"
			if fn.HasVarargs() {
				name, _ := fn.Param(varargs)
				star += name
			}
			out = append(out, star)
		}
		name, _ := fn.Param(i)
		if fn.ParamDefault(i) != nil {
			name += "?"
		}
		out = append(out, name)
	}
	if fn.HasVarargs() && fn.NumKwonlyParams() == 0 {
		name, _ := fn.Param(varargs)
		out = append(out, "*"+name)
	}
	if fn.HasKwargs() {
		name, _ := fn.Param(kwargs)
		out = append(out, "**"+name)
	}
	return out
}
