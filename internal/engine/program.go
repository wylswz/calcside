package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"math/big"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.starlark.net/starlark"

	"calcside/internal/capability"
	capfs "calcside/internal/capability/fs"
	"calcside/internal/secrets"
	"calcside/internal/serving"
	"calcside/internal/types"
)

// ProgramConfig is the trusted, release-shaped description of a serving
// program: the selected sources (entrypoint among them), bounded assets,
// the two contract schemas and the JSON budgets. Callers later supply
// release-pinned SessionDeps/SessionCreation per call; this engine type
// does not enforce release hashing or pinned specs itself.
type ProgramConfig struct {
	Entrypoint   string
	Sources      map[string][]byte
	Assets       map[string][]byte
	InputSchema  json.RawMessage
	OutputSchema json.RawMessage
	JSONLimits   serving.Limits
}

// ProgramCall parameterizes one prepare or invoke.
type ProgramCall struct {
	ID         string // required, unique among in-flight calls
	ActorID    string
	ActorEmail string
	Timeout    time.Duration // <=0 selects the default 30s
}

// ProgramError is a classified program failure. Messages are generic:
// they never echo Starlark tracebacks, source text or user values.
type ProgramError struct {
	Code    string // invalid_program|invalid_input|invalid_output|runtime|timeout|step_limit|memory_limit|policy_denied|out_of_scope
	Message string
}

// ProgramResult is the outcome of one call; Output is only set on
// success. Stdout is bounded and secret-redacted.
type ProgramResult struct {
	ID         string
	Output     json.RawMessage
	Stdout     string
	Error      *ProgramError
	DurationMs int64
	Steps      uint64
}

// Program is the immutable result of PrepareProgram: compiled sources,
// cloned assets, compiled schemas and budgets. It holds no globals,
// sessions, gates, secrets, hooks or live deps, so one Program serves
// concurrent invokes safely.
type Program struct {
	entrypoint   string
	sources      map[string][]byte // cloned sources kept for provenance
	programs     map[string]*starlark.Program
	assets       map[string][]byte
	inputSchema  *serving.Schema
	outputSchema *serving.Schema
	limits       serving.Limits
	predeclared  []string // sorted name set captured at prepare
}

const (
	defaultProgramTimeout = 30 * time.Second
	defaultProgramSteps   = 10_000_000
	defaultProgramOutput  = 1 << 20

	programMaxSources    = 64
	programMaxFiles      = 128
	programMaxFileBytes  = 1 << 20
	programMaxSource     = 1 << 20
	programMaxAssetBytes = 8 << 20
	programMaxPathLen    = 1024

	stdoutOmitted = "[output omitted]"
)

func progErr(code, msg string) *ProgramError {
	return &ProgramError{Code: code, Message: msg}
}

func invalidProgramf(format string, args ...any) *ProgramError {
	return progErr("invalid_program", fmt.Sprintf(format, args...))
}

// runtimeProgErr maps a classified exec error to a generic per-code
// message. Timeout explicitly records that the outcome may be unknown;
// the engine never retries.
func runtimeProgErr(t types.ExecErrorType) *ProgramError {
	switch t {
	case types.ErrTimeout:
		return progErr("timeout", "execution timed out; the outcome may be unknown")
	case types.ErrStepLimit:
		return progErr("step_limit", "execution exceeded the step limit")
	case types.ErrMemoryLimit:
		return progErr("memory_limit", "execution exceeded the memory limit")
	case types.ErrPolicyDenied:
		return progErr("policy_denied", "a capability call was denied")
	case types.ErrOutOfScope:
		return progErr("out_of_scope", "a capability call ran while the gate was disarmed")
	default:
		return progErr("runtime", "program execution failed")
	}
}

