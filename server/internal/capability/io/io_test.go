package io

import (
	"fmt"
	"strings"
	"testing"

	"go.starlark.net/starlark"

	"calcside/internal/capability"
	"calcside/internal/types"
)

func TestPrompt(t *testing.T) {
	cfg, err := factory{}.Validate(nil, capability.ServerLimits{})
	if err != nil {
		t.Fatal(err)
	}
	p := factory{}.Prompt(cfg)
	for _, want := range []string{"`io`", "io.println", fmt.Sprintf("%d bytes", defaultMaxOutput), "`print(...)`"} {
		if !strings.Contains(p, want) {
			t.Fatalf("missing %q in:\n%s", want, p)
		}
	}
	help := factory{}.Help(cfg)
	if len(help) < 2 || help[0].Name != "io" || !strings.Contains(help[0].Text, "io.println()") {
		t.Fatalf("help missing capability docs: %+v", help)
	}
	if !strings.Contains(help[0].Text, "io.display(") {
		t.Fatalf("help missing io.display: %q", help[0].Text)
	}
}

func displayFixture(t *testing.T) (*starlark.StringDict, *Displays, *capability.Gate) {
	t.Helper()
	gate := capability.NewGate(capability.GateOwner{}, nil, nil)
	cfg, err := factory{}.Validate(nil, capability.ServerLimits{})
	if err != nil {
		t.Fatal(err)
	}
	mod, closer, err := factory{}.New(cfg, capability.InstanceEnv{Gate: gate})
	if err != nil {
		t.Fatal(err)
	}
	disp, ok := closer.(*Closer)
	if !ok || disp.D == nil {
		t.Fatalf("closer = %#v", closer)
	}
	gate.Arm(capability.ExecContext{ExecID: "exe_t", InstanceID: "ins_t"})
	t.Cleanup(gate.Disarm)
	return &starlark.StringDict{"io": mod}, disp.D, gate
}

func TestDisplay(t *testing.T) {
	pre, disp, _ := displayFixture(t)
	code := `io.display({
		"$schema": "https://vega.github.io/schema/vega-lite/v6.json",
		"data": {"values": [{"day": "2026-01-01", "v": 1}]},
		"mark": "line",
		"encoding": {"x": {"field": "day", "type": "temporal"}, "y": {"field": "v", "type": "quantitative"}},
		"title": {"text": "Latency"},
	})`
	if _, err := starlark.Eval(&starlark.Thread{}, "test", code, *pre); err != nil {
		t.Fatal(err)
	}
	items := disp.Take()
	if len(items) != 1 {
		t.Fatalf("displays = %d", len(items))
	}
	if items[0].Title != "Latency" {
		t.Fatalf("title = %q", items[0].Title)
	}
	if !strings.Contains(string(items[0].Spec), `"mark":"line"`) {
		t.Fatalf("spec = %s", items[0].Spec)
	}
	if disp.Take() != nil {
		t.Fatal("Take did not drain")
	}
}

func TestDisplayTitleAndArgs(t *testing.T) {
	pre, disp, _ := displayFixture(t)
	// Explicit title wins; spec title.text is the fallback.
	for _, code := range []string{
		`io.display({"mark": "point", "data": {"values": []}, "title": "FromSpec"})`,
		`io.display({"mark": "point", "data": {"values": []}, "title": {"text": "ObjTitle"}})`,
		`io.display({"mark": "point", "data": {"values": []}}, title="Explicit")`,
	} {
		if _, err := starlark.Eval(&starlark.Thread{}, "test", code, *pre); err != nil {
			t.Fatalf("%s: %v", code, err)
		}
	}
	items := disp.Take()
	if len(items) != 3 {
		t.Fatalf("displays = %d", len(items))
	}
	if items[0].Title != "FromSpec" || items[1].Title != "ObjTitle" || items[2].Title != "Explicit" {
		t.Fatalf("titles = %q %q %q", items[0].Title, items[1].Title, items[2].Title)
	}
}

func TestDisplayRejects(t *testing.T) {
	pre, disp, _ := displayFixture(t)
	for _, tc := range []struct{ code, want string }{
		{`io.display("x")`, "spec must be a dict"},
		{`io.display([1])`, "spec must be a dict"},
		{`io.display({"mark": "image", "data": {"values": []}})`, `mark type "image" is not allowed`},
		{`io.display({"mark": "point", "data": {"url": "http://x"}})`, `data must be an object`},
		{`io.display({"mark": "point", "data": {"values": []}}, title=1)`, "title must be a string"},
		{`io.display({"mark": "point", "data": {"values": []}}, bogus=1)`, "unexpected keyword"},
	} {
		_, err := starlark.Eval(&starlark.Thread{}, "test", tc.code, *pre)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: err = %v, want %q", tc.code, err, tc.want)
		}
	}
	if got := disp.Take(); len(got) != 0 {
		t.Fatalf("rejected specs stored: %d", len(got))
	}
}

func TestDisplayCaps(t *testing.T) {
	pre, disp, _ := displayFixture(t)
	var err error
	for i := 0; i < 9; i++ {
		_, err = starlark.Eval(&starlark.Thread{}, "test",
			`io.display({"mark": "point", "data": {"values": []}, "title": "t"})`, *pre)
	}
	if err == nil || !strings.Contains(err.Error(), "at most 8 displays") {
		t.Fatalf("err = %v", err)
	}
	if got := disp.Take(); len(got) != 8 {
		t.Fatalf("displays = %d", len(got))
	}
}

func TestDisplayOutOfScope(t *testing.T) {
	pre, disp, gate := displayFixture(t)
	gate.Disarm()
	_, err := starlark.Eval(&starlark.Thread{}, "test",
		`io.display({"mark": "point", "data": {"values": []}})`, *pre)
	if err == nil {
		t.Fatal("expected out-of-scope error")
	}
	if got := disp.Take(); len(got) != 0 {
		t.Fatalf("display stored while disarmed: %d", len(got))
	}
}

func TestOpsIncludesDisplay(t *testing.T) {
	var found bool
	for _, op := range (factory{}).Ops() {
		if op.Name == OpDisplay {
			found = true
			if op.PolicyArgs["mark"] != "string" || op.ResultMeta["bytes"] != "number" {
				t.Fatalf("op info = %+v", op)
			}
		}
	}
	if !found {
		t.Fatal("Ops() missing display")
	}
	if OpDisplay != types.Op("display") {
		t.Fatalf("OpDisplay = %q", OpDisplay)
	}
}
