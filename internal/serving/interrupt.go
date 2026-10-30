package serving

import "go.starlark.net/syntax"

// UsesInterrupt reports whether any source references the `interrupt`
// module, so the API mints interrupt slots only for programs that can
// claim them. It only parses; unparseable sources report true and fail
// later at prepare.
func UsesInterrupt(sources map[string][]byte) bool {
	opts := &syntax.FileOptions{Set: true, While: true, TopLevelControl: true, GlobalReassign: true, Recursion: true}
	for name, src := range sources {
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
	return false
}
