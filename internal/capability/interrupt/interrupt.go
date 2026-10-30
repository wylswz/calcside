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
	"unicode/utf8"

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

const (
	opRunID types.Op = "run_id"
	opNew   types.Op = "new"
	opWait  types.Op = "wait"

	maxTitleRunes  = 200
	maxAssignees   = 16
	maxDeadlineS   = 2592000 // 30 days
	maxContextSize = 64 << 10
)

// Slot is the engine-local mirror of a pre-reserved interrupt identity;
// new claims the next unused slot in order. Token is a bearer
// credential and must never be logged or returned outside the dict.
type Slot struct {
	ID          string
	ConsoleURL  string
	CallbackURL string
	Token       string
}

// Waiting is the recorded outcome of a successful wait op.
type Waiting struct {
	InterruptID string
	Handler     string
	OnExpire    string
	Context     json.RawMessage
	Kind        string
	Title       string
	Assignees   []string
	EventSchema json.RawMessage
	ExpiresInS  int64
	FireInS     int64
}

type reservation struct {
	slot        Slot
	kind        string
	title       string
	assignees   []string
	eventSchema json.RawMessage
	expiresInS  int64
	fireInS     int64
}

// Host carries one step's interrupt state, built per call by the
// engine. The zero value is a valid host with no run and no slots;
// every op then reports that interrupts are unavailable.
type Host struct {
	RunID    string
	Step     int
	Slots    []Slot
	Handlers map[string]*serving.Schema // compiled context schemas by handler name
	Limits   serving.Limits
	Secrets  *secrets.Set // the call-scoped set, for the context secret check
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
		{Name: opNew, PolicyArgs: map[string]string{"kind": "string"}, ResultMeta: map[string]string{"id": "string", "kind": "string"},
			Params: []string{"kind", "title=None", "assignees=None", "event_schema=None", "expires_in_s=None", "fire_in_s=None"},
			Doc:    "reserve a unique interrupt (approval|callback|timer) and return its id and URLs"},
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
		"- `interrupt.new(kind, ...)` reserves an approval, callback or timer interrupt.\n" +
		"- `interrupt.wait(id, handler=..., context=...)` ends the step waiting on it.\n"
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
var errNoRun = errors.New("interrupts are only available in application runs")

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
						return nil, nil, errNoRun
					}
					return starlark.String(h.RunID), map[string]any{"run_id": h.RunID}, nil
				})
		}),
		"new": starlark.NewBuiltin("interrupt.new", func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			var kind string
			var title, assignees, eventSchema, expiresInS, fireInS starlark.Value
			if err := starlark.UnpackArgs("new", args, kwargs,
				"kind", &kind,
				"title?", &title,
				"assignees?", &assignees,
				"event_schema?", &eventSchema,
				"expires_in_s?", &expiresInS,
				"fire_in_s?", &fireInS); err != nil {
				return nil, err
			}
			callArgs := map[string]any{"kind": kind}
			return gate.Invoke(capability.ThreadContext(thread), Name, opNew, callArgs,
				func(ctx context.Context, _ map[string]any) (starlark.Value, map[string]any, error) {
					return h.newInterrupt(kind, title, assignees, eventSchema, expiresInS, fireInS)
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

// newInterrupt runs inside the gate and implements interrupt.new.
func (h *Host) newInterrupt(kind string, titleV, assigneesV, eventSchemaV, expiresV, fireV starlark.Value) (starlark.Value, map[string]any, error) {
	if h.RunID == "" {
		return nil, nil, errNoRun
	}
	if kind != "approval" && kind != "callback" && kind != "timer" {
		return nil, nil, errors.New("kind must be approval, callback or timer")
	}

	var title string
	if t, ok := titleV.(starlark.String); ok {
		title = string(t)
		if utf8.RuneCountInString(title) > maxTitleRunes {
			return nil, nil, fmt.Errorf("title exceeds %d characters", maxTitleRunes)
		}
	} else if _, ok := titleV.(starlark.NoneType); !ok && titleV != nil {
		return nil, nil, errors.New("title must be a string")
	}

	var assignees []string
	if l, ok := assigneesV.(*starlark.List); ok {
		if l.Len() > maxAssignees {
			return nil, nil, fmt.Errorf("assignees exceeds %d entries", maxAssignees)
		}
		assignees = make([]string, 0, l.Len())
		for i := 0; i < l.Len(); i++ {
			s, ok := l.Index(i).(starlark.String)
			if !ok || !strings.HasPrefix(string(s), "usr_") {
				return nil, nil, errors.New("assignees must be usr_-prefixed user ids")
			}
			assignees = append(assignees, string(s))
		}
	} else if _, ok := assigneesV.(starlark.NoneType); !ok && assigneesV != nil {
		return nil, nil, errors.New("assignees must be a list of user ids")
	}

	var schemaRaw json.RawMessage
	if d, ok := eventSchemaV.(*starlark.Dict); ok {
		if kind == "timer" {
			return nil, nil, errors.New("timer interrupts take no event_schema")
		}
		if h.ToJSON == nil {
			return nil, nil, errors.New("event_schema is not supported here")
		}
		raw, err := h.ToJSON(d)
		if err != nil {
			return nil, nil, errors.New("event_schema is not a bounded JSON value")
		}
		var obj map[string]any
		if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
			return nil, nil, errors.New("event_schema must be a JSON object")
		}
		if obj["type"] != "object" {
			return nil, nil, errors.New("event_schema must be an object schema")
		}
		if _, err := serving.CompileSchema(raw, h.Limits); err != nil {
			return nil, nil, errors.New("event_schema does not satisfy the contract schema rules")
		}
		schemaRaw = raw
	} else if _, ok := eventSchemaV.(starlark.NoneType); !ok && eventSchemaV != nil {
		return nil, nil, errors.New("event_schema must be a dict")
	}

	expiresInS, err := secondsArg("expires_in_s", expiresV)
	if err != nil {
		return nil, nil, err
	}
	fireInS, err := secondsArg("fire_in_s", fireV)
	if err != nil {
		return nil, nil, err
	}
	switch kind {
	case "timer":
		if fireInS == 0 {
			return nil, nil, errors.New("timer interrupts require fire_in_s")
		}
		if expiresInS != 0 {
			return nil, nil, errors.New("timer interrupts take no expires_in_s")
		}
	default:
		if fireInS != 0 {
			return nil, nil, errors.New("fire_in_s is only valid for timer interrupts")
		}
	}

	if h.next >= len(h.Slots) {
		return nil, nil, errors.New("interrupt limit for this step reached")
	}
	slot := h.Slots[h.next]
	h.next++
	if h.reservations == nil {
		h.reservations = map[string]*reservation{}
	}
	h.reservations[slot.ID] = &reservation{
		slot:        slot,
		kind:        kind,
		title:       title,
		assignees:   assignees,
		eventSchema: schemaRaw,
		expiresInS:  expiresInS,
		fireInS:     fireInS,
	}

	d := starlark.NewDict(6)
	set := func(k string, v starlark.Value) { _ = d.SetKey(starlark.String(k), v) }
	set("id", starlark.String(slot.ID))
	set("kind", starlark.String(kind))
	set("console_url", starlark.String(slot.ConsoleURL))
	if kind == "callback" {
		set("callback_url", starlark.String(slot.CallbackURL))
		set("callback_token", starlark.String(slot.Token))
	}
	if expiresInS != 0 {
		set("expires_in_s", starlark.MakeInt64(expiresInS))
	}
	if fireInS != 0 {
		set("fire_in_s", starlark.MakeInt64(fireInS))
	}
	d.Freeze()
	return d, map[string]any{"id": slot.ID, "kind": kind}, nil
}

// waitInterrupt runs inside the gate and implements interrupt.wait.
func (h *Host) waitInterrupt(id, handler, onExpire string, ctxVal starlark.Value) (starlark.Value, map[string]any, error) {
	res, ok := h.reservations[id]
	if !ok {
		return nil, nil, errors.New("interrupt id was not reserved in this step")
	}
	handlerSchema, ok := h.Handlers[handler]
	if !ok {
		return nil, nil, errors.New("handler is not declared")
	}
	var expireSchema *serving.Schema
	if onExpire != "" {
		expireSchema, ok = h.Handlers[onExpire]
		if !ok {
			return nil, nil, errors.New("on_expire is not a declared handler")
		}
	}
	dict, ok := ctxVal.(*starlark.Dict)
	if !ok {
		return nil, nil, errors.New("context must be a dict")
	}
	if h.ToJSON == nil {
		return nil, nil, errors.New("context is not supported here")
	}
	raw, err := h.ToJSON(dict)
	if err != nil {
		return nil, nil, errors.New("context is not a bounded JSON value")
	}
	if len(raw) > maxContextSize {
		return nil, nil, errors.New("context exceeds the byte limit")
	}
	// Fail closed on known secrets: a redacted context would silently
	// corrupt the next step.
	red, rerr := h.Secrets.RedactBounded(context.Background(), string(raw), maxContextSize)
	if rerr != nil || red != string(raw) {
		return nil, nil, errors.New("context contains a secret value")
	}
	if err := handlerSchema.Validate(raw); err != nil {
		return nil, nil, errors.New("context does not match the handler schema")
	}
	if expireSchema != nil {
		if err := expireSchema.Validate(raw); err != nil {
			return nil, nil, errors.New("context does not match the on_expire handler schema")
		}
	}
	h.Waiting = &Waiting{
		InterruptID: id,
		Handler:     handler,
		OnExpire:    onExpire,
		Context:     json.RawMessage(raw),
		Kind:        res.kind,
		Title:       res.title,
		Assignees:   res.assignees,
		EventSchema: res.eventSchema,
		ExpiresInS:  res.expiresInS,
		FireInS:     res.fireInS,
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
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	n, ok := i.Int64()
	if !ok || n < 1 || n > maxDeadlineS {
		return 0, fmt.Errorf("%s must be between 1 and %d", name, maxDeadlineS)
	}
	return n, nil
}
