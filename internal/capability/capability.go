// Package capability defines the capability security model: every
// capability operation flows through a per-instance Gate which checks that
// the gate is armed (an exec is in progress), runs policy/audit hooks, and
// records the outcome.
package capability

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"go.starlark.net/starlark"
)

// ErrOutOfScope is returned by any capability op invoked while its gate is
// disarmed or revoked (e.g. from a goroutine that outlived the exec).
var ErrOutOfScope = errors.New("capability used outside its scope")

// ExecContext identifies the exec an armed gate serves.
type ExecContext struct {
	ExecID     string
	InstanceID string
	UserID     string
	UserEmail  string
	Labels     map[string]string
}

// Call describes one capability operation in normalized, JSON-able form.
type Call struct {
	ExecID         string
	InstanceID     string
	UserID         string
	UserEmail      string
	InstanceLabels map[string]string
	Capability     string // "fs" | "net" | "io"
	Op             string // e.g. "read", "write", "get"
	Args           map[string]any
}

// Result is what hooks observe after an op runs. Meta must be JSON-able and
// must never carry raw file contents or response bodies.
type Result struct {
	Meta map[string]any
	Err  error
}

// DeniedError is produced when a hook denies a call.
type DeniedError struct {
	Hook   string
	Phase  string // "before" | "after"
	Reason string
}

func (e *DeniedError) Error() string {
	return fmt.Sprintf("denied by %s (%s): %s", e.Hook, e.Phase, e.Reason)
}

// Hook observes and may veto capability calls.
type Hook interface {
	Name() string
	// Before runs before the op; non-nil error denies the call.
	Before(ctx context.Context, c *Call) error
	// After runs after the op; non-nil error replaces the result.
	After(ctx context.Context, c *Call, r *Result) error
}

// Record describes the outcome of one gate invocation, delivered to the
// registered Observer (audit).
type Record struct {
	Call     Call
	Decision string // "allow" | "deny"
	Phase    string // "before" | "after" | "" (runtime error / out of scope)
	Reason   string
	Err      error
	Duration time.Duration
}

// Observer sees every gated invocation, allowed or denied.
type Observer interface {
	Observe(rec Record)
}

// Op describes the fn a capability binding calls through the gate.
// It returns the starlark value handed back to the script plus JSON-able
// metadata for hooks/audit.
type Op func(ctx context.Context, args map[string]any) (starlark.Value, map[string]any, error)

// GateOwner identifies the instance a gate belongs to, so records emitted
// while disarmed still carry instance/user attribution.
type GateOwner struct {
	InstanceID string
	UserID     string
	UserEmail  string
	Labels     map[string]string
}

// Gate is a per-instance arming switch. Capability bindings live for the
// instance lifetime; Invoke only lets calls through while armed.
type Gate struct {
	owner   GateOwner
	mu      sync.RWMutex
	armed   bool
	revoked bool
	exec    ExecContext
	hooks   []Hook
	obs     Observer
}

// NewGate builds a gate with the given hooks (run in order) and observer.
func NewGate(owner GateOwner, hooks []Hook, obs Observer) *Gate {
	return &Gate{owner: owner, hooks: hooks, obs: obs}
}

// Arm marks the gate as serving the given exec. No-op once revoked.
func (g *Gate) Arm(ec ExecContext) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.revoked {
		return
	}
	g.armed = true
	g.exec = ec
}

// Disarm ends the current exec's access.
func (g *Gate) Disarm() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.armed = false
	g.exec = ExecContext{}
}

// Revoke permanently disables the gate (instance deleted/expired).
func (g *Gate) Revoke() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.revoked = true
	g.armed = false
	g.exec = ExecContext{}
}

// Armed reports whether calls will pass right now.
func (g *Gate) Armed() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.armed && !g.revoked
}

