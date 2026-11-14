// Package notebook holds the pure static analysis behind instance
// notebooks (EP-0022): which top-level names a section of Starlark
// defines and which globals it reads. Parsing only — nothing here
// evaluates code, and the API tier still never runs user code.
package notebook

import (
	"sort"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"

	"calcside/internal/stdlib"
)

// fileOpts mirrors internal/engine's parse dialect: the same flags the
// runtime accepts, so analysis never disagrees with execution about
// what parses.
var fileOpts = &syntax.FileOptions{
	Set:             true,
	While:           true,
	TopLevelControl: true,
	GlobalReassign:  true,
	Recursion:       true,
}

// boundExtras are predeclared names every instance has beyond the
// Starlark universe and stdlib modules: env/secrets accessors, the
// gated print, help, and the interrupt binding.
var boundExtras = []string{"env", "secrets", "print", "help", "interrupt"}

// Analyze parses code and returns the sorted top-level names it binds
// (defines) and the sorted module-global names it reads (uses). Names
// that are always bound — Starlark builtins, stdlib modules, env,
// secrets, print, help, interrupt and the capability names in caps —
// never appear in uses. ok is false on any parse error; callers store
// empty sets then, matching "a section that does not parse has no
// static facts".
//
// uses is deliberately conservative for dependency tracking: a name
// read before this file binds it counts as a use even when a later
// statement defines it (`y = x` before `x = 1` needs an external x),
// and function/lambda bodies count reads of names the file never
// defines. Self-contained reads (`x = 1` then `y = x`) are not uses.
func Analyze(code string, caps []string) (defines, uses []string, ok bool) {
	f, err := fileOpts.Parse("section.star", code, 0)
	if err != nil {
		return nil, nil, false
	}
	a := &analyzer{
		bound:      map[string]bool{},
		allDefs:    map[string]bool{},
		guaranteed: map[string]bool{},
		defined:    map[string]bool{},
		readsNow:   map[string]bool{},
		readsDef:   map[string]bool{},
	}
	for name := range starlark.Universe {
		a.bound[name] = true
	}
	for name := range stdlib.Modules() {
		a.bound[name] = true
	}
	for _, n := range boundExtras {
		a.bound[n] = true
	}
	for _, n := range caps {
		a.bound[n] = true
	}
	// Pre-pass: every name the module scope binds anywhere in the file
	// (defines output), plus the subset bound unconditionally — deferred
	// bodies resolve against the guaranteed set only, since a name bound
	// inside a branch may not exist when the body runs.
	a.collectDefines(f.Stmts)
	a.collectGuaranteed(f.Stmts, false)
	for n := range a.allDefs {
		defines = append(defines, n)
	}
	sort.Strings(defines)
	a.stmts(f.Stmts, nil, false)
	useSet := map[string]bool{}
	for n := range a.readsNow {
		useSet[n] = true
	}
	for n := range a.readsDef {
		if !a.guaranteed[n] {
			useSet[n] = true
		}
	}
	for n := range useSet {
		uses = append(uses, n)
	}
	sort.Strings(uses)
	return defines, uses, true
}

// analyzer walks the AST with an explicit scope stack. Local scopes
// (function bodies, lambdas, comprehensions) are pushed frames; the
// module scope is tracked separately in defined/allDefs so reads can be
// classified by whether the name was bound yet at that program point.
type analyzer struct {
	bound      map[string]bool // names always provided by the instance
	allDefs    map[string]bool // every module-level binding in the file
	guaranteed map[string]bool // module bindings that always happen
	defined    map[string]bool // unconditional module bindings seen so far
	cond       int             // >0 inside conditional module-level blocks
	readsNow   map[string]bool // global reads at inline (module-order) points
	readsDef   map[string]bool // global reads inside deferred bodies
}

