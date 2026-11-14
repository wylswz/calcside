// Package interrupt implements the "interrupt" capability: the
// application-program ops that reserve interrupts (run_id/new/wait) and
// end a step on a waiting outcome. The capability is never registered
// in a Registry -- the engine grants it to program sessions only, and
// the Factory exists only for documentation and to refuse any spec.
package interrupt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"calcside/internal/capability"
	"calcside/internal/secrets"
	"calcside/internal/serving"
	"calcside/internal/types"
)

// Name is the capability and predeclared-module name.
const Name types.CapabilityName = "interrupt"

// ErrWait is the sentinel a successful interrupt.wait returns to unwind
// the Starlark call stack; the engine classifies it as the waiting
// outcome, not a failure. Nothing after wait runs.
var ErrWait = errors.New("interrupt wait")

// UsageError marks fixed-string misuse errors the caller should see
// verbatim; op bodies return it only for messages that never embed
// user values.
type UsageError struct{ Msg string }

func (e *UsageError) Error() string { return e.Msg }

const (
	opRunID types.Op = "run_id"
	opNew   types.Op = "new"
	opWait  types.Op = "wait"

	maxDeadlineS   = 2592000 // 30 days
	maxContextSize = 64 << 10
)

// Slot is the engine-local mirror of a pre-reserved interrupt identity;
// new claims the next unused slot in order. Token is a bearer
// credential and must never be logged or returned outside the dict.
type Slot struct {
	ID        string
	ResumeURL string
	Token     string
}

// Waiting is the recorded outcome of a successful wait op.
type Waiting struct {
	InterruptID string
	Handler     string
	OnExpire    string
	Context     json.RawMessage
	ExpiresInS  int64
}

type reservation struct {
	slot       Slot
	expiresInS int64
}

// Host carries one step's interrupt state, built per call by the
// engine. The zero value is a valid host with no run and no slots;
// every op then reports that interrupts are unavailable.
type Host struct {
	RunID string
	Step  int
	Slots []Slot
	// IsHandler reports whether a name is a top-level one-argument
	// function of the program (set by the engine after module init).
	IsHandler func(name string) bool
	Limits    serving.Limits
	Secrets   *secrets.Set // the call-scoped set, for the context secret check
	// ToJSON is the bounded Starlark-to-JSON conversion (the engine's
	// MarshalJSONValue), injected to avoid an engine import.
	ToJSON func(starlark.Value) ([]byte, error)

	next         int
	reservations map[string]*reservation
	Waiting      *Waiting
}

// Factory returns the capability.Factory for "interrupt". It is never
// registered; Validate always fails because spec cannot grant it.
func Factory() capability.Factory { return factory{} }

type factory struct{}

func (factory) Name() types.CapabilityName { return Name }

func (factory) Ops() []capability.OpInfo {
	return []capability.OpInfo{
		{Name: opRunID, ResultMeta: map[string]string{"run_id": "string"},
			Doc: "return the current application run id"},
		{Name: opNew, ResultMeta: map[string]string{"id": "string"},
			Params: []string{"expires_in_s=None"},
			Doc:    "reserve an interrupt; returns its id, resume_url and resume_token"},
		{Name: opWait, PolicyArgs: map[string]string{"id": "string", "handler": "string", "on_expire": "string"},
			Params: []string{"id", "handler", "on_expire=None", "context"},
			Doc:    "commit a reserved interrupt and end the step waiting on it"},
	}
}

func (factory) ConfigFields() []capability.FieldDoc { return nil }

func (factory) Validate(json.RawMessage, capability.ServerLimits) (any, error) {
	return nil, errors.New("interrupt is granted automatically to application programs")
}

func (factory) Prompt(any) string {
	return "#### `interrupt`\n" +
		"- `interrupt.run_id()` returns the current run id.\n" +
		"- `interrupt.new(expires_in_s=None)` reserves an interrupt and returns `{id, resume_url, resume_token}`.\n" +
		"- `interrupt.wait(id, handler=..., context=...)` ends the step; POSTing a JSON event to resume_url calls the handler.\n"
}

func (f factory) Help(any) []capability.HelpEntry {
	var b strings.Builder
	b.WriteString("#### `interrupt`\n")
	for _, op := range f.Ops() {
		b.WriteString(capability.OpLine(Name, op) + "\n")
	}
	out := []capability.HelpEntry{{
		Name:    string(Name),
		Summary: "application run interrupts",
		Text:    b.String(),
	}}
	for _, op := range f.Ops() {
		out = append(out, capability.OpHelpEntry(Name, op))
	}
	return out
}

// New is unreachable: the factory is never registered, so specs can
// never carry interrupt config.
func (factory) New(any, capability.InstanceEnv) (starlark.Value, io.Closer, error) {
	return nil, nil, errors.New("interrupt is granted automatically to application programs")
}

// errNoRun is the generic failure for interrupt ops outside an
// application run.
func errNoRun(op string) error {
	return &UsageError{Msg: "interrupt." + op + ": interrupts are only available in application runs"}
}

