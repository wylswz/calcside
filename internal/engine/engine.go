// Package engine runs Starlark code against an instance's capability
// bindings: it serializes execs per instance, enforces step/time/output
// limits, persists mutable globals across execs (REPL chunk semantics),
// classifies errors, and runs a heap watchdog that cancels execs when
// process memory exceeds the configured limit.
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"runtime"
	"runtime/metrics"
	"sort"
	"sync"
	"time"

	"go.starlark.net/resolve"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"
	"go.starlark.net/syntax"

	"calcside/internal/capability"
	capio "calcside/internal/capability/io"
	"calcside/internal/secrets"
	"calcside/internal/stdlib"
	"calcside/internal/types"
)

// Session is the per-instance runtime state shared by all execs.
type Session struct {
	Gate        *capability.Gate
	Predeclared starlark.StringDict // capabilities + json + math + print
	Globals     starlark.StringDict // user globals, persisted across execs (mutable)
	ExecMu      sync.Mutex          // serializes execs on this instance

	InstanceID string
	UserID     string
	UserEmail  string
	Labels     map[string]string

	// Out captures print/io output; the caller resets it per exec.
	Out *capio.Buffer
	// Capabilities are the granted typed configs, including implicit io.
	Capabilities map[string]any
	// Closers indexes capability closers by name, for host-side access
	// (console file browse) without starlark types.
	Closers map[types.CapabilityName]io.Closer
	closers []io.Closer // build order, for Close
}

// execContext builds the gate's ExecContext for one exec.
func (s *Session) execContext(execID string) capability.ExecContext {
	return capability.ExecContext{
		ExecID:     execID,
		InstanceID: s.InstanceID,
		UserID:     s.UserID,
		UserEmail:  s.UserEmail,
		Labels:     s.Labels,
	}
}

// Inspect snapshots the session globals: variable name to its Starlark
// repr. It takes ExecMu so the snapshot cannot observe a globals dict
// mid-mutation; values are rendered with String, so they are already
// safe to show — the caller still scrubs secret values as it does for
// exec output.
func (s *Session) Inspect(ctx context.Context) *InspectResult {
	s.ExecMu.Lock()
	defer s.ExecMu.Unlock()
	vars := map[string]string{}
	for varName, g := range s.Globals {
		if inspectBudget(g, maxInspectWork, 64) < 0 {
			vars[varName] = "<value omitted: inspection limit>"
		} else {
			vars[varName] = g.String()
		}
	}
	return &InspectResult{
		Variables: vars,
	}
}

const maxInspectWork = 64 << 10

var (
	maxInspectInt = starlark.MakeInt(1).Lsh(maxInspectWork)
	minInspectInt = starlark.MakeInt(-1).Lsh(maxInspectWork)
)

func inspectBudget(v starlark.Value, budget, depth int) int {
	budget -= 32
	if budget < 0 || depth == 0 {
		return -1
	}
	text := ""
	switch v := v.(type) {
	case starlark.String:
		text = string(v)
	case starlark.Bytes:
		text = string(v)
	case starlark.Int:
		if _, ok := v.Int64(); !ok {
			hi, _ := v.Cmp(maxInspectInt, 0)
			lo, _ := v.Cmp(minInspectInt, 0)
			if hi >= 0 || lo <= 0 {
				return -1
			}
			budget -= v.BigInt().BitLen()
		}
	case starlark.NoneType, starlark.Bool, starlark.Float, secretsStruct:
	case *starlark.Function:
		text = v.Name()
	case *starlark.Builtin:
		text = v.Name()
		if recv := v.Receiver(); recv != nil {
			budget -= len(recv.Type())
		}
	case *starlarkstruct.Module:
		text = v.Name
	case *starlarkstruct.Struct:
		budget = inspectBudget(v.Constructor(), budget, depth-1)
		for name, value := range v.Entries() {
			budget = inspectBudget(value, budget-len(name), depth-1)
			if budget < 0 {
				return -1
			}
		}
	case *starlark.Dict:
		for key, value := range v.Entries() {
			budget = inspectBudget(key, budget, depth-1)
			budget = inspectBudget(value, budget, depth-1)
			if budget < 0 {
				return -1
			}
		}
	case *starlark.List, starlark.Tuple, *starlark.Set:
		iter := v.(starlark.Iterable).Iterate()
		defer iter.Done()
		var elem starlark.Value
		for iter.Next(&elem) {
			budget = inspectBudget(elem, budget, depth-1)
			if budget < 0 {
				return -1
			}
		}
	default:
		return -1
	}
	if budget < 0 || len(text) > budget/4 {
		return -1
	}
	return budget - 4*len(text)
}