// PrepareProgram validates the config, builds a fresh session, compiles
// every selected source once, seeds assets, and runs the pure
// initialization -- top-level evaluation with the gate disarmed, so any
// capability call denies and no op body executes -- then verifies `main`
// is a function of exactly one positional parameter. `main` itself is
// never executed. The returned Program is immutable and reusable across
// invokes; the caller retains it for later InvokeProgram calls.
func (e *Engine) PrepareProgram(ctx context.Context, config ProgramConfig, deps SessionDeps, creation SessionCreation, call ProgramCall) (prog *Program, res ProgramResult) {
	start := time.Now()
	res.ID = call.ID

	// The call deadline spans preprocessing and execution alike. The
	// finalizer runs before tcancel so a real result is never turned
	// into a timeout by cleanup.
	ctx, tcancel := programTimeoutContext(ctx, call)
	defer tcancel()
	var scope *callScope
	defer func() {
		res.DurationMs = time.Since(start).Milliseconds()
		// Cancellation outranks every other outcome; capture metrics
		// and stdout while the session is still live and locked.
		if scope == nil {
			if perr := cancelledProgramErr(ctx); perr != nil {
				res.Error = perr
			}
			if res.Error != nil {
				prog = nil
			}
			return
		}
		res.Steps = scope.thread.ExecutionSteps()
		res.Stdout = scope.redactedStdout()
		if perr := cancelledProgramErr(scope.ctx); perr != nil {
			res.Error = perr
		}
		if res.Error != nil {
			prog = nil
		}
		scope.session.Close()
		scope.session.ExecMu.Unlock()
		scope.finish()
	}()

	if perr := cancelledProgramErr(ctx); perr != nil {
		res.Error = perr
		return nil, res
	}
	prog, perr := validateProgramConfig(config)
	if perr != nil {
		res.Error = perr
		return nil, res
	}
	if perr := cancelledProgramErr(ctx); perr != nil {
		res.Error = perr
		return nil, res
	}

	scope, perr = e.enterCall(ctx, call, deps, creation)
	if perr != nil {
		res.Error = perr
		return nil, res
	}

	s := scope.session
	s.SetActor(call.ActorID, call.ActorEmail)
	s.ExecMu.Lock()

	// Seed release assets into the fresh VFS.
	if err := seedAssets(scope.ctx, s, prog.assets); err != nil {
		if perr := cancelledProgramErr(scope.ctx); perr != nil {
			res.Error = perr
		} else {
			res.Error = invalidProgramf("asset seeding failed")
		}
		return nil, res
	}

	// Compile every selected source once against this session's
	// predeclared names.
	programs := map[string]*starlark.Program{}
	for _, name := range slices.Sorted(maps.Keys(prog.sources)) {
		if perr := cancelledProgramErr(scope.ctx); perr != nil {
			res.Error = perr
			return nil, res
		}
		_, pr, err := starlark.SourceProgramOptions(fileOpts, name, prog.sources[name], s.Predeclared.Has)
		if err != nil {
			res.Error = invalidProgramf("a selected source does not compile")
			return nil, res
		}
		programs[name] = pr
	}
	prog.programs = programs
	if perr := checkLoadGraph(programs); perr != nil {
		res.Error = perr
		return nil, res
	}
	prog.predeclared = slices.Sorted(maps.Keys(s.Predeclared))

	if perr := cancelledProgramErr(scope.ctx); perr != nil {
		res.Error = perr
		return nil, res
	}
	// Pure init: gate stays disarmed; denied calls fail the prepare.
	globals, ierr := prog.initModules(scope.thread, s.Predeclared)
	if ierr == nil {
		ierr = checkMain(globals)
	}
	if ierr != nil {
		res.Error = initFailureErr(scope.ctx, scope.thread, scope.maxSteps)
		return nil, res
	}
	return prog, res
}