// Invoke runs one capability operation through the gate: armed check,
// Before hooks (first error denies), the op itself, After hooks, and
// observer recording.
func (g *Gate) Invoke(ctx context.Context, capability, op string, args map[string]any, fn Op) (starlark.Value, error) {
	g.mu.RLock()
	if !g.armed || g.revoked {
		g.mu.RUnlock()
		if g.obs != nil {
			g.obs.Observe(Record{
				Call: Call{
					InstanceID:     g.owner.InstanceID,
					UserID:         g.owner.UserID,
					UserEmail:      g.owner.UserEmail,
					InstanceLabels: g.owner.Labels,
					Capability:     capability, Op: op, Args: args,
				},
				Decision: "deny",
				Reason:   ErrOutOfScope.Error(),
				Err:      ErrOutOfScope,
			})
		}
		return nil, ErrOutOfScope
	}
	exec := g.exec
	g.mu.RUnlock()

	call := Call{
		ExecID:         exec.ExecID,
		InstanceID:     exec.InstanceID,
		UserID:         exec.UserID,
		UserEmail:      exec.UserEmail,
		InstanceLabels: exec.Labels,
		Capability:     capability,
		Op:             op,
		Args:           args,
	}
	rec := Record{Call: call, Decision: "allow"}
	start := time.Now()
	defer func() {
		rec.Duration = time.Since(start)
		if g.obs != nil {
			g.obs.Observe(rec)
		}
	}()

	for _, h := range g.hooks {
		if err := h.Before(ctx, &call); err != nil {
			rec.Decision = "deny"
			rec.Phase = "before"
			rec.Reason = err.Error()
			rec.Err = err
			return nil, &DeniedError{Hook: h.Name(), Phase: "before", Reason: err.Error()}
		}
	}

	val, meta, opErr := fn(ctx, args)
	res := &Result{Meta: meta, Err: opErr}

	for _, h := range g.hooks {
		if err := h.After(ctx, &call, res); err != nil {
			rec.Decision = "deny"
			rec.Phase = "after"
			rec.Reason = err.Error()
			rec.Err = err
			return nil, &DeniedError{Hook: h.Name(), Phase: "after", Reason: err.Error()}
		}
	}

	rec.Err = opErr
	if opErr != nil {
		rec.Reason = opErr.Error()
	}
	return val, opErr
}

// OpInfo documents one capability operation.
type OpInfo struct {
	Name   string   `json:"name"`
	Doc    string   `json:"doc"`
	Params []string `json:"params,omitempty"`
}

// FieldDoc documents a capability config field.
type FieldDoc struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Doc     string `json:"doc"`
	Default any    `json:"default,omitempty"`
}

// Factory builds a starlark binding for one capability.
type Factory interface {
	Name() string
	Ops() []OpInfo
	ConfigFields() []FieldDoc
	// Validate parses and validates raw JSON config, applying defaults and
	// clamping against server limits. Returns the typed config for New.
	Validate(cfg json.RawMessage, limits ServerLimits) (any, error)
	// New builds the starlark value bound to the given gate. The returned
	// io.Closer (may be nil) is closed when the instance ends.
	New(cfg any, gate *Gate) (starlark.Value, io.Closer, error)
}

// ServerLimits bounds what instance specs may request.
type ServerLimits struct {
	MaxFSQuotaBytes     int64
	MaxTTL              time.Duration
	DefaultTTL          time.Duration
	MaxExecTimeout      time.Duration
	MaxSteps            uint64
	MaxOutputBytes      int64
	NetAllowPrivate     bool  // allow private/reserved IPs in net allowlists
	MaxNetResponseBytes int64 // clamp for net.max_response_bytes
}

// Registry holds capability factories by name.
type Registry struct {
	factories map[string]Factory
	order     []string
}

func NewRegistry() *Registry {
	return &Registry{factories: map[string]Factory{}}
}

func (r *Registry) Register(f Factory) {
	name := f.Name()
	if _, ok := r.factories[name]; !ok {
		r.order = append(r.order, name)
	}
	r.factories[name] = f
}

func (r *Registry) Get(name string) (Factory, bool) {
	f, ok := r.factories[name]
	return f, ok
}

func (r *Registry) Names() []string {
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}
