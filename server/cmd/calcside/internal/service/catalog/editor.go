package catalog

import (
	"fmt"
	"sort"
	"strings"

	opatypes "github.com/open-policy-agent/opa/v1/types"

	"calcside/internal/completion"
	"calcside/internal/policy"
)

func EditorSymbols() []completion.Symbol {
	out := []completion.Symbol{
		{Name: "input", Kind: "namespace", Detail: "policy input"},
		{Name: "input.phase", Kind: "property", Detail: `"before" | "after"`},
		{Name: "input.user", Kind: "property", Detail: "object"},
		{Name: "input.user.id", Kind: "property", Detail: "string"},
		{Name: "input.user.email", Kind: "property", Detail: "string"},
		{Name: "input.instance", Kind: "property", Detail: "object"},
		{Name: "input.instance.id", Kind: "property", Detail: "string"},
		{Name: "input.instance.labels", Kind: "property", Detail: "object<string, string>"},
		{Name: "input.exec_id", Kind: "property", Detail: "string"},
		{Name: "input.capability", Kind: "property", Detail: "capability name"},
		{Name: "input.op", Kind: "property", Detail: "operation name"},
		{Name: "input.args", Kind: "property", Detail: "normalized operation arguments", Doc: "Select an operation to see its fields. File contents and response bodies are never included."},
		{Name: "input.result", Kind: "property", Detail: "object (after only)"},
		{Name: "input.result.error", Kind: "property", Detail: "string | null (after only)"},
		{Name: "input.result.meta", Kind: "property", Detail: "operation metadata (after only)"},
	}
	for _, b := range policy.UserCapabilities().Builtins {
		if b.Deprecated || b.Infix != "" || strings.HasPrefix(b.Name, "internal.") {
			continue
		}
		params := []string{}
		args := b.Decl.NamedFuncArgs()
		for i, a := range args.Args {
			name := fmt.Sprintf("arg%d", i+1)
			if n, ok := a.(*opatypes.NamedType); ok {
				name = n.Name
			}
			params = append(params, name)
		}
		if args.Variadic != nil {
			params = append(params, "...")
		}
		out = append(out, completion.Symbol{Name: b.Name, Kind: "function", Detail: b.Decl.String(), Doc: b.Description, Params: params})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