// InvokeProgram runs one call against a prepared Program. The raw input
// is decoded, schema-validated and converted to a frozen Starlark value
// before any session or capability is built -- invalid input produces
// zero factory builds and zero capability ops. A fresh session is
// created every call: assets are reseeded, all modules reinitialize
// from scratch (gate disarmed), the gate arms only around
// starlark.Call(main, frozenInput), and output is converted, redacted
// and output-schema validated before the session closes. The caller
// supplies one per-call observer; all observation and cleanup completes
// synchronously before return, so no audit drain can race.
func (e *Engine) InvokeProgram(ctx context.Context, program *Program, input json.RawMessage, deps SessionDeps, creation SessionCreation, call ProgramCall) (res ProgramResult) {
	start := time.Now()
	res.ID = call.ID

	// The call deadline spans preprocessing and execution alike. The
	// finalizer runs before tcancel so a real result is never turned
	// into a timeout by cleanup.
	ctx, tcancel := programTimeoutContext(ctx, call)
	defer tcancel()
	var scope *callScope
	defer func() {
		res.DurationMs = time.Since(start).Milliseconds()
		// Cancellation outranks every other outcome; capture metrics
		// and stdout while the session is still live and locked.
		if scope == nil {
			if perr := cancelledProgramErr(ctx); perr != nil {
				res.Error = perr
			}
			if res.Error != nil {
				res.Output = nil
			}
			return
		}
		res.Steps = scope.thread.ExecutionSteps()
		res.Stdout = scope.redactedStdout()
		if perr := cancelledProgramErr(scope.ctx); perr != nil {
			res.Error = perr
		}
		if res.Error != nil {
			res.Output = nil
		}
		scope.session.Close()
		scope.session.ExecMu.Unlock()
		scope.finish()
	}()

	if perr := cancelledProgramErr(ctx); perr != nil {
		res.Error = perr
		return res
	}
	if program == nil || program.programs == nil {
		res.Error = invalidProgramf("program is not prepared")
		return res
	}
	if call.ID == "" {
		res.Error = invalidProgramf("call ID is required")
		return res
	}
	// Input validation and conversion run before any session work.
	if err := program.inputSchema.Validate(input); err != nil {
		res.Error = progErr("invalid_input", "input does not match the contract")
		return res
	}
	decoded, err := serving.DecodeJSON(input, program.limits)
	if err != nil {
		res.Error = progErr("invalid_input", "input is not valid bounded JSON")
		return res
	}
	arg, err := decodedJSONToStarlark(decoded)
	if err != nil {
		res.Error = progErr("invalid_input", "input is not representable as a JSON value")
		return res
	}
	if perr := cancelledProgramErr(ctx); perr != nil {
		res.Error = perr
		return res
	}

	var perr *ProgramError
	scope, perr = e.enterCall(ctx, call, deps, creation)
	if perr != nil {
		res.Error = perr
		return res
	}

	s := scope.session
	s.SetActor(call.ActorID, call.ActorEmail)
	s.ExecMu.Lock()

	// The fresh session must expose the same predeclared name set the
	// program was compiled against.
	if !slices.Equal(slices.Sorted(maps.Keys(s.Predeclared)), program.predeclared) {
		res.Error = invalidProgramf("session capabilities differ from the prepared program")
		return res
	}
	if err := seedAssets(scope.ctx, s, program.assets); err != nil {
		if perr := cancelledProgramErr(scope.ctx); perr != nil {
			res.Error = perr
		} else {
			res.Error = invalidProgramf("asset seeding failed")
		}
		return res
	}
	if perr := cancelledProgramErr(scope.ctx); perr != nil {
		res.Error = perr
		return res
	}

	globals, err := program.initModules(scope.thread, s.Predeclared)
	if err == nil {
		err = checkMain(globals)
	}
	if err != nil {
		res.Error = initFailureErr(scope.ctx, scope.thread, scope.maxSteps)
		return res
	}

	// Arm only around the main call.
	if perr := cancelledProgramErr(scope.ctx); perr != nil {
		res.Error = perr
		return res
	}
	s.Gate.Arm(s.execContext(call.ID))
	out, callErr := starlark.Call(scope.thread, globals["main"], starlark.Tuple{arg}, nil)
	s.Gate.Disarm()

	if callErr != nil {
		ce := classify(scope.ctx, callErr, scope.maxSteps, scope.thread.ExecutionSteps())
		if ce.Type == types.ErrSyntax {
			res.Error = invalidProgramf("program does not initialize")
		} else {
			res.Error = runtimeProgErr(ce.Type)
		}
		return res
	}
	res.Output = nil

	// Cancellation takes precedence over every conversion/validation
	// outcome -- a cancelled call never reports invalid_output.
	if perr := cancelledProgramErr(scope.ctx); perr != nil {
		res.Error = perr
		return res
	}
	fail := func(msg string) ProgramResult {
		if perr := cancelledProgramErr(scope.ctx); perr != nil {
			res.Error = perr
		} else {
			res.Error = progErr("invalid_output", msg)
		}
		return res
	}

	// Convert, redact, then validate the redacted output -- fail closed
	// as invalid_output, never return unredacted or unvalidated bytes.
	raw, err := MarshalJSONValue(out, program.limits)
	if err != nil {
		return fail("output is not a bounded JSON value")
	}
	red, err := scope.secrets.RedactBounded(scope.ctx, string(raw), program.limits.MaxBytes)
	if err != nil {
		return fail("output could not be safely redacted")
	}
	if _, err := serving.DecodeJSON([]byte(red), program.limits); err != nil {
		return fail("redacted output is not valid JSON")
	}
	if err := program.outputSchema.Validate([]byte(red)); err != nil {
		return fail("redacted output does not match the contract")
	}
	res.Output = json.RawMessage(red)
	return res
}

