package instance

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestExecDisplays(t *testing.T) {
	n := newNode(t, Options{}, nil)
	id := n.mustCreate(`{}`)
	code := `
spec = {
	"$schema": "https://vega.github.io/schema/vega-lite/v6.json",
	"data": {"values": [{"day": "2026-01-01", "v": 1}, {"day": "2026-01-02", "v": 5}]},
	"mark": "line",
	"encoding": {"x": {"field": "day", "type": "temporal"}, "y": {"field": "v", "type": "quantitative"}},
	"title": "Latency",
}
io.display(spec)
io.display(chart.spec("point", data=[{"a": 1}], title="Second"))
`
	res := n.mustExec(id, code)
	if len(res.Displays) != 2 {
		t.Fatalf("displays = %d", len(res.Displays))
	}
	d := res.Displays[0]
	if d.Kind != "vega-lite" || d.Title != "Latency" || d.Redacted {
		t.Fatalf("display = %+v", d)
	}
	if !strings.Contains(string(d.Spec), `"mark":"line"`) {
		t.Fatalf("spec = %s", d.Spec)
	}
	if res.Displays[1].Title != "Second" {
		t.Fatalf("second title = %q", res.Displays[1].Title)
	}

	// Displays are per-exec: the next exec sees none.
	res = n.mustExec(id, `1 + 1`)
	if len(res.Displays) != 0 {
		t.Fatalf("stale displays = %d", len(res.Displays))
	}
}

func TestExecDisplaysSurviveFailure(t *testing.T) {
	n := newNode(t, Options{}, nil)
	id := n.mustCreate(`{}`)
	res, err := n.exec(id, `
io.display({"mark": "point", "data": {"values": []}, "title": "kept"})
fail("boom")
`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Error == nil {
		t.Fatal("expected exec error")
	}
	if len(res.Displays) != 1 || res.Displays[0].Title != "kept" {
		t.Fatalf("displays on failed exec = %+v", res.Displays)
	}
}

func TestExecDisplaysSecretRedaction(t *testing.T) {
	n := newNode(t, Options{}, nil)
	// A secret containing characters that JSON escaping would hide from
	// a naive string replace.
	secret := `va"l\ue`
	id := n.mustCreate(fmt.Sprintf(`{"secrets":{"S":{"value":%q}}}`, secret))
	// %q quoting keeps the quote/backslash secret valid inside Starlark
	// string literals; the decoded values are the raw secret.
	code := fmt.Sprintf(`
spec = {
	"$schema": "https://vega.github.io/schema/vega-lite/v6.json",
	"data": {"values": [{"k": %q, "v": 1}]},
	"mark": "point",
	"encoding": {"x": {"field": %q, "type": "nominal"}},
	"title": %q,
}
io.display(spec, title=%q)
`, secret, secret, secret, "t-"+secret)
	res := n.mustExec(id, code)
	if len(res.Displays) != 1 {
		t.Fatalf("displays = %d", len(res.Displays))
	}
	d := res.Displays[0]
	if !d.Redacted {
		t.Fatal("expected Redacted")
	}
	if strings.Contains(string(d.Spec), secret) {
		t.Fatalf("spec leaked secret: %s", d.Spec)
	}
	if strings.Contains(d.Title, secret) {
		t.Fatalf("title leaked secret: %q", d.Title)
	}
	if !strings.Contains(string(d.Spec), "[REDACTED:S]") {
		t.Fatalf("spec not redacted: %s", d.Spec)
	}
	if d.Title != "t-[REDACTED:S]" {
		t.Fatalf("title = %q", d.Title)
	}
	// The redacted spec is still valid JSON.
	if !json.Valid(d.Spec) {
		t.Fatalf("redacted spec is not JSON: %s", d.Spec)
	}
}
