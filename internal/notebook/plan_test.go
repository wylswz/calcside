package notebook

import (
	"reflect"
	"testing"
)

func planIn(targets []string, bulk bool, fresh, once []string) PlanInput {
	in := PlanInput{
		Targets:  targets,
		Bulk:     bulk,
		Fresh:    map[string]bool{},
		OnceDone: map[string]bool{},
	}
	for _, n := range fresh {
		in.Fresh[n] = true
	}
	for _, n := range once {
		in.OnceDone[n] = true
	}
	return in
}

func planNodes(g Graph, in PlanInput) []PlanNode { return g.Plan(in).Nodes }

func TestGraphPlanOrderAndReuse(t *testing.T) {
	t.Run("reverse page order runs provider first", func(t *testing.T) {
		g := BuildGraph([]GraphSection{
			gsec("use", nil, []string{"x"}), gsec("make", []string{"x"}, nil),
		})
		got := planNodes(g, planIn([]string{"use"}, false, nil, nil))
		want := []PlanNode{
			{Name: "make", Action: ActionRun, Reason: "upstream: not fresh"},
			{Name: "use", Action: ActionRun, Reason: "target"},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("nodes=%+v want %+v", got, want)
		}
	})

	t.Run("fresh upstream reused, target always runs", func(t *testing.T) {
		g := BuildGraph([]GraphSection{
			gsec("make", []string{"x"}, nil), gsec("use", nil, []string{"x"}),
		})
		got := planNodes(g, planIn([]string{"use"}, false, []string{"make", "use"}, nil))
		want := []PlanNode{
			{Name: "make", Action: ActionReuse, Reason: "fresh"},
			{Name: "use", Action: ActionRun, Reason: "target"},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("nodes=%+v want %+v", got, want)
		}
	})

	t.Run("non-fresh grandparent forces fresh parent to run", func(t *testing.T) {
		// a defines x; b defines y reads x; c defines z reads y.
		// b and c are fresh, but a is not — b's inputs change, so b
		// must re-run, and c is the explicit target.
		g := BuildGraph([]GraphSection{
			gsec("a", []string{"x"}, nil),
			gsec("b", []string{"y"}, []string{"x"}),
			gsec("c", []string{"z"}, []string{"y"}),
		})
		got := planNodes(g, planIn([]string{"c"}, false, []string{"b", "c"}, nil))
		want := []PlanNode{
			{Name: "a", Action: ActionRun, Reason: "upstream: not fresh"},
			{Name: "b", Action: ActionRun, Reason: "upstream a runs first"},
			{Name: "c", Action: ActionRun, Reason: "target"},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("nodes=%+v want %+v", got, want)
		}
	})

	t.Run("diamond runs shared upstream once", func(t *testing.T) {
		// a feeds b and c; both feed d. Target d, nothing fresh.
		g := BuildGraph([]GraphSection{
			gsec("a", []string{"x"}, nil),
			gsec("b", []string{"y"}, []string{"x"}),
			gsec("c", []string{"z"}, []string{"x"}),
			gsec("d", nil, []string{"y", "z"}),
		})
		got := planNodes(g, planIn([]string{"d"}, false, nil, nil))
		want := []PlanNode{
			{Name: "a", Action: ActionRun, Reason: "upstream: not fresh"},
			{Name: "b", Action: ActionRun, Reason: "upstream: not fresh"},
			{Name: "c", Action: ActionRun, Reason: "upstream: not fresh"},
			{Name: "d", Action: ActionRun, Reason: "target"},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("nodes=%+v want %+v", got, want)
		}
	})

	t.Run("fresh non-target reuses when providers only reuse", func(t *testing.T) {
		// a→b→c all fresh; target c runs, a and b reuse — b stays
		// fresh because its provider a is not re-run in this plan.
		g := BuildGraph([]GraphSection{
			gsec("a", []string{"x"}, nil),
			gsec("b", []string{"y"}, []string{"x"}),
			gsec("c", nil, []string{"y"}),
		})
		got := planNodes(g, planIn([]string{"c"}, false, []string{"a", "b", "c"}, nil))
		want := []PlanNode{
			{Name: "a", Action: ActionReuse, Reason: "fresh"},
			{Name: "b", Action: ActionReuse, Reason: "fresh"},
			{Name: "c", Action: ActionRun, Reason: "target"},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("nodes=%+v want %+v", got, want)
		}
	})
}

