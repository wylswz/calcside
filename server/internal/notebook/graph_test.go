package notebook

import (
	"reflect"
	"testing"
)

func gsec(name string, defines, uses []string) GraphSection {
	return GraphSection{Name: name, Defines: defines, Uses: uses, ParseOK: true}
}

func gsecBad(name string, defines, uses []string) GraphSection {
	return GraphSection{Name: name, Defines: defines, Uses: uses, ParseOK: false}
}

func edgeNames(g Graph) [][3]string {
	var out [][3]string
	for _, e := range g.Edges {
		out = append(out, [3]string{e.From, e.To, e.Name})
	}
	return out
}

func TestBuildGraphEdgesAndOrder(t *testing.T) {
	cases := []struct {
		name  string
		secs  []GraphSection
		edges [][3]string
		order []string
	}{
		{
			// Consumer declared before its provider: the edge still
			// points provider → consumer and Order puts provider first.
			"chain reversed page order",
			[]GraphSection{gsec("use", nil, []string{"x"}), gsec("make", []string{"x"}, nil)},
			[][3]string{{"make", "use", "x"}},
			[]string{"make", "use"},
		},
		{
			// Diamond: a feeds b and c; both feed d.
			"diamond",
			[]GraphSection{
				gsec("a", []string{"x"}, nil),
				gsec("b", []string{"y"}, []string{"x"}),
				gsec("c", []string{"z"}, []string{"x"}),
				gsec("d", nil, []string{"y", "z"}),
			},
			[][3]string{
				{"a", "b", "x"}, {"a", "c", "x"}, {"b", "d", "y"}, {"c", "d", "z"},
			},
			[]string{"a", "b", "c", "d"},
		},
		{
			"independent nodes keep input order",
			[]GraphSection{gsec("c", nil, nil), gsec("a", nil, nil), gsec("b", nil, nil)},
			nil,
			[]string{"c", "a", "b"},
		},
		{
			// Two names on one pair: two edges, one topo hop.
			"multi-name pair",
			[]GraphSection{gsec("p", []string{"x", "y"}, nil), gsec("q", nil, []string{"x", "y"})},
			[][3]string{{"p", "q", "x"}, {"p", "q", "y"}},
			[]string{"p", "q"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := BuildGraph(tc.secs)
			if got := edgeNames(g); !reflect.DeepEqual(got, tc.edges) {
				t.Errorf("edges=%v want %v", got, tc.edges)
			}
			if !reflect.DeepEqual(g.Order, tc.order) {
				t.Errorf("order=%v want %v", g.Order, tc.order)
			}
			if len(g.Diagnostics) != 0 {
				t.Errorf("unexpected diagnostics: %+v", g.Diagnostics)
			}
		})
	}
}

func TestBuildGraphDiagnostics(t *testing.T) {
	t.Run("duplicate unread is allowed", func(t *testing.T) {
		// Loop vars and temporaries may be defined twice; with no
		// reader the ambiguity is harmless — no diagnostic, no edges.
		g := BuildGraph([]GraphSection{gsec("a", []string{"x"}, nil), gsec("b", []string{"x"}, nil)})
		if len(g.Diagnostics) != 0 {
			t.Fatalf("diagnostics: %+v", g.Diagnostics)
		}
		if len(g.Edges) != 0 {
			t.Fatalf("edges: %+v", g.Edges)
		}
	})

	t.Run("duplicate read lists definers then readers", func(t *testing.T) {
		g := BuildGraph([]GraphSection{
			gsec("a", []string{"x"}, nil), gsec("b", []string{"x"}, nil), gsec("c", nil, []string{"x"}),
		})
		if len(g.Diagnostics) != 1 || g.Diagnostics[0].Kind != DiagDuplicateDefine {
			t.Fatalf("diagnostics: %+v", g.Diagnostics)
		}
		d := g.Diagnostics[0]
		if d.Message != "x is defined by a, b" {
			t.Fatalf("message: %q", d.Message)
		}
		if !reflect.DeepEqual(d.Sections, []string{"a", "b", "c"}) {
			t.Fatalf("sections: %v", d.Sections)
		}
		if len(g.Edges) != 0 {
			t.Fatalf("edges: %+v", g.Edges)
		}
	})

	t.Run("self use+define with external definer is duplicate", func(t *testing.T) {
		// `x = x + 1` shape: the section both defines and reads x, and a
		// second section also defines it — ambiguous, not self-missing.
		g := BuildGraph([]GraphSection{
			gsec("inc", []string{"x"}, []string{"x"}), gsec("seed", []string{"x"}, nil),
		})
		if len(g.Diagnostics) != 1 || g.Diagnostics[0].Kind != DiagDuplicateDefine {
			t.Fatalf("diagnostics: %+v", g.Diagnostics)
		}
		if len(g.Edges) != 0 {
			t.Fatalf("edges: %+v", g.Edges)
		}
	})

	t.Run("self read-before-bind sole definer is missing", func(t *testing.T) {
		g := BuildGraph([]GraphSection{gsec("inc", []string{"x"}, []string{"x"})})
		if len(g.Diagnostics) != 1 {
			t.Fatalf("diagnostics: %+v", g.Diagnostics)
		}
		d := g.Diagnostics[0]
		if d.Kind != DiagMissingProvider || d.Message != "inc reads x — no section defines it" {
			t.Fatalf("diag: %+v", d)
		}
	})

	t.Run("missing provider multi-name singular/plural", func(t *testing.T) {
		g := BuildGraph([]GraphSection{
			gsec("one", nil, []string{"x"}),
			gsec("two", nil, []string{"y", "x"}),
		})
		if len(g.Diagnostics) != 2 {
			t.Fatalf("diagnostics: %+v", g.Diagnostics)
		}
		// missing diagnostics follow section order
		if g.Diagnostics[0].Message != "one reads x — no section defines it" {
			t.Fatalf("first: %+v", g.Diagnostics[0])
		}
		if g.Diagnostics[1].Message != "two reads x, y — no section defines them" {
			t.Fatalf("second: %+v", g.Diagnostics[1])
		}
		if !reflect.DeepEqual(g.Diagnostics[1].Names, []string{"x", "y"}) {
			t.Fatalf("names not sorted: %+v", g.Diagnostics[1].Names)
		}
	})

	t.Run("parse error ignores stale defines and uses", func(t *testing.T) {
		g := BuildGraph([]GraphSection{
			gsecBad("p", []string{"x"}, []string{"y"}),
			gsec("c", nil, []string{"x"}),
		})
		if len(g.Diagnostics) != 2 {
			t.Fatalf("diagnostics: %+v", g.Diagnostics)
		}
		// parse_error group precedes missing_provider
		if g.Diagnostics[0].Kind != DiagParseError || g.Diagnostics[0].Message != "p does not parse" {
			t.Fatalf("first: %+v", g.Diagnostics[0])
		}
		if g.Diagnostics[1].Kind != DiagMissingProvider {
			t.Fatalf("second: %+v", g.Diagnostics[1])
		}
		if len(g.Edges) != 0 {
			t.Fatalf("edges: %+v", g.Edges)
		}
	})
}