// programTimeoutContext applies the call deadline at the public entry
// point so preprocessing and execution share one budget.
func programTimeoutContext(ctx context.Context, call ProgramCall) (context.Context, context.CancelFunc) {
	timeout := call.Timeout
	if timeout <= 0 {
		timeout = defaultProgramTimeout
	}
	return context.WithTimeout(ctx, timeout)
}

// cancelledProgramErr maps a cancelled context cause to a ProgramError;
// nil while the context is still live.
func cancelledProgramErr(ctx context.Context) *ProgramError {
	cause := context.Cause(ctx)
	if cause == nil {
		return nil
	}
	if errors.Is(cause, ErrMemoryLimit) {
		return runtimeProgErr(types.ErrMemoryLimit)
	}
	return progErr("timeout", "execution timed out; the outcome may be unknown")
}

// initFailureErr maps a pure-init failure: context causes win
// (timeout/memory_limit), step exhaustion is step_limit, everything
// else is invalid_program.
func initFailureErr(ctx context.Context, thread *starlark.Thread, maxSteps uint64) *ProgramError {
	if perr := cancelledProgramErr(ctx); perr != nil {
		return perr
	}
	if maxSteps > 0 && thread.ExecutionSteps() >= maxSteps {
		return runtimeProgErr(types.ErrStepLimit)
	}
	return invalidProgramf("program does not initialize")
}

// callScope bundles the bounded execution state shared by prepare and
// invoke: the semaphore slot, the watchdog registry entry, the
// step-capped thread and the fresh session.
type callScope struct {
	ctx       context.Context
	thread    *starlark.Thread
	session   *Session
	secrets   *secrets.Set
	maxSteps  uint64
	outBound  int64
	stopAfter func() bool

	e          *Engine
	callID     string
	closed     bool
	registered bool // this scope owns running[callID]
}

// close releases everything enterCall acquired, idempotently: session
// close (gate revoked), the cancellation watcher stopped, the running
// registry entry dropped -- only when this scope owns it -- and the
// semaphore slot freed.
func (s *callScope) close() {
	if s.closed {
		return
	}
	s.closed = true
	if s.session != nil {
		s.session.Close()
	}
	s.release()
}

// finish drops the watcher, registry entry and slot after the caller
// has already closed the session under ExecMu.
func (s *callScope) finish() {
	if s.closed {
		return
	}
	s.closed = true
	s.release()
}

func (s *callScope) release() {
	if s.stopAfter != nil {
		s.stopAfter()
	}
	if s.registered {
		s.e.runningMu.Lock()
		delete(s.e.running, s.callID)
		s.e.runningMu.Unlock()
	}
	<-s.e.sem
}