func TestGraphPlanOnce(t *testing.T) {
	t.Run("bulk skips fresh once-done leaf", func(t *testing.T) {
		// "all": p provides x, leaf reads it and is once+done+fresh.
		g := BuildGraph([]GraphSection{
			gsec("p", []string{"x"}, nil), gsec("leaf", nil, []string{"x"}),
		})
		got := planNodes(g, planIn([]string{"p", "leaf"}, true,
			[]string{"p", "leaf"}, []string{"leaf"}))
		want := []PlanNode{
			{Name: "p", Action: ActionRun, Reason: "target"},
			{Name: "leaf", Action: ActionSkip, Reason: "once section already succeeded"},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("nodes=%+v want %+v", got, want)
		}
	})

	t.Run("bulk non-fresh once-done provider of a target runs", func(t *testing.T) {
		// leaf is once+done but stale; target leaf2 reads leaf's x.
		// Skipping leaf as a target must not skip it as an upstream.
		g := BuildGraph([]GraphSection{
			gsec("leaf", []string{"x"}, nil), gsec("leaf2", nil, []string{"x"}),
		})
		got := planNodes(g, planIn([]string{"leaf", "leaf2"}, true,
			nil, []string{"leaf"}))
		want := []PlanNode{
			{Name: "leaf", Action: ActionRun, Reason: "upstream: not fresh"},
			{Name: "leaf2", Action: ActionRun, Reason: "target"},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("nodes=%+v want %+v", got, want)
		}
	})

	t.Run("explicit once target runs", func(t *testing.T) {
		g := BuildGraph([]GraphSection{gsec("once", []string{"x"}, nil)})
		got := planNodes(g, planIn([]string{"once"}, false, []string{"once"}, []string{"once"}))
		want := []PlanNode{{Name: "once", Action: ActionRun, Reason: "target"}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("nodes=%+v want %+v", got, want)
		}
	})

	t.Run("skipped once targets report in notebook order", func(t *testing.T) {
		g := BuildGraph([]GraphSection{
			gsec("zeta", []string{"z"}, nil), gsec("alpha", []string{"a"}, nil),
			gsec("main", nil, nil),
		})
		got := planNodes(g, planIn([]string{"zeta", "alpha", "main"}, true,
			[]string{"zeta", "alpha", "main"}, []string{"zeta", "alpha"}))
		want := []PlanNode{
			{Name: "main", Action: ActionRun, Reason: "target"},
			{Name: "zeta", Action: ActionSkip, Reason: "once section already succeeded"},
			{Name: "alpha", Action: ActionSkip, Reason: "once section already succeeded"},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("nodes=%+v want %+v", got, want)
		}
	})
}

