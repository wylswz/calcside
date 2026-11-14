// Package io implements the "io" capability: print / io.println writing to
// a per-exec bounded output buffer.
package io

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"go.starlark.net/starlark"

	"calcside/internal/capability"
	"calcside/internal/chart"
	"calcside/internal/types"
)

const truncMarker = "\n...[output truncated]"

// Config configures the io capability.
type Config struct {
	MaxOutputBytes int64 `json:"max_output_bytes"`
}

const defaultMaxOutput = 1 << 20

// Buffer is a bounded output sink shared by print and io.println.
type Buffer struct {
	mu        sync.Mutex
	buf       strings.Builder
	max       int64
	truncated bool
}

func NewBuffer(max int64) *Buffer {
	if max <= 0 {
		max = defaultMaxOutput
	}
	return &Buffer{max: max}
}

// Write appends to the buffer, truncating at max with a marker. Returns the
// number of bytes accepted (for meta).
func (b *Buffer) Write(s string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.truncated {
		return 0
	}
	rem := b.max - int64(b.buf.Len())
	if int64(len(s)) <= rem {
		b.buf.WriteString(s)
		return len(s)
	}
	if rem > 0 {
		b.buf.WriteString(s[:rem])
	}
	b.buf.WriteString(truncMarker)
	b.truncated = true
	return int(rem)
}

func (b *Buffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Reset clears the buffer (called before each exec; the binding lives for
// the instance lifetime but output is per-exec).
func (b *Buffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Reset()
	b.truncated = false
}

func (b *Buffer) Truncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.truncated
}

// Factory returns the capability.Factory for "io".
func Factory() capability.Factory { return factory{} }

type factory struct{}

// Op consts for io.
const (
	OpPrintln types.Op = "println"
	OpPrint   types.Op = "print"   // used by the print builtin, not a method
	OpDisplay types.Op = "display" // attach a validated chart spec to the exec
)

func (factory) Name() types.CapabilityName { return types.CapIO }

func (factory) Ops() []capability.OpInfo {
	return []capability.OpInfo{
		{Name: OpPrintln, PolicyArgs: map[string]string{"bytes": "number"}, ResultMeta: map[string]string{"bytes": "number"}, Doc: "write args to output buffer, space separated, newline terminated"},
		{Name: OpDisplay, PolicyArgs: map[string]string{"bytes": "number", "rows": "number", "mark": "string"}, ResultMeta: map[string]string{"bytes": "number"}, Doc: "attach a validated Vega-Lite spec dict to the exec result (max 8 per exec)"},
	}
}

func (factory) ConfigFields() []capability.FieldDoc {
	return []capability.FieldDoc{
		{Name: "max_output_bytes", Type: types.FieldInt, Doc: "output buffer cap", Default: defaultMaxOutput},
	}
}

func (factory) Validate(raw json.RawMessage, limits capability.ServerLimits) (any, error) {
	cfg := Config{MaxOutputBytes: defaultMaxOutput}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("io config: %w", err)
		}
	}
	if cfg.MaxOutputBytes <= 0 {
		cfg.MaxOutputBytes = defaultMaxOutput
	}
	if limits.MaxOutputBytes > 0 && cfg.MaxOutputBytes > limits.MaxOutputBytes {
		cfg.MaxOutputBytes = limits.MaxOutputBytes
	}
	return cfg, nil
}

func (f factory) Prompt(cfgAny any) string {
	cfg, _ := cfgAny.(Config)
	var b strings.Builder
	b.WriteString("#### `io`\n")
	b.WriteString("- `print(...)` and `io.println(...)` write to the exec output.\n")
	b.WriteString("- `io.display(spec, title=None)` attaches a validated Vega-Lite chart (see `chart`) to the exec result.\n")
	fmt.Fprintf(&b, "- Output is capped at %d bytes per exec.\n", cfg.MaxOutputBytes)
	return b.String()
}

func (f factory) Help(cfgAny any) []capability.HelpEntry {
	cfg, _ := cfgAny.(Config)
	var b strings.Builder
	b.WriteString("#### `io`\n")
	for _, op := range f.Ops() {
		b.WriteString(capability.OpLine(types.CapIO, op) + "\n")
	}
	b.WriteString("- `print(...)` writes to the same output buffer.\n")
	b.WriteString("- `io.display` specs are validated Vega-Lite; build them with `chart.*` or the charts extension.\n")
	fmt.Fprintf(&b, "- Output is capped at %d bytes per exec.\n", cfg.MaxOutputBytes)
	out := []capability.HelpEntry{{
		Name:    string(types.CapIO),
		Summary: "exec output and print",
		Text:    b.String(),
	}}
	for _, op := range f.Ops() {
		out = append(out, capability.OpHelpEntry(types.CapIO, op))
	}
	return out
}

func (factory) New(cfgAny any, env capability.InstanceEnv) (starlark.Value, io.Closer, error) {
	cfg, ok := cfgAny.(Config)
	if !ok {
		return nil, nil, fmt.Errorf("io: bad config type %T", cfgAny)
	}
	buf := NewBuffer(cfg.MaxOutputBytes)
	disp := NewDisplays()
	return bindModule(buf, disp, env.Gate), &Closer{B: buf, D: disp}, nil
}

// Closer exposes the output Buffer and display buffer to the engine.
type Closer struct {
	B *Buffer
	D *Displays
}

