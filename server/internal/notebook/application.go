package notebook

import (
	"go.starlark.net/syntax"
)

// ApplicationSourceFacts parses one application notebook section and
// returns its dependency-graph facts plus entry hints: the direct
// top-level def names in the file and the literal string targets of
// interrupt.wait's handler/on_expire arguments. The hints are
// presentation metadata only — they never validate eligibility, never
// resolve aliases or computed targets, and never label every def a
// handler. A parse failure yields a GraphSection with ParseOK false
// and no facts, matching "a section that does not parse has no static
// facts".
func ApplicationSourceFacts(id, code string, caps []string) (GraphSection, []string, []string) {
	f, err := fileOpts.Parse(id+".star", code, 0)
	if err != nil {
		return GraphSection{Name: id}, nil, nil
	}
	defines, uses, _ := AnalyzeFile(f, caps)
	var defs, targets []string
	for _, st := range f.Stmts {
		if d, ok := st.(*syntax.DefStmt); ok {
			defs = append(defs, d.Name.Name)
		}
	}
	syntax.Walk(f, func(n syntax.Node) bool {
		call, ok := n.(*syntax.CallExpr)
		if !ok {
			return true
		}
		dot, ok := call.Fn.(*syntax.DotExpr)
		if !ok || dot.Name == nil || dot.Name.Name != "wait" {
			return true
		}
		base, ok := dot.X.(*syntax.Ident)
		if !ok || base.Name != "interrupt" {
			return true
		}
		for _, arg := range call.Args {
			be, ok := arg.(*syntax.BinaryExpr)
			if !ok || be.Op != syntax.EQ {
				continue
			}
			key, ok := be.X.(*syntax.Ident)
			if !ok || (key.Name != "handler" && key.Name != "on_expire") {
				continue
			}
			lit, ok := be.Y.(*syntax.Literal)
			if !ok {
				continue
			}
			if s, ok := lit.Value.(string); ok && s != "" {
				targets = append(targets, s)
			}
		}
		return true
	})
	return GraphSection{Name: id, Defines: defines, Uses: uses, ParseOK: true}, defs, targets
}
