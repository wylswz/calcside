// Package engine runs Starlark code against an instance's capability
// bindings: it serializes execs per instance, enforces step/time/output
// limits, persists mutable globals across execs (REPL chunk semantics),
// classifies errors, and runs a heap watchdog that cancels execs when
// process memory exceeds the configured limit.
package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"runtime/metrics"
	"sync"
	"time"

	"go.starlark.net/resolve"
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"

	"calcside/internal/capability"
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

// WithMemoryLimit sets the heap watchdog limit in bytes; 0 disables.
func WithMemoryLimit(bytes uint64) Option {
	return func(e *Engine) { e.memLimit = bytes }
}

// WithHeapSampler overrides the heap sampler (tests).
func WithHeapSampler(f func() uint64) Option {
	return func(e *Engine) { e.sampler = f }
}

// WithWatchdogInterval overrides the 100ms watchdog tick (tests).
func WithWatchdogInterval(d time.Duration) Option {
	return func(e *Engine) { e.interval = d }
}

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
func (e *Engine) Exec(ctx context.Context, s *Session, execID, code string, timeout time.Duration, maxSteps uint64, output OutputReader) Result {
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
	for k, v := range s.Predeclared {
		s.Globals[k] = v
	}

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
