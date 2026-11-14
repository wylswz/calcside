package ext

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"calcside/internal/capability"
)

func parse(t *testing.T, manifest string) *CapabilityManifest {
	t.Helper()
	m, err := ParseManifest([]byte(manifest))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	return m
}

func TestOpSpecPlainParamsUnchanged(t *testing.T) {
	m := parse(t, `name: t
version: 0.1.0
ops:
  - {name: a, params: [x, y]}
  - {name: b}
  - {name: c, params: []}
  - name: d
    params:
`)
	if len(m.Ops) != 4 {
		t.Fatalf("ops: %d", len(m.Ops))
	}
	if got := m.Ops[0].Params; strings.Join(got, ",") != "x,y" {
		t.Fatalf("params: %v", got)
	}
	if m.Ops[0].ParamDocs != nil || m.Ops[1].ParamDocs != nil ||
		m.Ops[2].ParamDocs != nil || m.Ops[3].ParamDocs != nil {
		t.Fatal("plain params must not populate ParamDocs")
	}
	if len(m.Ops[1].Params) != 0 || len(m.Ops[2].Params) != 0 || len(m.Ops[3].Params) != 0 {
		t.Fatal("empty params expected")
	}
}

func TestOpSpecDocumentedParams(t *testing.T) {
	m := parse(t, `name: t
version: 0.1.0
ops:
  - name: line
    doc: Line chart
    returns: "dict — spec"
    example: |
      spec = ext.t.line(data, "x", "y")
      io.display(spec)
    params:
      - data
      - name: options
        type: dict
        doc: Optional chart options.
        optional: true
        fields:
          - {name: color, type: string, doc: Colour field., default: null}
          - name: zoom
            type: bool
            doc: Drag to pan.
            default: false
          - name: bins
            type: int
            default: 30
`)
	op := m.Ops[0]
	if strings.Join(op.Params, ",") != "data,options" {
		t.Fatalf("params: %v", op.Params)
	}
	if len(op.ParamDocs) != 2 {
		t.Fatalf("param docs: %d", len(op.ParamDocs))
	}
	if op.ParamDocs[0].Name != "data" || op.ParamDocs[0].Doc != "" {
		t.Fatalf("plain item must contribute a name-only doc: %+v", op.ParamDocs[0])
	}
	od := op.ParamDocs[1]
	if od.Name != "options" || od.Type != "dict" || !od.Optional {
		t.Fatalf("options doc: %+v", od)
	}
	if len(od.Fields) != 3 {
		t.Fatalf("fields: %d", len(od.Fields))
	}
	color := od.Fields[0]
	if !color.HasDefault || color.Default != nil {
		t.Fatalf("explicit null default must set HasDefault with nil value: %+v", color)
	}
	raw, err := color.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"default":null`) {
		t.Fatalf("explicit null default must serialize: %s", raw)
	}
	if od.Fields[2].Default != 30 {
		t.Fatalf("bins default: %#v", od.Fields[2].Default)
	}
	if op.Returns != "dict — spec" {
		t.Fatalf("returns: %q", op.Returns)
	}
	if !strings.Contains(op.Example, "io.display(spec)") {
		t.Fatalf("example: %q", op.Example)
	}
}

func TestOpSpecManifestErrors(t *testing.T) {
	cases := map[string]string{
		"unknown op key": `name: t
version: 0.1.0
ops: [{name: a, bogus: 1}]
`,
		"params not a list": `name: t
version: 0.1.0
ops: [{name: a, params: x}]
`,
		"params bad item": `name: t
version: 0.1.0
ops: [{name: a, params: [x, 5]}]
`,
		"param doc unknown key": `name: t
version: 0.1.0
ops:
  - {name: a, params: [{name: x, bogus: 1}]}
`,
		"param doc no name": `name: t
version: 0.1.0
ops:
  - {name: a, params: [{type: string}]}
`,
		"nested fields": `name: t
version: 0.1.0
ops:
  - {name: a, params: [{name: x, fields: [{name: y, fields: [{name: z}]}]}]}
`,
		"fields not a list": `name: t
version: 0.1.0
ops:
  - {name: a, params: [{name: x, fields: y}]}
`,
		"bad field name": `name: t
version: 0.1.0
ops:
  - {name: a, params: [{name: x, fields: [{name: "1bad"}]}]}
`,
		"dup field": `name: t
version: 0.1.0
ops:
  - {name: a, params: [{name: x, fields: [{name: y}, {name: y}]}]}
`,
		"dup param doc": `name: t
version: 0.1.0
ops:
  - {name: a, params: [{name: x}, x, {name: x}]}
`,
		"type too long": `name: t
version: 0.1.0
ops:
  - {name: a, params: [{name: x, type: "` + strings.Repeat("t", 65) + `"}]}
`,
		"doc too long": `name: t
version: 0.1.0
ops:
  - {name: a, params: [{name: x, doc: "` + strings.Repeat("d", 1025) + `"}]}
`,
		"returns too long": `name: t
version: 0.1.0
ops:
  - {name: a, returns: "` + strings.Repeat("r", 513) + `"}
`,
		"example too long": `name: t
version: 0.1.0
ops:
  - {name: a, example: "` + strings.Repeat("e", 4097) + `"}
`,
		"too many fields": `name: t
version: 0.1.0
ops:
  - {name: a, params: [{name: x, fields: [` + strings.Repeat("{name: f},", 33) + `{name: f}]}]}
`,
		"non-scalar default": `name: t
version: 0.1.0
ops:
  - {name: a, params: [{name: x, default: {k: v}}]}
`,
		"seq default": `name: t
version: 0.1.0
ops:
  - {name: a, params: [{name: x, default: [1, 2]}]}
`,
	}
	for name, manifest := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseManifest([]byte(manifest)); err == nil {
				t.Fatal("expected manifest error")
			}
		})
	}
}

func TestOpSpecScalarDefaults(t *testing.T) {
	m := parse(t, `name: t
version: 0.1.0
ops:
  - {name: a, params: [{name: s, default: "hi"}, {name: i, default: 3},
     {name: f, default: 1.5}, {name: b, default: true}, {name: n, default: null}]}
`)
	pd := m.Ops[0].ParamDocs
	if pd[0].Default != "hi" || pd[1].Default != 3 || pd[2].Default != 1.5 || pd[3].Default != true {
		t.Fatalf("defaults: %#v", pd)
	}
	if !pd[4].HasDefault || pd[4].Default != nil {
		t.Fatalf("null default: %+v", pd[4])
	}
}

// TestContribManifestsUnchanged loads every contrib/*/*/capability.yaml
// and requires params to stay exactly the declared names in order:
// plain-string manifests keep ParamDocs nil; documented manifests keep
// Params and ParamDocs names in lockstep.
func TestContribManifestsUnchanged(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..", "contrib")
	families, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	loaded := 0
	for _, fam := range families {
		if !fam.IsDir() {
			continue
		}
		versions, err := os.ReadDir(filepath.Join(root, fam.Name()))
		if err != nil {
			continue
		}
		for _, ver := range versions {
			if !ver.IsDir() {
				continue
			}
			data, err := os.ReadFile(filepath.Join(root, fam.Name(), ver.Name(), "capability.yaml"))
			if err != nil {
				continue
			}
			m, err := ParseManifest(data)
			if err != nil {
				t.Fatalf("%s/%s: %v", fam.Name(), ver.Name(), err)
			}
			loaded++
			for _, op := range m.Ops {
				if op.ParamDocs != nil {
					if len(op.ParamDocs) != len(op.Params) {
						t.Fatalf("%s/%s op %s: param docs %d != params %d",
							fam.Name(), ver.Name(), op.Name, len(op.ParamDocs), len(op.Params))
					}
					for i, p := range op.ParamDocs {
						if p.Name != op.Params[i] {
							t.Fatalf("%s/%s op %s: param doc %d %q != param %q",
								fam.Name(), ver.Name(), op.Name, i, p.Name, op.Params[i])
						}
					}
				}
				for _, p := range op.Params {
					if !identRe.MatchString(p) {
						t.Fatalf("%s/%s op %s: invalid param %q", fam.Name(), ver.Name(), op.Name, p)
					}
				}
			}
		}
	}
	if loaded == 0 {
		t.Fatal("no contrib manifests found")
	}
}

func TestOpDetailRendersParamDocs(t *testing.T) {
	op := capability.OpInfo{
		Name:   "line",
		Doc:    "Line chart spec.",
		Params: []string{"data", "x", "y", "options"},
		ParamDocs: []capability.ParamDoc{
			{Name: "data", Type: "list[dict]", Doc: "Rows of flat dicts."},
			{Name: "x", Type: "string", Doc: "Field name."},
			{Name: "y", Type: "string", Doc: "Field name."},
			{Name: "options", Type: "dict", Doc: "Chart options.", Optional: true,
				Fields: []capability.ParamDoc{
					{Name: "color", Type: "string", Doc: "Colour field.", HasDefault: true},
					{Name: "tooltip", Type: "bool", Default: true},
				}},
		},
		Returns: "dict — spec",
		Example: "spec = ext.t.line(data, \"x\", \"y\")\nio.display(spec)",
	}
	text := capability.OpDetail("ext.t", op)
	for _, want := range []string{
		"- `ext.t.line(data, x, y, options)` — Line chart spec.",
		"  Parameters:",
		"  - `data` (list[dict]): Rows of flat dicts.",
		"  - `options` (dict, optional): Chart options.",
		"    - `color` (string, default None): Colour field.",
		"    - `tooltip` (bool, default True)\n",
		"  Returns: dict — spec",
		"  Example:",
		"    spec = ext.t.line(data, \"x\", \"y\")",
		"    io.display(spec)",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("OpDetail missing %q:\n%s", want, text)
		}
	}
	// No docs → identical to the old OpHelpEntry text.
	plain := capability.OpInfo{Name: "read", Doc: "Read a file.", Params: []string{"path"}}
	entry := capability.OpHelpEntry("fs", plain)
	if entry.Text != "- `fs.read(path)` — Read a file.\n" {
		t.Fatalf("plain help text changed: %q", entry.Text)
	}
}