func (c *Closer) Close() error { return nil }

// Display is one validated chart spec attached to an exec.
type Display struct {
	Title string
	Spec  json.RawMessage
}

// Displays is the bounded per-exec chart buffer behind io.display.
// Methods are nil-safe so sessions built without the io capability
// (or hand-built test sessions) work unchanged.
type Displays struct {
	mu    sync.Mutex
	items []Display
	bytes int
}

func NewDisplays() *Displays { return &Displays{} }

func (d *Displays) Reset() {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.items = nil
	d.bytes = 0
}

// Take drains the buffer, returning the displays in call order.
func (d *Displays) Take() []Display {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	out := d.items
	d.items = nil
	d.bytes = 0
	return out
}

func (d *Displays) Append(title string, spec json.RawMessage) error {
	if d == nil {
		return fmt.Errorf("io.display: no display buffer")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.items) >= chart.MaxDisplays {
		return fmt.Errorf("io.display: at most %d displays per exec", chart.MaxDisplays)
	}
	if d.bytes+len(spec) > chart.MaxTotalBytes {
		return fmt.Errorf("io.display: displays exceed %d bytes per exec", chart.MaxTotalBytes)
	}
	d.items = append(d.items, Display{Title: title, Spec: spec})
	d.bytes += len(spec)
	return nil
}

// PrintBuiltin returns a starlark builtin suitable as the predeclared
// "print", writing through the same gate/buffer.
func PrintBuiltin(buf *Buffer, gate *capability.Gate) *starlark.Builtin {
	return starlark.NewBuiltin("print", func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		s := joinArgs(args) + "\n"
		return gate.Invoke(capability.ThreadContext(thread), types.CapIO, OpPrint, map[string]any{"bytes": len(s)},
			func(ctx context.Context, _ map[string]any) (starlark.Value, map[string]any, error) {
				n := buf.Write(s)
				return starlark.None, map[string]any{"bytes": n}, nil
			})
	})
}

// BindBuffer exposes the io module bound to an existing buffer (used by
// the engine and tests wiring a shared output sink).
func BindBuffer(buf *Buffer, disp *Displays, gate *capability.Gate) starlark.Value {
	return bindModule(buf, disp, gate)
}

func bindModule(buf *Buffer, disp *Displays, gate *capability.Gate) starlark.Value {
	return capability.Bind(types.CapIO, gate, map[types.Op]capability.Method{
		OpPrintln: func(args starlark.Tuple, kwargs []starlark.Tuple) (map[string]any, capability.OpBody, error) {
			s := joinArgs(args) + "\n"
			return map[string]any{"bytes": len(s)}, func(ctx context.Context) (starlark.Value, map[string]any, error) {
				n := buf.Write(s)
				return starlark.None, map[string]any{"bytes": n}, nil
			}, nil
		},
		OpDisplay: displayMethod(disp),
	})
}

// displayMethod parses io.display args, validates the spec against the
// chart allowlist, and returns the gated op that appends it. Arg
// parsing and validation happen before the gate so malformed calls
// never reach policy/audit; the gate sees only {bytes, rows, mark}.
func displayMethod(disp *Displays) capability.Method {
	return func(args starlark.Tuple, kwargs []starlark.Tuple) (map[string]any, capability.OpBody, error) {
		var spec, title starlark.Value
		if err := starlark.UnpackArgs("io.display", args, kwargs, "spec", &spec, "title?", &title); err != nil {
			return nil, nil, err
		}
		if _, ok := spec.(*starlark.Dict); !ok {
			return nil, nil, fmt.Errorf("io.display: spec must be a dict")
		}
		titleStr := ""
		if title != nil && title != starlark.None {
			s, ok := title.(starlark.String)
			if !ok {
				return nil, nil, fmt.Errorf("io.display: title must be a string")
			}
			titleStr = string(s)
		}
		gv, err := capability.JSONValue(spec)
		if err != nil {
			return nil, nil, fmt.Errorf("io.display: %w", err)
		}
		raw, err := json.Marshal(gv)
		if err != nil {
			return nil, nil, fmt.Errorf("io.display: spec: %w", err)
		}
		canon, err := chart.Validate(raw)
		if err != nil {
			return nil, nil, fmt.Errorf("io.display: %w", err)
		}
		if titleStr == "" {
			titleStr = specTitle(canon)
		}
		mark, rows := chart.KindRows(canon)
		callArgs := map[string]any{"bytes": len(canon), "rows": rows, "mark": mark}
		return callArgs, func(ctx context.Context) (starlark.Value, map[string]any, error) {
			if err := disp.Append(titleStr, canon); err != nil {
				return nil, nil, err
			}
			return starlark.None, map[string]any{"bytes": len(canon)}, nil
		}, nil
	}
}

// specTitle extracts a display title from a validated spec: a string
// title, or title.text when the title is an object.
func specTitle(spec []byte) string {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(spec, &m); err != nil {
		return ""
	}
	raw, ok := m["title"]
	if !ok {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var obj struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		return obj.Text
	}
	return ""
}

func joinArgs(args starlark.Tuple) string {
	var sb strings.Builder
	for i, v := range args {
		if i > 0 {
			sb.WriteByte(' ')
		}
		if s, ok := v.(starlark.String); ok {
			sb.WriteString(string(s))
		} else {
			sb.WriteString(v.String())
		}
	}
	return sb.String()
}