func TestGraphPlanBlocked(t *testing.T) {
	blockedKinds := func(g Graph, in PlanInput) []string {
		p := g.Plan(in)
		if len(p.Nodes) != 0 {
			t.Fatalf("blocked plan still has nodes: %+v", p.Nodes)
		}
		var kinds []string
		for _, d := range p.Blocked {
			kinds = append(kinds, d.Kind)
		}
		return kinds
	}

	t.Run("missing provider in closure blocks", func(t *testing.T) {
		g := BuildGraph([]GraphSection{gsec("c", nil, []string{"x"})})
		if k := blockedKinds(g, planIn([]string{"c"}, false, nil, nil)); !reflect.DeepEqual(k, []string{DiagMissingProvider}) {
			t.Fatalf("blocked=%v", k)
		}
	})

	t.Run("read duplicate in closure blocks", func(t *testing.T) {
		g := BuildGraph([]GraphSection{
			gsec("a", []string{"x"}, nil), gsec("b", []string{"x"}, nil), gsec("c", nil, []string{"x"}),
		})
		if k := blockedKinds(g, planIn([]string{"c"}, false, nil, nil)); !reflect.DeepEqual(k, []string{DiagDuplicateDefine}) {
			t.Fatalf("blocked=%v", k)
		}
	})

	t.Run("cycle in closure blocks", func(t *testing.T) {
		g := BuildGraph([]GraphSection{
			gsec("a", []string{"x"}, []string{"y"}), gsec("b", []string{"y"}, []string{"x"}),
		})
		if k := blockedKinds(g, planIn([]string{"a"}, false, nil, nil)); !reflect.DeepEqual(k, []string{DiagCycle}) {
			t.Fatalf("blocked=%v", k)
		}
	})

	t.Run("unrelated broken section does not block", func(t *testing.T) {
		g := BuildGraph([]GraphSection{
			gsec("bad", nil, []string{"x"}), gsec("ok", []string{"y"}, nil),
		})
		got := planNodes(g, planIn([]string{"ok"}, false, nil, nil))
		want := []PlanNode{{Name: "ok", Action: ActionRun, Reason: "target"}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("nodes=%+v want %+v", got, want)
		}
	})

	t.Run("cycle outside closure does not block", func(t *testing.T) {
		// a↔b cycle, c independent — Order is nil graph-wide but the
		// closure is acyclic, so the Kahn fallback orders it.
		g := BuildGraph([]GraphSection{
			gsec("a", []string{"x"}, []string{"y"}), gsec("b", []string{"y"}, []string{"x"}),
			gsec("c", []string{"z"}, nil),
		})
		p := g.Plan(planIn([]string{"c"}, false, nil, nil))
		if len(p.Blocked) != 0 {
			t.Fatalf("blocked=%+v", p.Blocked)
		}
		want := []PlanNode{{Name: "c", Action: ActionRun, Reason: "target"}}
		if !reflect.DeepEqual(p.Nodes, want) {
			t.Fatalf("nodes=%+v want %+v", p.Nodes, want)
		}
	})

	t.Run("parse error never blocks", func(t *testing.T) {
		// The parse-failed section is the target itself: it runs and
		// fails with the real syntax error.
		g := BuildGraph([]GraphSection{
			gsecBad("p", nil, nil), gsec("ok", []string{"y"}, nil),
		})
		p := g.Plan(planIn([]string{"p"}, false, nil, nil))
		if len(p.Blocked) != 0 {
			t.Fatalf("blocked=%+v", p.Blocked)
		}
		want := []PlanNode{{Name: "p", Action: ActionRun, Reason: "target"}}
		if !reflect.DeepEqual(p.Nodes, want) {
			t.Fatalf("nodes=%+v want %+v", p.Nodes, want)
		}
	})

	t.Run("unread duplicate does not block", func(t *testing.T) {
		g := BuildGraph([]GraphSection{
			gsec("a", []string{"x"}, nil), gsec("b", []string{"x"}, nil),
		})
		p := g.Plan(planIn([]string{"a"}, false, nil, nil))
		if len(p.Blocked) != 0 {
			t.Fatalf("blocked=%+v", p.Blocked)
		}
		want := []PlanNode{{Name: "a", Action: ActionRun, Reason: "target"}}
		if !reflect.DeepEqual(p.Nodes, want) {
			t.Fatalf("nodes=%+v want %+v", p.Nodes, want)
		}
	})

	t.Run("unknown target ignored", func(t *testing.T) {
		g := BuildGraph([]GraphSection{gsec("use", nil, []string{"x"}), gsec("make", []string{"x"}, nil)})
		got := planNodes(g, planIn([]string{"ghost", "use"}, false, nil, nil))
		want := []PlanNode{
			{Name: "make", Action: ActionRun, Reason: "upstream: not fresh"},
			{Name: "use", Action: ActionRun, Reason: "target"},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("nodes=%+v want %+v", got, want)
		}
	})
}
