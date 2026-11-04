package serving

import (
	"strings"

	"go.starlark.net/syntax"

	"calcside/internal/module"
)

// UsesModuleInterrupt is UsesInterrupt over a program's sources plus
// every attached module tree's .star files — module.yaml is YAML, not
// code, so it is excluded from the scan (a YAML doc that mentions
// "interrupt" must not mint spurious slots).
func UsesModuleInterrupt(sources map[string][]byte, modules map[string]module.Tree) bool {
	var extra []map[string][]byte
	for _, t := range modules {
		star := map[string][]byte{}
		for name, b := range t.Files {
			if strings.HasSuffix(name, ".star") {
				star[name] = b
			}
		}
		extra = append(extra, star)
	}
	return UsesInterrupt(sources, extra...)
}

// UsesInterrupt reports whether any source references the `interrupt`
// module, so the API mints interrupt slots only for programs that can
// claim them. It only parses; unparseable sources report true and fail
// later at prepare. Extra file sets (for example attached module
// trees) are scanned alongside: a program reachable through a module
// load can claim a slot, so handlers in modules must count.
func UsesInterrupt(sources map[string][]byte, extra ...map[string][]byte) bool {
	opts := &syntax.FileOptions{Set: true, While: true, TopLevelControl: true, GlobalReassign: true, Recursion: true}
	for _, set := range append([]map[string][]byte{sources}, extra...) {
		for name, src := range set {
			f, err := opts.Parse(name, src, 0)
			if err != nil {
				return true
			}
			found := false
			syntax.Walk(f, func(n syntax.Node) bool {
				if id, ok := n.(*syntax.Ident); ok && id.Name == "interrupt" {
					found = true
				}
				return !found
			})
			if found {
				return true
			}
		}
	}
	return false
}
