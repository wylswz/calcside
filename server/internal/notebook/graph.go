package notebook

import (
	"fmt"
	"sort"
	"strings"
)

// GraphSection is one section's static facts for dependency-graph
// building: the input order of the slice is the notebook's section
// order. ParseOK false means the code did not parse — its defines and
// uses are treated as empty and it earns a parse_error diagnostic.
type GraphSection struct {
	Name    string
	Defines []string
	Uses    []string
	ParseOK bool
}

// Edge is one variable dependency: From defines Name, To reads it.
type Edge struct {
	From string
	To   string
	Name string
}

// Diagnostic kinds emitted by BuildGraph.
const (
	DiagDuplicateDefine = "duplicate_define"
	DiagCycle           = "cycle"
	DiagMissingProvider = "missing_provider"
	DiagParseError      = "parse_error"
)

// Diagnostic is one graph problem. Sections lists the involved section
// names (a cycle reports one concrete path, start first, without
// repeating it); Names lists the involved variable names — sorted
// missing names, the duplicated name, or the per-hop cycle labels.
type Diagnostic struct {
	Kind     string
	Message  string
	Sections []string
	Names    []string
}

// Graph is the dependency view of a notebook: provider→consumer edges
// derived from current saved code, diagnostics, and Order — a Kahn
// topological order over every input section (ready set by notebook
// position), nil when any cycle exists. names keeps the input section
// order for Plan's notebook-position tie breaks; it is not part of the
// API surface.
type Graph struct {
	Edges       []Edge
	Diagnostics []Diagnostic
	Order       []string
	names       []string
}