// collectDefines records every name bound at module level, in any
// order — the set deferred bodies resolve against.
func (a *analyzer) collectDefines(ss []syntax.Stmt) {
	for _, st := range ss {
		switch s := st.(type) {
		case *syntax.AssignStmt:
			a.collectTarget(s.LHS)
		case *syntax.DefStmt:
			a.allDefs[s.Name.Name] = true
		case *syntax.LoadStmt:
			for _, id := range s.To {
				a.allDefs[id.Name] = true
			}
		case *syntax.IfStmt:
			a.collectDefines(s.True)
			a.collectDefines(s.False)
		case *syntax.ForStmt:
			a.collectTarget(s.Vars)
			a.collectDefines(s.Body)
		case *syntax.WhileStmt:
			a.collectDefines(s.Body)
		}
	}
}

func (a *analyzer) collectTarget(e syntax.Expr) {
	a.collectTargetInto(e, a.allDefs)
}

func (a *analyzer) collectTargetInto(e syntax.Expr, m map[string]bool) {
	switch t := e.(type) {
	case *syntax.Ident:
		m[t.Name] = true
	case *syntax.ParenExpr:
		a.collectTargetInto(t.X, m)
	case *syntax.TupleExpr:
		for _, e := range t.List {
			a.collectTargetInto(e, m)
		}
	case *syntax.ListExpr:
		for _, e := range t.List {
			a.collectTargetInto(e, m)
		}
	}
}

// collectGuaranteed records module-level bindings that are certain to
// happen: statements outside any conditional block. Branches, loop
// bodies and loop variables are conditional — a name bound only there
// may not exist when deferred code runs, so it is not guaranteed.
func (a *analyzer) collectGuaranteed(ss []syntax.Stmt, cond bool) {
	for _, st := range ss {
		switch s := st.(type) {
		case *syntax.AssignStmt:
			if !cond {
				a.collectTargetInto(s.LHS, a.guaranteed)
			}
		case *syntax.DefStmt:
			if !cond {
				a.guaranteed[s.Name.Name] = true
			}
		case *syntax.LoadStmt:
			if !cond {
				for _, id := range s.To {
					a.guaranteed[id.Name] = true
				}
			}
		case *syntax.IfStmt:
			a.collectGuaranteed(s.True, true)
			a.collectGuaranteed(s.False, true)
		case *syntax.ForStmt:
			a.collectGuaranteed(s.Body, true)
		case *syntax.WhileStmt:
			a.collectGuaranteed(s.Body, true)
		}
	}
}

// scopes is the stack of local (non-module) binding sets; deferred
// marks reads inside def/lambda bodies, which run after the section.
func (a *analyzer) stmts(ss []syntax.Stmt, scopes []map[string]bool, deferred bool) {
	for _, st := range ss {
		a.stmt(st, scopes, deferred)
	}
}

func (a *analyzer) stmt(st syntax.Stmt, scopes []map[string]bool, deferred bool) {
	switch s := st.(type) {
	case *syntax.AssignStmt:
		// RHS evaluates before the binding lands.
		a.expr(s.RHS, scopes, deferred)
		if s.Op == syntax.EQ {
			a.bind(s.LHS, scopes, deferred)
		} else {
			// x += 1 reads the old binding, then rebinds it.
			a.expr(s.LHS, scopes, deferred)
			a.bind(s.LHS, scopes, deferred)
		}
	case *syntax.DefStmt:
		a.def(s, scopes, deferred)
	case *syntax.ExprStmt:
		a.expr(s.X, scopes, deferred)
	case *syntax.IfStmt:
		a.expr(s.Cond, scopes, deferred)
		a.cond++
		a.stmts(s.True, scopes, deferred)
		a.stmts(s.False, scopes, deferred)
		a.cond--
	case *syntax.ForStmt:
		a.expr(s.X, scopes, deferred)
		a.cond++
		// Loop vars are certain inside the body but uncertain after
		// it (the iterable may be empty), so they bind in a scope
		// local to the body rather than the module's defined set.
		inner := append(append([]map[string]bool{}, scopes...), map[string]bool{})
		a.bind(s.Vars, inner, deferred)
		a.stmts(s.Body, inner, deferred)
		a.cond--
	case *syntax.WhileStmt:
		a.expr(s.Cond, scopes, deferred)
		a.cond++
		a.stmts(s.Body, scopes, deferred)
		a.cond--
	case *syntax.LoadStmt:
		// To holds the local names bound; From names live in the
		// loaded module and are not global reads here.
		for _, id := range s.To {
			a.bindName(id.Name, scopes)
		}
	case *syntax.ReturnStmt:
		if s.Result != nil {
			a.expr(s.Result, scopes, deferred)
		}
	case *syntax.BranchStmt:
		// break/continue/pass bind nothing.
	}
}