// enterCall waits for an engine slot, registers the call for watchdog
// cancellation, builds the fresh session and returns the scope. The
// caller supplies an already-deadlined context and owns scope.close();
// failure paths inside enterCall clean up themselves.
func (e *Engine) enterCall(ctx context.Context, call ProgramCall, deps SessionDeps, creation SessionCreation) (*callScope, *ProgramError) {
	if call.ID == "" {
		return nil, invalidProgramf("call ID is required")
	}
	if deps.Registry == nil {
		return nil, invalidProgramf("session registry is required")
	}
	if perr := cancelledProgramErr(ctx); perr != nil {
		return nil, perr
	}
	select {
	case e.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, cancelledProgramErr(ctx)
	}
	scope := &callScope{e: e, callID: call.ID}
	if perr := cancelledProgramErr(ctx); perr != nil {
		scope.close()
		return nil, perr
	}
	ctx, cancel := context.WithCancelCause(ctx)
	scope.ctx = ctx

	e.runningMu.Lock()
	if _, exists := e.running[call.ID]; exists {
		e.runningMu.Unlock()
		cancel(errors.New("duplicate call id"))
		scope.close()
		return nil, invalidProgramf("call ID already in flight")
	}
	e.running[call.ID] = cancel
	scope.registered = true
	e.runningMu.Unlock()

	steps := creation.MaxSteps
	if steps <= 0 {
		steps = defaultProgramSteps
	}
	scope.maxSteps = steps
	thread := &starlark.Thread{Name: "program " + call.ID}
	thread.SetMaxExecutionSteps(steps)
	thread.SetLocal(capability.ContextKey, ctx)
	scope.thread = thread
	scope.stopAfter = context.AfterFunc(ctx, func() {
		cause := context.Cause(ctx)
		if cause == nil {
			cause = ctx.Err()
		}
		thread.Cancel(cause.Error())
	})

	cc := creation
	cc.MaxSteps = steps
	scope.outBound = cc.MaxOutputBytes
	if scope.outBound <= 0 {
		scope.outBound = defaultProgramOutput
	}
	cc.MaxOutputBytes = scope.outBound
	d := deps
	d.Context = ctx
	if perr := cancelledProgramErr(ctx); perr != nil {
		scope.close()
		return nil, perr
	}
	s, err := NewSession(d, cc)
	if err != nil {
		scope.close()
		if perr := cancelledProgramErr(ctx); perr != nil {
			return nil, perr
		}
		return nil, invalidProgramf("session could not be created")
	}
	scope.session = s
	if perr := cancelledProgramErr(ctx); perr != nil {
		scope.close()
		return nil, perr
	}
	scope.secrets = d.Secrets
	return scope, nil
}

// redactedStdout returns the session's bounded output buffer with
// known secrets removed. If redaction cannot stay within the
// configured bound the whole buffer is replaced by a fixed marker so
// nothing unredacted escapes.
func (s *callScope) redactedStdout() string {
	if s.session == nil || s.session.Out == nil {
		return ""
	}
	red, err := s.secrets.RedactBounded(s.ctx, s.session.Out.String(), int(s.outBound))
	if err != nil {
		marker := stdoutOmitted
		if int64(len(marker)) > s.outBound {
			marker = marker[:s.outBound]
		}
		return marker
	}
	return red
}

// initModules loads the entrypoint and everything it reaches via the
// compiled-program map with a fresh per-invocation globals cache; every
// selected source is initialized at most once. The gate stays whatever
// the caller left it -- callers run this disarmed.
func (p *Program) initModules(thread *starlark.Thread, pre starlark.StringDict) (starlark.StringDict, error) {
	modules := map[string]starlark.StringDict{}
	inited := map[string]bool{}
	thread.Load = func(t *starlark.Thread, module string) (starlark.StringDict, error) {
		if g, ok := modules[module]; ok {
			return g, nil
		}
		prog, ok := p.programs[module]
		if !ok {
			return nil, fmt.Errorf("load of unselected module")
		}
		if inited[module] {
			return nil, fmt.Errorf("load cycle in module graph")
		}
		inited[module] = true
		g, err := prog.Init(t, pre)
		if err != nil {
			return nil, err
		}
		modules[module] = g
		return g, nil
	}
	return thread.Load(thread, p.entrypoint)
}