// BuildGraph derives the dependency graph from per-section static
// facts. A name defined by two or more sections is ambiguous: it earns
// one duplicate_define diagnostic and produces no edges. A use with no
// definer — or whose only definer is the reader itself (read before
// bind) — is missing_provider. Cycles are reported per strongly
// connected component as one shortest concrete path through the
// component's earliest section.
func BuildGraph(secs []GraphSection) Graph {
	n := len(secs)
	g := Graph{Edges: []Edge{}, Diagnostics: []Diagnostic{}}
	g.names = make([]string, 0, n)
	for _, s := range secs {
		g.names = append(g.names, s.Name)
	}

	// definers maps each defined name to the indices of sections
	// defining it, in notebook order. Parse failures define nothing.
	definers := map[string][]int{}
	parseDiags := []Diagnostic{}
	for i, s := range secs {
		if !s.ParseOK {
			parseDiags = append(parseDiags, Diagnostic{
				Kind: DiagParseError, Message: fmt.Sprintf("%s does not parse",
					s.Name), Sections: []string{s.Name}, Names: []string{},
			})
			continue
		}
		seenDef := map[string]bool{}
		for _, d := range s.Defines {
			if seenDef[d] {
				continue
			}
			seenDef[d] = true
			definers[d] = append(definers[d], i)
		}
	}

	// readers maps each used name to the indices of sections reading
	// it, in notebook order. Parse failures read nothing.
	readers := map[string][]int{}
	for i, s := range secs {
		if !s.ParseOK {
			continue
		}
		seenUse := map[string]bool{}
		for _, u := range s.Uses {
			if seenUse[u] {
				continue
			}
			seenUse[u] = true
			readers[u] = append(readers[u], i)
		}
	}

	// An ambiguous name is only a problem when something reads it —
	// unread duplicates (loop vars, temporaries) are harmless. The
	// diagnostic lists definers first, then readers not already
	// listed, each group in notebook order.
	dup := map[string]bool{}
	dupDiags := []Diagnostic{}
	for name, ds := range definers {
		if len(ds) < 2 || len(readers[name]) == 0 {
			continue
		}
		dup[name] = true
		inSet := map[int]bool{}
		var order []int
		for _, i := range ds {
			inSet[i] = true
			order = append(order, i)
		}
		for _, i := range readers[name] {
			if !inSet[i] {
				inSet[i] = true
				order = append(order, i)
			}
		}
		names := make([]string, 0, len(order))
		for _, i := range order {
			names = append(names, secs[i].Name)
		}
		dupDiags = append(dupDiags, Diagnostic{
			Kind:     DiagDuplicateDefine,
			Message:  fmt.Sprintf("%s is defined by %s", name, strings.Join(names[:len(ds)], ", ")),
			Sections: names, Names: []string{name},
		})
	}
	sort.Slice(dupDiags, func(i, j int) bool { return dupDiags[i].Names[0] < dupDiags[j].Names[0] })

	// Edges and missing providers. Edge indices stay internal until
	// sorted; emitted pairs carry the lexicographically smallest
	// variable name for cycle labels.
	type rawEdge struct {
		from, to int
		name     string
	}
	raw := []rawEdge{}
	missing := make([][]string, n)
	for c, s := range secs {
		if !s.ParseOK {
			continue
		}
		seen := map[string]bool{}
		for _, u := range s.Uses {
			if seen[u] || dup[u] {
				continue
			}
			seen[u] = true
			ds := definers[u]
			switch {
			case len(ds) == 0:
				missing[c] = append(missing[c], u)
			case len(ds) == 1 && ds[0] == c:
				missing[c] = append(missing[c], u)
			case len(ds) == 1:
				raw = append(raw, rawEdge{ds[0], c, u})
			}
		}
	}

	sort.Slice(raw, func(i, j int) bool {
		a, b := raw[i], raw[j]
		if a.from != b.from {
			return a.from < b.from
		}
		if a.to != b.to {
			return a.to < b.to
		}
		return a.name < b.name
	})
	pairName := map[[2]int]string{}
	for _, e := range raw {
		key := [2]int{e.from, e.to}
		if cur, ok := pairName[key]; !ok || e.name < cur {
			pairName[key] = e.name
		}
		g.Edges = append(g.Edges, Edge{From: secs[e.from].Name, To: secs[e.to].Name, Name: e.name})
	}

	// Diagnostics are emitted grouped: parse errors (section order),
	// duplicates (name order), missing providers (section order),
	// cycles (start-section order).
	missDiags := []Diagnostic{}
	for c, s := range secs {
		if !s.ParseOK || len(missing[c]) == 0 {
			continue
		}
		sort.Strings(missing[c])
		var msg string
		if len(missing[c]) == 1 {
			msg = fmt.Sprintf("%s reads %s — no section defines it", s.Name, missing[c][0])
		} else {
			msg = fmt.Sprintf("%s reads %s — no section defines them", s.Name, strings.Join(missing[c], ", "))
		}
		missDiags = append(missDiags, Diagnostic{
			Kind: DiagMissingProvider, Message: msg,
			Sections: []string{s.Name}, Names: missing[c],
		})
	}
	g.Diagnostics = append(g.Diagnostics, parseDiags...)
	g.Diagnostics = append(g.Diagnostics, dupDiags...)
	g.Diagnostics = append(g.Diagnostics, missDiags...)

	// Adjacency over emitted edges for cycle detection and topo order.
	// succ[v] holds successor indices sorted ascending — index order is
	// notebook order, so traversal is deterministic. Several variable
	// names on one pair collapse to a single hop.
	succ := make([][]int, n)
	for _, e := range raw {
		succ[e.from] = append(succ[e.from], e.to)
	}
	indeg := make([]int, n)
	for i := range succ {
		sort.Ints(succ[i])
		out := succ[i][:0]
		prev := -1
		for _, w := range succ[i] {
			if w != prev {
				out = append(out, w)
				prev = w
				indeg[w]++
			}
		}
		succ[i] = out
	}

	// Tarjan SCCs; only components with more than one node are cycles.
	idx := make([]int, n)
	low := make([]int, n)
	onStack := make([]bool, n)
	for i := range idx {
		idx[i] = -1
	}
	counter := 0
	stack := []int{}
	var sccs [][]int
	var strong func(v int)
	strong = func(v int) {
		idx[v], low[v] = counter, counter
		counter++
		stack = append(stack, v)
		onStack[v] = true
		for _, w := range succ[v] {
			if idx[w] == -1 {
				strong(w)
				if low[w] < low[v] {
					low[v] = low[w]
				}
			} else if onStack[w] && idx[w] < low[v] {
				low[v] = idx[w]
			}
		}
		if low[v] == idx[v] {
			var comp []int
			for {
				w := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				onStack[w] = false
				comp = append(comp, w)
				if w == v {
					break
				}
			}
			sccs = append(sccs, comp)
		}
	}
	for v := 0; v < n; v++ {
		if idx[v] == -1 {
			strong(v)
		}
	}

	type cycDiag struct {
		start int
		d     Diagnostic
	}
	cycleDiags := []cycDiag{}
	for _, comp := range sccs {
		if len(comp) < 2 {
			continue
		}
		start := comp[0]
		for _, v := range comp[1:] {
			if v < start {
				start = v
			}
		}
		path := shortestCycle(start, succ, n)
		if len(path) == 0 {
			continue
		}
		secNames := make([]string, 0, len(path))
		for _, v := range path {
			secNames = append(secNames, secs[v].Name)
		}
		hopNames := make([]string, 0, len(path))
		var msg strings.Builder
		for i, v := range path {
			next := path[(i+1)%len(path)]
			label := pairName[[2]int{v, next}]
			hopNames = append(hopNames, label)
			if i > 0 {
				msg.WriteString("; ")
			}
			fmt.Fprintf(&msg, "%s → %s via %s", secs[v].Name, secs[next].Name, label)
		}
		cycleDiags = append(cycleDiags, cycDiag{start, Diagnostic{
			Kind: DiagCycle, Message: msg.String(),
			Sections: secNames, Names: hopNames,
		}})
	}
	sort.Slice(cycleDiags, func(i, j int) bool { return cycleDiags[i].start < cycleDiags[j].start })
	for _, c := range cycleDiags {
		g.Diagnostics = append(g.Diagnostics, c.d)
	}

	// Kahn topo order over every input section; the ready set is the
	// lowest notebook index, keeping the order deterministic. Any
	// leftover means a cycle — Order stays nil then.
	indeg2 := make([]int, n)
	copy(indeg2, indeg)
	ready := []int{}
	for i := 0; i < n; i++ {
		if indeg2[i] == 0 {
			ready = append(ready, i)
		}
	}
	order := []int{}
	for len(ready) > 0 {
		v := ready[0]
		ready = ready[1:]
		order = append(order, v)
		for _, w := range succ[v] {
			indeg2[w]--
			if indeg2[w] == 0 {
				// insert keeping ready sorted by index
				p := sort.SearchInts(ready, w)
				ready = append(ready, 0)
				copy(ready[p+1:], ready[p:])
				ready[p] = w
			}
		}
	}
	if len(order) == n {
		g.Order = make([]string, 0, n)
		for _, v := range order {
			g.Order = append(g.Order, secs[v].Name)
		}
	}
	return g
}

// shortestCycle returns the node indices of a shortest directed cycle
// through start: BFS over successors in notebook order, first return
// edge to start wins. The path lists start first and never repeats it.
// Returns nil when start lies on no cycle.
func shortestCycle(start int, succ [][]int, n int) []int {
	prev := make([]int, n)
	seen := make([]bool, n)
	for i := range prev {
		prev[i] = -1
	}
	queue := []int{start}
	seen[start] = true
	found := -1
	for qi := 0; qi < len(queue) && found < 0; qi++ {
		v := queue[qi]
		for _, w := range succ[v] {
			if w == start {
				found = v
				break
			}
			if !seen[w] {
				seen[w] = true
				prev[w] = v
				queue = append(queue, w)
			}
		}
	}
	if found < 0 {
		return nil
	}
	path := []int{found}
	for v := found; v != start; v = prev[v] {
		path = append(path, prev[v])
	}
	// path is found..start reversed-tail; reverse to start..found
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
	return path
}