func TestBuildGraphCycles(t *testing.T) {
	t.Run("two cycle exact message", func(t *testing.T) {
		// a defines x reads y; b defines y reads x.
		g := BuildGraph([]GraphSection{
			gsec("a", []string{"x"}, []string{"y"}),
			gsec("b", []string{"y"}, []string{"x"}),
		})
		var cyc *Diagnostic
		for i := range g.Diagnostics {
			if g.Diagnostics[i].Kind == DiagCycle {
				cyc = &g.Diagnostics[i]
			}
		}
		if cyc == nil {
			t.Fatalf("no cycle diagnostic: %+v", g.Diagnostics)
		}
		if cyc.Message != "a → b via x; b → a via y" {
			t.Fatalf("message: %q", cyc.Message)
		}
		if !reflect.DeepEqual(cyc.Sections, []string{"a", "b"}) {
			t.Fatalf("sections: %v", cyc.Sections)
		}
		if !reflect.DeepEqual(cyc.Names, []string{"x", "y"}) {
			t.Fatalf("names: %v", cyc.Names)
		}
		if g.Order != nil {
			t.Fatalf("order should be nil on cycle: %v", g.Order)
		}
	})

	t.Run("three cycle with chord takes shortest deterministic path", func(t *testing.T) {
		// a→b via p, b→c via q, c→a via r, plus chord a→c via s.
		// The a→c→a two-cycle is shorter than the three-cycle.
		g := BuildGraph([]GraphSection{
			gsec("a", []string{"p", "s"}, []string{"r"}),
			gsec("b", []string{"q"}, []string{"p"}),
			gsec("c", []string{"r"}, []string{"q", "s"}),
		})
		var cyc *Diagnostic
		for i := range g.Diagnostics {
			if g.Diagnostics[i].Kind == DiagCycle {
				cyc = &g.Diagnostics[i]
			}
		}
		if cyc == nil {
			t.Fatalf("no cycle diagnostic: %+v", g.Diagnostics)
		}
		if cyc.Message != "a → c via s; c → a via r" {
			t.Fatalf("message: %q", cyc.Message)
		}
		if !reflect.DeepEqual(cyc.Sections, []string{"a", "c"}) {
			t.Fatalf("sections: %v", cyc.Sections)
		}
		if g.Order != nil {
			t.Fatalf("order should be nil on cycle: %v", g.Order)
		}
	})

	t.Run("cycle label picks smallest name on a pair", func(t *testing.T) {
		// a→b via m and z; b→a via n — the a→b hop reports "m".
		g := BuildGraph([]GraphSection{
			gsec("a", []string{"m", "z"}, []string{"n"}),
			gsec("b", []string{"n"}, []string{"m", "z"}),
		})
		var cyc *Diagnostic
		for i := range g.Diagnostics {
			if g.Diagnostics[i].Kind == DiagCycle {
				cyc = &g.Diagnostics[i]
			}
		}
		if cyc == nil {
			t.Fatalf("no cycle diagnostic: %+v", g.Diagnostics)
		}
		if cyc.Message != "a → b via m; b → a via n" {
			t.Fatalf("message: %q", cyc.Message)
		}
	})
}

func TestBuildGraphEmpty(t *testing.T) {
	g := BuildGraph(nil)
	if g.Edges == nil || g.Diagnostics == nil || g.Order == nil {
		t.Fatalf("slices must be non-nil: %+v", g)
	}
	if len(g.Edges) != 0 || len(g.Diagnostics) != 0 || len(g.Order) != 0 {
		t.Fatalf("expected empty graph: %+v", g)
	}
}