// Close revokes the gate and releases capability resources.
func (s *Session) Close() {
	s.Gate.Revoke()
	for _, c := range s.closers {
		_ = c.Close()
	}
}

// SessionCreation is the plain-data description of a session: whose it
// is and what it is granted. Everything in it derives from a
// runtime.CreateRequest after capability.NormalizeSpec.
type SessionCreation struct {
	InstanceID string
	UserID     string
	UserEmail  string
	Labels     map[string]string

	// Capabilities maps capability name to its validated, typed config.
	// io is granted even when absent.
	Capabilities   map[string]any
	Policies       []string
	Env            map[string]string
	MaxSteps       uint64
	MaxOutputBytes int64
}

// SessionDeps are the live collaborators a session is wired to — the
// part of session creation that is not data.
type SessionDeps struct {
	Context  context.Context
	Registry *capability.Registry
	Limits   capability.ServerLimits
	Hooks    []capability.Hook
	Observer capability.Observer
	// Secrets backs secret injection and the `secrets` global; may be nil.
	Secrets *secrets.Set
}

// NewSession builds the gate, capability bindings and predeclared
// globals for one instance. On error every capability built so far is
// closed.
func NewSession(d SessionDeps, c SessionCreation) (*Session, error) {
	if d.Context == nil {
		d.Context = context.Background()
	}
	labels := c.Labels
	if labels == nil {
		labels = map[string]string{}
	}
	gate := capability.NewGate(capability.GateOwner{
		InstanceID: c.InstanceID,
		UserID:     c.UserID,
		UserEmail:  c.UserEmail,
		Labels:     labels,
	}, d.Hooks, d.Observer)

	// env: frozen dict (env.get / env["X"] / env.keys()); secrets: names
	// only. Neither goes through the gate.
	envDict := starlark.NewDict(len(c.Env))
	for k, v := range c.Env {
		_ = envDict.SetKey(starlark.String(k), starlark.String(v))
	}
	envDict.Freeze()
	predeclared := stdlib.Modules()
	predeclared["env"] = envDict
	predeclared["secrets"] = secretsStruct{set: d.Secrets}

	// io is always granted, even when not listed in the spec.
	caps := maps.Clone(c.Capabilities)
	if caps == nil {
		caps = map[string]any{}
	}
	ioName := string(types.CapIO)
	if _, ok := caps[ioName]; !ok {
		if f, ok := d.Registry.Get(types.CapIO); ok {
			cfg, err := f.Validate(json.RawMessage(fmt.Sprintf(`{"max_output_bytes":%d}`, c.MaxOutputBytes)), d.Limits)
			if err != nil {
				return nil, err
			}
			caps[ioName] = cfg
		}
	}

	names := make([]string, 0, len(caps))
	for n := range caps {
		if n != string(types.CapExt) {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	// ext is built last: its bindings compose the other capabilities.
	if _, ok := caps[string(types.CapExt)]; ok {
		names = append(names, string(types.CapExt))
	}

	s := &Session{
		Gate:         gate,
		Predeclared:  predeclared,
		Globals:      starlark.StringDict{},
		InstanceID:   c.InstanceID,
		UserID:       c.UserID,
		UserEmail:    c.UserEmail,
		Labels:       labels,
		Capabilities: caps,
		Closers:      map[types.CapabilityName]io.Closer{},
	}
	fail := func(err error) (*Session, error) {
		for _, cl := range s.closers {
			_ = cl.Close()
		}
		return nil, err
	}
	bindings := map[types.CapabilityName]starlark.Value{}
	for _, name := range names {
		f, ok := d.Registry.Get(types.CapabilityName(name))
		if !ok {
			return fail(fmt.Errorf("unknown capability %s", name))
		}
		val, closer, err := f.New(caps[name], capability.InstanceEnv{Context: d.Context, Gate: gate, Secrets: d.Secrets, Bindings: bindings, MaxSteps: c.MaxSteps, Policies: c.Policies})
		if err != nil {
			return fail(fmt.Errorf("capability %s: %s", name, err))
		}
		if closer != nil {
			s.closers = append(s.closers, closer)
			s.Closers[types.CapabilityName(name)] = closer
			if ioc, ok := closer.(*capio.Closer); ok {
				s.Out = ioc.B
			}
		}
		predeclared[name] = val
		bindings[types.CapabilityName(name)] = val
	}
	if s.Out == nil {
		s.Out = capio.NewBuffer(c.MaxOutputBytes)
	}
	predeclared["print"] = capio.PrintBuiltin(s.Out, gate)

	if err := ValidatePredeclared(predeclared); err != nil {
		return fail(err)
	}
	return s, nil
}

// Error is the structured script error in an ExecResult.
type Error struct {
	Type      types.ExecErrorType `json:"type"` // syntax|runtime|policy_denied|out_of_scope|timeout|step_limit|memory_limit
	Message   string              `json:"message"`
	Backtrace string              `json:"backtrace,omitempty"`
}

// Result of one execution.
type Result struct {
	ExecID     string `json:"exec_id"`
	Output     string `json:"output"`
	Error      *Error `json:"error"`
	DurationMs int64  `json:"duration_ms"`
	Steps      uint64 `json:"steps"`
}

type InspectResult struct {
	Variables map[string]string `json:"variables"`
}

// ErrMemoryLimit is the cancel cause when the watchdog kills execs.
var ErrMemoryLimit = errors.New("exec memory limit exceeded")

// Engine executes Starlark programs and watches process heap.
type Engine struct {
	sem chan struct{} // bounds concurrent execs globally

	memLimit uint64
	sampler  func() uint64
	interval time.Duration

	runningMu sync.Mutex
	running   map[string]context.CancelCauseFunc

	stopWatch chan struct{}
	watchWg   sync.WaitGroup
}

// Option configures the engine.
type Option func(*Engine)

func defaultHeapSampler() func() uint64 {
	return func() uint64 {
		var m [1]metrics.Sample
		m[0].Name = "/memory/classes/heap/objects:bytes"
		metrics.Read(m[:])
		return m[0].Value.Uint64()
	}
}

func New(maxConcurrent int, opts ...Option) *Engine {
	if maxConcurrent <= 0 {
		maxConcurrent = 64
	}
	e := &Engine{
		sem:       make(chan struct{}, maxConcurrent),
		sampler:   defaultHeapSampler(),
		interval:  100 * time.Millisecond,
		running:   map[string]context.CancelCauseFunc{},
		stopWatch: make(chan struct{}),
	}
	for _, o := range opts {
		o(e)
	}
	if e.memLimit > 0 {
		e.watchWg.Add(1)
		go e.watchdog()
	}
	return e
}

// Close stops the watchdog goroutine.
func (e *Engine) Close() {
	close(e.stopWatch)
	e.watchWg.Wait()
}

// watchdog samples heap and cancels all running execs when over limit.
func (e *Engine) watchdog() {
	defer e.watchWg.Done()
	t := time.NewTicker(e.interval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			heap := e.sampler()
			if heap <= e.memLimit {
				continue
			}
			e.runningMu.Lock()
			n := len(e.running)
			for _, cancel := range e.running {
				cancel(ErrMemoryLimit)
			}
			e.runningMu.Unlock()
			if n > 0 {
				slog.Warn("exec memory limit exceeded; cancelled execs",
					"heap_bytes", heap, "limit", e.memLimit, "cancelled", n)
			}
			runtime.GC()
		case <-e.stopWatch:
			return
		}
	}
}

var fileOpts = &syntax.FileOptions{
	Set:             true,
	While:           true,
	TopLevelControl: true,
	GlobalReassign:  true,
	Recursion:       true,
}

// ValidatePredeclared rejects extras that shadow Starlark builtins.
func ValidatePredeclared(pre starlark.StringDict) error {
	for name := range pre {
		if name == "print" {
			continue // we deliberately override the print builtin
		}
		if _, ok := starlark.Universe[name]; ok {
			return fmt.Errorf("predeclared name %q shadows a builtin", name)
		}
	}
	return nil
}

// OutputReader returns the exec output buffer contents.
type OutputReader func() string

// Exec runs code in the session, returning a classified result. Code is
// executed as a REPL chunk directly in the session globals dict, so
// mutable values (lists, dicts) persist and stay mutable across execs.
// Capability bindings are re-injected each exec so user code cannot
// permanently shadow them.
func (e *Engine) Exec(
	ctx context.Context,
	s *Session, execID,
	code string,
	timeout time.Duration,
	maxSteps uint64,
	output OutputReader,
) Result {
	start := time.Now()
	res := Result{ExecID: execID}

	select {
	case e.sem <- struct{}{}:
		defer func() { <-e.sem }()
	case <-ctx.Done():
		res.Error = &Error{Type: types.ErrTimeout, Message: "waiting for exec slot: " + ctx.Err().Error()}
		return res
	}

	s.ExecMu.Lock()
	defer s.ExecMu.Unlock()

	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	if timeout > 0 {
		var tCancel context.CancelFunc
		ctx, tCancel = context.WithTimeout(ctx, timeout)
		defer tCancel()
	}

	thread := &starlark.Thread{Name: "exec " + execID}
	if maxSteps > 0 {
		thread.SetMaxExecutionSteps(maxSteps)
	}
	thread.SetLocal(capability.ContextKey, ctx)

	e.runningMu.Lock()
	e.running[execID] = cancel
	e.runningMu.Unlock()
	defer func() {
		e.runningMu.Lock()
		delete(e.running, execID)
		e.runningMu.Unlock()
	}()

	cancelDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			cause := context.Cause(ctx)
			if cause == nil {
				cause = ctx.Err()
			}
			thread.Cancel(cause.Error())
		case <-cancelDone:
		}
	}()
	defer close(cancelDone)

	s.Gate.Arm(s.execContext(execID))
	defer s.Gate.Disarm()

	// Re-inject capability bindings + modules so exec N-1 shadowing does
	// not leak into exec N.
	maps.Copy(s.Globals, s.Predeclared)

	f, err := fileOpts.Parse(execID+".star", code, 0)
	if err == nil {
		err = starlark.ExecREPLChunk(f, thread, s.Globals)
	}
	res.Steps = thread.ExecutionSteps()
	res.DurationMs = time.Since(start).Milliseconds()
	if output != nil {
		res.Output = output()
	}
	if err != nil {
		res.Error = classify(ctx, err, maxSteps, res.Steps)
	} else if maxSteps > 0 && res.Steps >= maxSteps {
		res.Error = &Error{Type: types.ErrStepLimit, Message: "exceeded execution step limit"}
	}
	return res
}