// Binding builds the frozen `interrupt` module for one session,
// dispatching every op through the gate.
func Binding(h *Host, gate *capability.Gate) starlark.Value {
	members := starlark.StringDict{
		"run_id": starlark.NewBuiltin("interrupt.run_id", func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			if err := starlark.UnpackPositionalArgs("run_id", args, kwargs, 0); err != nil {
				return nil, err
			}
			return gate.Invoke(capability.ThreadContext(thread), Name, opRunID, nil,
				func(ctx context.Context, _ map[string]any) (starlark.Value, map[string]any, error) {
					if h.RunID == "" {
						return nil, nil, errNoRun("run_id")
					}
					return starlark.String(h.RunID), map[string]any{"run_id": h.RunID}, nil
				})
		}),
		"new": starlark.NewBuiltin("interrupt.new", func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			var expiresInS starlark.Value
			if err := starlark.UnpackArgs("new", args, kwargs, "expires_in_s?", &expiresInS); err != nil {
				return nil, err
			}
			return gate.Invoke(capability.ThreadContext(thread), Name, opNew, nil,
				func(ctx context.Context, _ map[string]any) (starlark.Value, map[string]any, error) {
					return h.newInterrupt(expiresInS)
				})
		}),
		"wait": starlark.NewBuiltin("interrupt.wait", func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			var id string
			var handler string
			var onExpire string
			var ctxVal starlark.Value
			if err := starlark.UnpackArgs("wait", args, kwargs,
				"id", &id,
				"handler", &handler,
				"on_expire?", &onExpire,
				"context", &ctxVal); err != nil {
				return nil, err
			}
			callArgs := map[string]any{"id": id, "handler": handler}
			if onExpire != "" {
				callArgs["on_expire"] = onExpire
			}
			v, err := gate.Invoke(capability.ThreadContext(thread), Name, opWait, callArgs,
				func(ctx context.Context, _ map[string]any) (starlark.Value, map[string]any, error) {
					return h.waitInterrupt(id, handler, onExpire, ctxVal)
				})
			if err != nil {
				return nil, err
			}
			_ = v // the op body returns None; the audit record is already written
			return nil, ErrWait
		}),
	}
	return starlarkstruct.FromStringDict(starlarkstruct.Default, members)
}

// newInterrupt runs inside the gate and implements interrupt.new: it
// claims the next pre-reserved slot for this step.
func (h *Host) newInterrupt(expiresV starlark.Value) (starlark.Value, map[string]any, error) {
	if h.RunID == "" {
		return nil, nil, errNoRun("new")
	}
	expiresInS, err := secondsArg("expires_in_s", expiresV)
	if err != nil {
		return nil, nil, err
	}
	if h.next >= len(h.Slots) {
		if len(h.Slots) == 0 {
			return nil, nil, &UsageError{Msg: "interrupt.new: interrupts are unavailable on this server (requires --secret-key)"}
		}
		return nil, nil, &UsageError{Msg: "interrupt.new: interrupt limit for this step reached"}
	}
	slot := h.Slots[h.next]
	h.next++
	if h.reservations == nil {
		h.reservations = map[string]*reservation{}
	}
	h.reservations[slot.ID] = &reservation{slot: slot, expiresInS: expiresInS}

	d := starlark.NewDict(4)
	set := func(k string, v starlark.Value) { _ = d.SetKey(starlark.String(k), v) }
	set("id", starlark.String(slot.ID))
	set("resume_url", starlark.String(slot.ResumeURL))
	set("resume_token", starlark.String(slot.Token))
	if expiresInS != 0 {
		set("expires_in_s", starlark.MakeInt64(expiresInS))
	}
	d.Freeze()
	return d, map[string]any{"id": slot.ID}, nil
}

// waitInterrupt runs inside the gate and implements interrupt.wait.
func (h *Host) waitInterrupt(id, handler, onExpire string, ctxVal starlark.Value) (starlark.Value, map[string]any, error) {
	res, ok := h.reservations[id]
	if !ok {
		return nil, nil, &UsageError{Msg: "interrupt.wait: interrupt id was not reserved in this step"}
	}
	isHandler := func(n string) bool { return h.IsHandler != nil && h.IsHandler(n) }
	if !isHandler(handler) {
		return nil, nil, &UsageError{Msg: "interrupt.wait: handler must name a top-level one-argument function (not main, not _-prefixed)"}
	}
	if onExpire != "" && !isHandler(onExpire) {
		return nil, nil, &UsageError{Msg: "interrupt.wait: on_expire must name a top-level one-argument function (not main, not _-prefixed)"}
	}
	dict, ok := ctxVal.(*starlark.Dict)
	if !ok {
		return nil, nil, &UsageError{Msg: "interrupt.wait: context must be a dict"}
	}
	if h.ToJSON == nil {
		return nil, nil, &UsageError{Msg: "interrupt.wait: context is not supported here"}
	}
	raw, err := h.ToJSON(dict)
	if err != nil {
		return nil, nil, &UsageError{Msg: "interrupt.wait: context is not a bounded JSON value"}
	}
	if len(raw) > maxContextSize {
		return nil, nil, &UsageError{Msg: "interrupt.wait: context exceeds the byte limit"}
	}
	// Fail closed on known secrets: a redacted context would silently
	// corrupt the next step.
	red, rerr := h.Secrets.RedactBounded(context.Background(), string(raw), maxContextSize)
	if rerr != nil || red != string(raw) {
		return nil, nil, &UsageError{Msg: "interrupt.wait: context contains a secret value"}
	}
	h.Waiting = &Waiting{
		InterruptID: id,
		Handler:     handler,
		OnExpire:    onExpire,
		Context:     json.RawMessage(raw),
		ExpiresInS:  res.expiresInS,
	}
	return starlark.None, map[string]any{"id": id}, nil
}

// secondsArg parses an optional whole-second deadline argument into
// int64; 0 means absent.
func secondsArg(name string, v starlark.Value) (int64, error) {
	if v == nil {
		return 0, nil
	}
	if _, ok := v.(starlark.NoneType); ok {
		return 0, nil
	}
	i, ok := v.(starlark.Int)
	if !ok {
		return 0, &UsageError{Msg: "interrupt.new: " + name + " must be an integer"}
	}
	n, ok := i.Int64()
	if !ok || n < 1 || n > maxDeadlineS {
		return 0, &UsageError{Msg: fmt.Sprintf("interrupt.new: %s must be between 1 and %d", name, maxDeadlineS)}
	}
	return n, nil
}