// def handles `def name(params): body`: the name binds in the current
// scope, defaults evaluate there too, and the body gets a fresh scope
// seeded with the parameter names — deferred, since it runs at call
// time, after the whole section has executed.
func (a *analyzer) def(s *syntax.DefStmt, scopes []map[string]bool, deferred bool) {
	a.bindName(s.Name.Name, scopes)
	locals := map[string]bool{}
	for _, p := range s.Params {
		switch p := p.(type) {
		case *syntax.Ident:
			locals[p.Name] = true
		case *syntax.BinaryExpr:
			// ident=default: the default reads the enclosing scope now.
			if id, ok := p.X.(*syntax.Ident); ok {
				locals[id.Name] = true
			}
			a.expr(p.Y, scopes, deferred)
		case *syntax.UnaryExpr:
			// *args / **kwargs; X may be nil for a bare `*`.
			if id, ok := p.X.(*syntax.Ident); ok {
				locals[id.Name] = true
			}
		}
	}
	a.stmts(s.Body, append(append([]map[string]bool{}, scopes...), locals), true)
}

// bind records assignment targets: plain identifiers bind in the
// current scope (module scope when scopes is empty); index and
// attribute targets only read their base.
func (a *analyzer) bind(target syntax.Expr, scopes []map[string]bool, deferred bool) {
	switch t := target.(type) {
	case *syntax.Ident:
		a.bindName(t.Name, scopes)
	case *syntax.ParenExpr:
		a.bind(t.X, scopes, deferred)
	case *syntax.TupleExpr:
		for _, e := range t.List {
			a.bind(e, scopes, deferred)
		}
	case *syntax.ListExpr:
		for _, e := range t.List {
			a.bind(e, scopes, deferred)
		}
	default:
		// d[k] = v / obj.attr = v mutate rather than bind.
		a.expr(target, scopes, deferred)
	}
}

func (a *analyzer) bindName(name string, scopes []map[string]bool) {
	if len(scopes) == 0 {
		// Conditional module binds do not mark the name as bound for
		// later inline reads — the branch may not have run.
		if a.cond == 0 {
			a.defined[name] = true
		}
		return
	}
	scopes[len(scopes)-1][name] = true
}

// expr walks an expression for global reads. Ident resolution checks
// local scopes first; what remains reads a module global. Inline reads
// (deferred=false) must find the name already bound in this file or
// provided by the instance; deferred reads may also resolve to names
// the file binds later, since the body runs after the section.
func (a *analyzer) expr(e syntax.Expr, scopes []map[string]bool, deferred bool) {
	switch x := e.(type) {
	case *syntax.Ident:
		for i := len(scopes) - 1; i >= 0; i-- {
			if scopes[i][x.Name] {
				return
			}
		}
		if a.bound[x.Name] {
			return
		}
		if deferred {
			// Record every deferred global read; the guaranteed-binding
			// filter at the end decides which are real external uses —
			// a name bound only inside a branch is not guaranteed to
			// exist when the body runs.
			a.readsDef[x.Name] = true
		} else if !a.defined[x.Name] {
			a.readsNow[x.Name] = true
		}
	case *syntax.Literal:
	case *syntax.ParenExpr:
		a.expr(x.X, scopes, deferred)
	case *syntax.ListExpr:
		for _, e := range x.List {
			a.expr(e, scopes, deferred)
		}
	case *syntax.TupleExpr:
		for _, e := range x.List {
			a.expr(e, scopes, deferred)
		}
	case *syntax.DictExpr:
		for _, e := range x.List {
			if d, ok := e.(*syntax.DictEntry); ok {
				a.expr(d.Key, scopes, deferred)
				a.expr(d.Value, scopes, deferred)
			}
		}
	case *syntax.UnaryExpr:
		if x.X != nil {
			a.expr(x.X, scopes, deferred)
		}
	case *syntax.BinaryExpr:
		a.expr(x.X, scopes, deferred)
		a.expr(x.Y, scopes, deferred)
	case *syntax.CondExpr:
		a.expr(x.Cond, scopes, deferred)
		a.expr(x.True, scopes, deferred)
		a.expr(x.False, scopes, deferred)
	case *syntax.IndexExpr:
		a.expr(x.X, scopes, deferred)
		a.expr(x.Y, scopes, deferred)
	case *syntax.SliceExpr:
		a.expr(x.X, scopes, deferred)
		if x.Lo != nil {
			a.expr(x.Lo, scopes, deferred)
		}
		if x.Hi != nil {
			a.expr(x.Hi, scopes, deferred)
		}
		if x.Step != nil {
			a.expr(x.Step, scopes, deferred)
		}
	case *syntax.DotExpr:
		// x.name: the field name is not a variable read.
		a.expr(x.X, scopes, deferred)
	case *syntax.CallExpr:
		a.expr(x.Fn, scopes, deferred)
		for _, arg := range x.Args {
			a.callArg(arg, scopes, deferred)
		}
	case *syntax.LambdaExpr:
		a.lambda(x, scopes, deferred)
	case *syntax.Comprehension:
		a.comprehension(x, scopes, deferred)
	}
}