// classify maps an exec error to a stable error type without string
// matching: ctx cause first (timeout/memory_limit), then step exhaustion,
// then Go type assertions.
func classify(ctx context.Context, err error, maxSteps, steps uint64) *Error {
	var evalErr *starlark.EvalError
	var denied *capability.DeniedError
	msg := err.Error()
	bt := ""
	if errors.As(err, &evalErr) {
		bt = evalErr.Backtrace()
		msg = evalErr.Msg
	}

	if errors.Is(err, capability.ErrOutOfScope) {
		return &Error{Type: types.ErrOutOfScope, Message: msg, Backtrace: bt}
	}
	if errors.As(err, &denied) {
		return &Error{Type: types.ErrPolicyDenied, Message: denied.Error(), Backtrace: bt}
	}

	// Parse errors (syntax.Error) and resolve errors (resolve.ErrorList,
	// e.g. undefined names) are static errors.
	var synErr syntax.Error
	var resErr resolve.ErrorList
	if errors.As(err, &synErr) || errors.As(err, &resErr) {
		return &Error{Type: types.ErrSyntax, Message: msg, Backtrace: bt}
	}

	// Context cause distinguishes timeout from watchdog cancellation.
	if cause := context.Cause(ctx); cause != nil || ctx.Err() != nil {
		switch {
		case errors.Is(cause, ErrMemoryLimit):
			return &Error{Type: types.ErrMemoryLimit, Message: msg, Backtrace: bt}
		default:
			return &Error{Type: types.ErrTimeout, Message: msg, Backtrace: bt}
		}
	}

	if maxSteps > 0 && steps >= maxSteps {
		return &Error{Type: types.ErrStepLimit, Message: msg, Backtrace: bt}
	}
	return &Error{Type: types.ErrRuntime, Message: msg, Backtrace: bt}
}