// checkMain verifies the entrypoint globals define main as a function
// of exactly one positional parameter.
func checkMain(globals starlark.StringDict) error {
	v, ok := globals["main"]
	if !ok {
		return errors.New("entrypoint does not define main")
	}
	fn, ok := v.(*starlark.Function)
	if !ok {
		return errors.New("main is not a function")
	}
	if fn.NumParams() != 1 || fn.NumKwonlyParams() != 0 || fn.HasVarargs() || fn.HasKwargs() {
		return errors.New("main must take exactly one positional parameter")
	}
	return nil
}

// checkLoadGraph statically rejects missing, non-canonical or cyclic
// loads before any source is executed.
func checkLoadGraph(programs map[string]*starlark.Program) *ProgramError {
	for _, name := range slices.Sorted(maps.Keys(programs)) {
		pr := programs[name]
		for i := 0; i < pr.NumLoads(); i++ {
			module, _ := pr.Load(i)
			if err := checkReleaseName(module, true); err != nil {
				return invalidProgramf("invalid load in source %s", name)
			}
			if _, ok := programs[module]; !ok {
				return invalidProgramf("source %s loads an unselected module", name)
			}
		}
	}
	// Cycle check by DFS over the static load edges.
	const (
		white = iota
		gray
		black
	)
	color := map[string]int{}
	var visit func(string) *ProgramError
	visit = func(name string) *ProgramError {
		switch color[name] {
		case gray:
			return invalidProgramf("cyclic load in module graph")
		case black:
			return nil
		}
		color[name] = gray
		pr := programs[name]
		for i := 0; i < pr.NumLoads(); i++ {
			module, _ := pr.Load(i)
			if err := visit(module); err != nil {
				return err
			}
		}
		color[name] = black
		return nil
	}
	for _, name := range slices.Sorted(maps.Keys(programs)) {
		if err := visit(name); err != nil {
			return err
		}
	}
	return nil
}

// validateProgramConfig enforces all static bounds, compiles the two
// contract schemas and defensively clones the sources, assets and
// config into an immutable Program.
func validateProgramConfig(c ProgramConfig) (*Program, *ProgramError) {
	entry := c.Entrypoint
	if entry == "" {
		entry = "main.star"
	}
	if len(c.Sources) == 0 || len(c.Sources) > programMaxSources {
		return nil, invalidProgramf("sources must be 1..%d files", programMaxSources)
	}
	if len(c.Sources)+len(c.Assets) > programMaxFiles {
		return nil, invalidProgramf("sources and assets exceed %d files", programMaxFiles)
	}
	if err := checkReleaseName(entry, true); err != nil {
		return nil, invalidProgramf("invalid entrypoint name")
	}
	if _, ok := c.Sources[entry]; !ok {
		return nil, invalidProgramf("entrypoint is not among the selected sources")
	}

	seen := map[string]string{}
	all := map[string]bool{}
	var srcTotal, assetTotal int
	for name, src := range c.Sources {
		if err := checkReleaseName(name, true); err != nil {
			return nil, invalidProgramf("invalid source name")
		}
		if err := capfs.CheckExportPath("/work/"+name, seen); err != nil {
			return nil, invalidProgramf("non-portable source path")
		}
		if all[name] {
			return nil, invalidProgramf("duplicate source/asset path")
		}
		all[name] = true
		if len(src) > programMaxFileBytes {
			return nil, invalidProgramf("source exceeds per-file byte limit")
		}
		srcTotal += len(src)
	}
	for name, a := range c.Assets {
		if err := checkReleaseName(name, false); err != nil {
			return nil, invalidProgramf("invalid asset name")
		}
		if err := capfs.CheckExportPath("/work/"+name, seen); err != nil {
			return nil, invalidProgramf("non-portable asset path")
		}
		if all[name] {
			return nil, invalidProgramf("duplicate source/asset path")
		}
		all[name] = true
		if len(a) > programMaxFileBytes {
			return nil, invalidProgramf("asset exceeds per-file byte limit")
		}
		assetTotal += len(a)
	}
	if srcTotal > programMaxSource {
		return nil, invalidProgramf("sources exceed total byte limit")
	}
	if assetTotal > programMaxAssetBytes {
		return nil, invalidProgramf("assets exceed total byte limit")
	}
	// No file may be an ancestor of another file.
	for name := range all {
		for d := path.Dir(name); d != "." && d != "/"; d = path.Dir(d) {
			if all[d] {
				return nil, invalidProgramf("file path is an ancestor of another file")
			}
		}
	}

	if len(c.InputSchema) == 0 {
		return nil, invalidProgramf("input_schema is required")
	}
	inSchema, err := serving.CompileSchema(c.InputSchema, c.JSONLimits)
	if err != nil {
		return nil, invalidProgramf("invalid input_schema")
	}
	if len(c.OutputSchema) == 0 {
		return nil, invalidProgramf("output_schema is required")
	}
	outSchema, err := serving.CompileSchema(c.OutputSchema, c.JSONLimits)
	if err != nil {
		return nil, invalidProgramf("invalid output_schema")
	}

	prog := &Program{
		entrypoint:   entry,
		sources:      make(map[string][]byte, len(c.Sources)),
		assets:       make(map[string][]byte, len(c.Assets)),
		inputSchema:  inSchema,
		outputSchema: outSchema,
		limits:       c.JSONLimits.Effective(),
	}
	for name, src := range c.Sources {
		prog.sources[name] = slices.Clone(src)
	}
	for name, a := range c.Assets {
		prog.assets[name] = slices.Clone(a)
	}
	return prog, nil
}

