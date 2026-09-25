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
	OpPrint   types.Op = "print" // used by the print builtin, not a method
)

func (factory) Name() types.CapabilityName { return types.CapIO }

func (factory) Ops() []capability.OpInfo {
	return []capability.OpInfo{
		{Name: OpPrintln, Doc: "write args to output buffer, space separated, newline terminated"},
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

func (factory) New(cfgAny any, env capability.InstanceEnv) (starlark.Value, io.Closer, error) {
	cfg, ok := cfgAny.(Config)
	if !ok {
		return nil, nil, fmt.Errorf("io: bad config type %T", cfgAny)
	}
	buf := NewBuffer(cfg.MaxOutputBytes)
	return bindModule(buf, env.Gate), &Closer{B: buf}, nil
}

// Closer exposes the output Buffer to the engine.
type Closer struct{ B *Buffer }

func (c *Closer) Close() error { return nil }

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
func BindBuffer(buf *Buffer, gate *capability.Gate) starlark.Value {
	return bindModule(buf, gate)
}

func bindModule(buf *Buffer, gate *capability.Gate) starlark.Value {
	return capability.Bind(types.CapIO, gate, map[types.Op]capability.Method{
		OpPrintln: func(args starlark.Tuple, kwargs []starlark.Tuple) (map[string]any, capability.OpBody, error) {
			s := joinArgs(args) + "\n"
			return map[string]any{"bytes": len(s)}, func(ctx context.Context) (starlark.Value, map[string]any, error) {
				n := buf.Write(s)
				return starlark.None, map[string]any{"bytes": n}, nil
			}, nil
		},
	})
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
