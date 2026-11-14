package notebook

import "sort"

// Plan actions assigned to each closure node.
const (
	ActionRun   = "run"
	ActionReuse = "reuse"
	ActionSkip  = "skip"
)

// PlanNode is one section's planned outcome. Reason explains the
// action: "target", "upstream: not fresh", "upstream <name> runs
// first", "fresh", or "once section already succeeded".
type PlanNode struct {
	Name   string
	Action string
	Reason string
}

// PlanInput describes one run request against derived section states.
// Targets are requested section names (unknown names are ignored —
// callers validate). Bulk marks an "all"/"stale" selection, where the
// once rule applies. Fresh reports state == fresh per section; OnceDone
// reports rerun == once with a prior success.
type PlanInput struct {
	Targets  []string
	Bulk     bool
	Fresh    map[string]bool
	OnceDone map[string]bool
}

// Plan is the planner's answer: Nodes lists every closure member in
// execution order followed by skipped once targets in notebook order;
// Blocked lists the diagnostics intersecting the closure (in graph
// diagnostic order) — when non-empty Nodes is empty and nothing may
// run.
type Plan struct {
	Nodes   []PlanNode
	Blocked []Diagnostic
}

// Plan computes the execution plan for a run request. The closure is
// the remaining targets plus every transitive provider; a blocking
// diagnostic touching the closure stops everything. Otherwise nodes
// walk the closure in topological order (Order restricted to the
// closure, Kahn over the closure when a cycle elsewhere nilled Order):
// targets always run, non-fresh upstreams run, fresh nodes run when a
// direct provider runs in this plan (inputs changed), and the rest
// reuse.
func (g Graph) Plan(in PlanInput) Plan {
	p := Plan{Nodes: []PlanNode{}, Blocked: []Diagnostic{}}
	pos := make(map[string]int, len(g.names))
	for i, n := range g.names {
		pos[n] = i
	}
	known := func(n string) bool { _, ok := pos[n]; return ok }

	// providers maps a consumer to its direct provider names.
	providers := map[string][]string{}
	for _, e := range g.Edges {
		dup := false
		for _, pr := range providers[e.To] {
			if pr == e.From {
				dup = true
				break
			}
		}
		if !dup {
			providers[e.To] = append(providers[e.To], e.From)
		}
	}
	for _, prs := range providers {
		sort.Slice(prs, func(i, j int) bool { return pos[prs[i]] < pos[prs[j]] })
	}

	// Bulk selections drop once-done targets up front; they may still
	// re-enter the closure as upstream providers.
	targets := map[string]bool{}
	skipped := map[string]bool{}
	for _, t := range in.Targets {
		if !known(t) || targets[t] || skipped[t] {
			continue
		}
		if in.Bulk && in.OnceDone[t] {
			skipped[t] = true
			continue
		}
		targets[t] = true
	}

	// closure = remaining targets + all transitive providers.
	closure := map[string]bool{}
	stack := []string{}
	for t := range targets {
		if !closure[t] {
			closure[t] = true
			stack = append(stack, t)
		}
	}
	for len(stack) > 0 {
		c := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, pr := range providers[c] {
			if !closure[pr] {
				closure[pr] = true
				stack = append(stack, pr)
			}
		}
	}

	// Blocking diagnostics: duplicate_define, missing_provider and
	// cycle touch the closure; parse_error never does — the section
	// runs and fails with the real syntax error.
	for _, d := range g.Diagnostics {
		if d.Kind == DiagParseError {
			continue
		}
		for _, s := range d.Sections {
			if closure[s] {
				p.Blocked = append(p.Blocked, d)
				break
			}
		}
	}
	if len(p.Blocked) > 0 {
		return p
	}

	// Execution order: Order restricted to the closure when the graph
	// is acyclic; otherwise Kahn over the closure subgraph (reachable
	// only when the cycle lies outside the closure).
	var order []string
	if g.Order != nil {
		for _, n := range g.Order {
			if closure[n] {
				order = append(order, n)
			}
		}
	} else {
		order = closureOrder(providers, closure, pos)
	}

	runSet := map[string]bool{}
	for _, n := range order {
		action, reason := ActionReuse, "fresh"
		switch {
		case targets[n]:
			action, reason = ActionRun, "target"
		case !in.Fresh[n]:
			action, reason = ActionRun, "upstream: not fresh"
		default:
			for _, pr := range providers[n] {
				if runSet[pr] {
					action, reason = ActionRun, "upstream "+pr+" runs first"
					break
				}
			}
		}
		if action == ActionRun {
			runSet[n] = true
		}
		p.Nodes = append(p.Nodes, PlanNode{Name: n, Action: action, Reason: reason})
	}

	// Skipped once targets that did not re-enter as upstream, in
	// notebook order.
	var skippedNames []string
	for n := range skipped {
		if !closure[n] {
			skippedNames = append(skippedNames, n)
		}
	}
	sort.Slice(skippedNames, func(i, j int) bool { return pos[skippedNames[i]] < pos[skippedNames[j]] })
	for _, n := range skippedNames {
		p.Nodes = append(p.Nodes, PlanNode{
			Name: n, Action: ActionSkip, Reason: "once section already succeeded",
		})
	}
	return p
}

// closureOrder Kahn-sorts the closure subgraph with the ready set
// ordered by notebook position. Used only when a cycle elsewhere in
// the graph nilled Order — an unblocked closure is always acyclic.
func closureOrder(providers map[string][]string, closure map[string]bool, pos map[string]int) []string {
	consumers := map[string][]string{}
	indeg := map[string]int{}
	for n := range closure {
		indeg[n] = 0
	}
	for c := range closure {
		for _, pr := range providers[c] {
			if !closure[pr] {
				continue
			}
			indeg[c]++
			consumers[pr] = append(consumers[pr], c)
		}
	}
	ready := []string{}
	for n := range closure {
		if indeg[n] == 0 {
			ready = append(ready, n)
		}
	}
	sort.Slice(ready, func(i, j int) bool { return pos[ready[i]] < pos[ready[j]] })
	order := []string{}
	for len(ready) > 0 {
		n := ready[0]
		ready = ready[1:]
		order = append(order, n)
		for _, c := range consumers[n] {
			indeg[c]--
			if indeg[c] == 0 {
				ins := len(ready)
				for i, r := range ready {
					if pos[r] > pos[c] {
						ins = i
						break
					}
				}
				ready = append(ready, "")
				copy(ready[ins+1:], ready[ins:])
				ready[ins] = c
			}
		}
	}
	return order
}