// checkReleaseName enforces the canonical root-relative portable path
// rules shared by source and asset names.
func checkReleaseName(name string, star bool) error {
	if name == "" || len(name) > programMaxPathLen {
		return errors.New("invalid path length")
	}
	if path.IsAbs(name) || strings.Contains(name, "\\") || path.Clean(name) != name {
		return errors.New("non-canonical path")
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." {
			return errors.New("non-canonical path")
		}
	}
	if star && !strings.HasSuffix(name, ".star") {
		return errors.New("source path must end in .star")
	}
	return nil
}

// seedAssets writes the release assets into the fresh session's VFS.
// This is trusted bootstrap content under the disarmed gate, not a
// script operation, so it bypasses audit intentionally.
func seedAssets(ctx context.Context, s *Session, assets map[string][]byte) error {
	if len(assets) == 0 {
		return nil
	}
	closer, ok := s.Closers[types.CapFS].(*capfs.Closer)
	if !ok || closer == nil || closer.V == nil {
		return errors.New("assets require the fs capability")
	}
	for _, name := range slices.Sorted(maps.Keys(assets)) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := closer.V.Write(name, string(assets[name]), false); err != nil {
			return errors.New("asset write failed")
		}
	}
	return nil
}

// decodedJSONToStarlark converts a decoded JSON tree into a frozen
// Starlark value. Integral numbers keep full precision as big ints;
// ordinary fractions become finite float64, and out-of-range fractions
// are rejected before any user code runs -- the normal language
// behavior for numeric inputs.
func decodedJSONToStarlark(v any) (starlark.Value, error) {
	switch t := v.(type) {
	case nil:
		return starlark.None, nil
	case bool:
		return starlark.Bool(t), nil
	case string:
		return starlark.String(t), nil
	case json.Number:
		r, ok := new(big.Rat).SetString(t.String())
		if !ok {
			return nil, errors.New("invalid number")
		}
		if r.IsInt() {
			return starlark.MakeBigInt(new(big.Int).Set(r.Num())), nil
		}
		f, err := strconv.ParseFloat(t.String(), 64)
		if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
			return nil, errors.New("number out of range")
		}
		return starlark.Float(f), nil
	case []any:
		elems := make([]starlark.Value, len(t))
		for i, e := range t {
			sv, err := decodedJSONToStarlark(e)
			if err != nil {
				return nil, err
			}
			elems[i] = sv
		}
		l := starlark.NewList(elems)
		l.Freeze()
		return l, nil
	case map[string]any:
		d := starlark.NewDict(len(t))
		for k, e := range t {
			sv, err := decodedJSONToStarlark(e)
			if err != nil {
				return nil, err
			}
			if err := d.SetKey(starlark.String(k), sv); err != nil {
				return nil, err
			}
		}
		d.Freeze()
		return d, nil
	default:
		return nil, errors.New("unsupported JSON value")
	}
}
