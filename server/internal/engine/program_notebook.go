package engine

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"

	"calcside/internal/notebook"
)

// program_notebook.go implements the EP-0025 notebook program shape: an
// immutable release whose sources are ordinary notebook sections
// rather than a load()-linked bundle. Entry points are uniquely bound
// top-level one-argument functions — `main` for public invoke, handler
// names for interrupt resume/expiry. The section path is the stable
// internal identifier; the dependency graph comes from the same
// static analysis instance notebooks use.

// notebookProgram is the prepared notebook plan: the validated
// dependency graph, the entry-name→owning-section index, and each
// section's imported edges keyed by consumer.
type notebookProgram struct {
	graph   notebook.Graph
	entries map[string]string
	imports map[string][]notebook.Edge // keyed by edge.To
}

// entryCandidate reports whether a root-level def is shaped like an
// entry function: exactly one positional parameter, optionally with a
// default (matching checkEntry); no star or kwargs.
func entryCandidate(d *syntax.DefStmt) bool {
	if len(d.Params) != 1 {
		return false
	}
	switch p := d.Params[0].(type) {
	case *syntax.Ident:
		return true
	case *syntax.BinaryExpr:
		if p.Op == syntax.EQ {
			_, ok := p.X.(*syntax.Ident)
			return ok
		}
	}
	return false
}

// compileNotebook parses, analyzes and compiles every section of a
// notebook release. The whole bundle is validated at prepare — any
// graph diagnostic is an invalid program, since an immutable release
// cannot carry a broken graph. Errors stay generic: no source text or
// user names leak through ProgramResult.
func (p *Program) compileNotebook(ctx context.Context, s *Session) *ProgramError {
	opts := *fileOpts
	// Notebook sections use the load()-binds-globally dialect so an
	// explicit load alias is exported exactly as AnalyzeFile counts
	// it; bare (non-@) loads are still rejected below.
	opts.LoadBindsGlobally = true

	preNames := slices.Sorted(maps.Keys(s.Predeclared))
	files := map[string]*syntax.File{}
	sections := make([]notebook.GraphSection, 0, len(p.sources))
	bindCounts := map[string]int{}
	candidates := map[string]string{} // entry-shaped def name -> section
	for _, name := range slices.Sorted(maps.Keys(p.sources)) {
		if perr := cancelledProgramErr(ctx); perr != nil {
			return perr
		}
		f, err := opts.Parse(name, p.sources[name], 0)
		if err != nil {
			return invalidProgramf("a selected source does not compile")
		}
		files[name] = f
		defines, uses, binds := notebook.AnalyzeFile(f, preNames)
		sections = append(sections, notebook.GraphSection{
			Name: name, Defines: defines, Uses: uses, ParseOK: true,
		})
		for n, c := range binds {
			bindCounts[n] += c
		}
		for _, st := range f.Stmts {
			d, ok := st.(*syntax.DefStmt)
			if !ok || !entryCandidate(d) {
				continue
			}
			if d.Name.Name == "main" || HandlerName(d.Name.Name) {
				candidates[d.Name.Name] = name
			}
		}
	}

	// An entry name must be bound exactly once across the whole
	// release — a second def, assignment or load shadow disqualifies
	// it. Ambiguous names that nothing routes to do not fail prepare.
	entries := map[string]string{}
	for name, sec := range candidates {
		if bindCounts[name] == 1 {
			entries[name] = sec
		}
	}
	if entries["main"] == "" {
		return invalidProgramf("notebook program must define a unique main function")
	}

	graph := notebook.BuildGraph(sections)
	if len(graph.Diagnostics) > 0 {
		return invalidProgramf("notebook section graph is invalid")
	}
	imports := map[string][]notebook.Edge{}
	for _, e := range graph.Edges {
		imports[e.To] = append(imports[e.To], e)
	}

	programs := map[string]*starlark.Program{}
	for _, name := range slices.Sorted(maps.Keys(files)) {
		if perr := cancelledProgramErr(ctx); perr != nil {
			return perr
		}
		inputNames := map[string]bool{}
		for _, e := range imports[name] {
			inputNames[e.Name] = true
		}
		pr, err := starlark.FileProgram(files[name], func(n string) bool {
			return s.Predeclared.Has(n) || inputNames[n]
		})
		if err != nil {
			return invalidProgramf("a selected source does not compile")
		}
		// Notebook sections may only load pinned @ mounts; a bare
		// load would bypass the section graph entirely.
		for i := 0; i < pr.NumLoads(); i++ {
			mod, _ := pr.Load(i)
			if !strings.HasPrefix(mod, "@") {
				return invalidProgramf("notebook sections cannot load bundle sources")
			}
		}
		programs[name] = pr
	}
	p.programs = programs
	p.notebook = &notebookProgram{graph: graph, entries: entries, imports: imports}
	return nil
}

// initNotebook initializes the transitive dependency closure of the
// section owning entry, in topological order, inside one fresh
// invocation session. Each section gets its own namespace: a copy of
// the session predeclared set plus exactly the names its edges import
// from already-initialized providers. Provider globals are frozen
// before export, so shared data stays read-only. The gate stays
// disarmed — callers run this before arming the entry call. No
// section state escapes the call.
func (p *Program) initNotebook(thread *starlark.Thread, s *Session, entry string) (starlark.StringDict, error) {
	target := p.notebook.entries[entry]
	if target == "" {
		return nil, fmt.Errorf("unknown entry %q", entry)
	}
	plan := p.notebook.graph.Plan(notebook.PlanInput{Targets: []string{target}})
	if len(plan.Blocked) > 0 {
		return nil, fmt.Errorf("notebook section graph is blocked")
	}
	ms := s.modules
	if ms == nil {
		ms = &moduleSet{cache: map[string]starlark.StringDict{}, inited: map[string]bool{}}
	}
	// There is no bare load namespace for notebook sections; @ mounts
	// resolve through the session's attached modules as usual.
	thread.Load = ms.loadFunc(s.Predeclared, nil)
	initialized := map[string]starlark.StringDict{}
	for _, node := range plan.Nodes {
		if node.Action != notebook.ActionRun {
			continue
		}
		pre := maps.Clone(s.Predeclared)
		for _, e := range p.notebook.imports[node.Name] {
			v, ok := initialized[e.From][e.Name]
			if !ok {
				return nil, fmt.Errorf("notebook provider did not export a dependency")
			}
			pre[e.Name] = v
		}
		globals, err := p.programs[node.Name].Init(thread, pre)
		if err != nil {
			return nil, err
		}
		globals.Freeze()
		initialized[node.Name] = globals
	}
	g, ok := initialized[target]
	if !ok {
		return nil, fmt.Errorf("entry section did not initialize")
	}
	return g, nil
}
