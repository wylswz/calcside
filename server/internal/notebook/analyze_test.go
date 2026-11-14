package notebook

import (
	"reflect"
	"testing"
)

func TestAnalyze(t *testing.T) {
	cases := []struct {
		name    string
		code    string
		defines []string
		uses    []string
		ok      bool
	}{
		{"assign", "x = 1\ny = x + 2", []string{"x", "y"}, nil, true},
		{"tuple", "a, b = 1, 2", []string{"a", "b"}, nil, true},
		{"augassign", "count += 1", []string{"count"}, []string{"count"}, true},
		{"def", "def f(a, b=orders):\n  return a + b + global_x\nf(1)", []string{"f"}, []string{"global_x", "orders"}, true},
		{"def local", "def f():\n  tmp = 1\n  return tmp", []string{"f"}, nil, true},
		{"lambda", "g = lambda p=orders: p * 2", []string{"g"}, []string{"orders"}, true},
		{"comp", "rows = [r for r in raw if r.ok]\nout = {k: v for k, v in pairs}", []string{"out", "rows"}, []string{"pairs", "raw"}, true},
		{"for loop", "for i in items:\n  total += i", []string{"i", "total"}, []string{"items", "total"}, true},
		{"if branches", "if flag:\n  a = 1\nelse:\n  a = 2", []string{"a"}, []string{"flag"}, true},
		{"load", `load("@m/mod.star", "foo", bar="baz")`, []string{"bar", "foo"}, nil, true},
		{"index assign", `cfg["k"] = v`, nil, []string{"cfg", "v"}, true},
		{"dot assign", `obj.field = v`, nil, []string{"obj", "v"}, true},
		{"builtins excluded", "print(len(items))\nx = str(1)", []string{"x"}, []string{"items"}, true},
		{"stdlib excluded", "j = json.encode(d)", []string{"j"}, []string{"d"}, true},
		{"caps excluded", "data = fs.read('a')", []string{"data"}, nil, true},
		{"nested def", "def outer():\n  def inner():\n    return secret_thing\n  return inner()", []string{"outer"}, []string{"secret_thing"}, true},
		{"call kwargs", "net.post(url=u, body=b)", nil, []string{"b", "u"}, true},
		{"syntax error", "def broken(:\n", nil, nil, false},
		{"while", "while running:\n  step()", nil, []string{"running", "step"}, true},
		{"shadow param", "def f(x):\n  return x\ny = f(orders)", []string{"f", "y"}, []string{"orders"}, true},
		{"cond def deferred read", "if cond:\n  x = 1\ndef f():\n  return x", []string{"f", "x"}, []string{"cond", "x"}, true},
		{"guaranteed def not a use", "x = 1\ndef f():\n  return x", []string{"f", "x"}, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defs, uses, ok := Analyze(tc.code, []string{"fs", "net"})
			if ok != tc.ok {
				t.Fatalf("ok=%v want %v", ok, tc.ok)
			}
			if !ok {
				return
			}
			if !reflect.DeepEqual(defs, tc.defines) {
				t.Errorf("defines=%v want %v", defs, tc.defines)
			}
			if len(uses) == 0 && len(tc.uses) == 0 {
				return
			}
			if !reflect.DeepEqual(uses, tc.uses) {
				t.Errorf("uses=%v want %v", uses, tc.uses)
			}
		})
	}
}