// callArg walks one call argument: f(k=v) names a parameter, not a
// variable; f(*xs) / f(**kw) unwrap the star.
func (a *analyzer) callArg(e syntax.Expr, scopes []map[string]bool, deferred bool) {
	switch x := e.(type) {
	case *syntax.BinaryExpr:
		if x.Op == syntax.EQ {
			a.expr(x.Y, scopes, deferred)
			return
		}
		a.expr(x.X, scopes, deferred)
		a.expr(x.Y, scopes, deferred)
	case *syntax.UnaryExpr:
		if x.Op == syntax.STAR || x.Op == syntax.STARSTAR {
			if x.X != nil {
				a.expr(x.X, scopes, deferred)
			}
			return
		}
		a.expr(e, scopes, deferred)
	default:
		a.expr(e, scopes, deferred)
	}
}

// lambda binds params locally; default values evaluate in the
// enclosing scope at definition time, the body deferred.
func (a *analyzer) lambda(x *syntax.LambdaExpr, scopes []map[string]bool, deferred bool) {
	locals := map[string]bool{}
	for _, p := range x.Params {
		switch p := p.(type) {
		case *syntax.Ident:
			locals[p.Name] = true
		case *syntax.BinaryExpr:
			if id, ok := p.X.(*syntax.Ident); ok {
				locals[id.Name] = true
			}
			a.expr(p.Y, scopes, deferred)
		case *syntax.UnaryExpr:
			if id, ok := p.X.(*syntax.Ident); ok {
				locals[id.Name] = true
			}
		}
	}
	a.expr(x.Body, append(append([]map[string]bool{}, scopes...), locals), true)
}

// comprehension walks [body for vars in x if cond ...]: clauses bind
// in the comprehension's own scope and evaluate inline, left to right.
func (a *analyzer) comprehension(x *syntax.Comprehension, scopes []map[string]bool, deferred bool) {
	local := map[string]bool{}
	inner := append(append([]map[string]bool{}, scopes...), local)
	for _, c := range x.Clauses {
		switch c := c.(type) {
		case *syntax.ForClause:
			a.expr(c.X, inner, deferred)
			a.bindInto(c.Vars, local)
		case *syntax.IfClause:
			a.expr(c.Cond, inner, deferred)
		}
	}
	a.expr(x.Body, inner, deferred)
}

// bindInto records comprehension/for-clause variables without
// touching the module scope.
func (a *analyzer) bindInto(target syntax.Expr, local map[string]bool) {
	switch t := target.(type) {
	case *syntax.Ident:
		local[t.Name] = true
	case *syntax.ParenExpr:
		a.bindInto(t.X, local)
	case *syntax.TupleExpr:
		for _, e := range t.List {
			a.bindInto(e, local)
		}
	case *syntax.ListExpr:
		for _, e := range t.List {
			a.bindInto(e, local)
		}
	}
}
