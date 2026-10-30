package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
)

// serving.go is the transport contract for EP-0014 ephemeral serving
// replicas. A replica is a dedicated, leased node object hosting one
// compiled release program; it shares the instance identifier
// namespace but is not a development instance and carries no REPL
// state.
//
// This file is types only -- it must not import the engine, Starlark,
// store or serving packages.

// ProgramBundle is the immutable, release-shaped program payload a
// prepare carries. Bounds are fixed engine defaults; there is no
// client-tunable JSON limit field. This is not yet a full
// content-hashed, spec-pinned release record.
type ProgramBundle struct {
	Entrypoint string            `json:"entrypoint,omitempty"`
	Sources    map[string][]byte `json:"sources"`
	Assets     map[string][]byte `json:"assets,omitempty"`
	// The two contract schemas are publish-time release metadata, not
	// declared inside the Starlark sources.
	InputSchema  json.RawMessage `json:"input_schema"`
	OutputSchema json.RawMessage `json:"output_schema"`
	// Handlers declares the named handler entries a run may dispatch
	// to, each with an optional context schema.
	Handlers map[string]HandlerSpec `json:"handlers,omitempty"`
}

// HandlerSpec declares one interrupt handler: the function name (the
// map key) and the schema its stored context must satisfy.
type HandlerSpec struct {
	ContextSchema json.RawMessage `json:"context_schema,omitempty"`
}

// PrepareRequest asks a node to compile, pure-init and register an
// ephemeral serving replica. InstanceID is the unique ephemeral
// replica ID -- not a development or persistent logical ID -- minted
// by the API tier. The embedded CreateRequest fields carry the resolved
// spec, policies, in-memory secret values, labels, owner, epoch and
// lease ExpiresAt exactly as a Create does; resolved secret values
// live in memory only and are never persisted by the node.
type PrepareRequest struct {
	CreateRequest
	ReleaseID  string        `json:"release_id"`
	Generation int64         `json:"generation"`
	Program    ProgramBundle `json:"program"`
	// PrepareID uniquely identifies this prepare call (the engine
	// call ID); a failed prepare leaves no usable replica and the
	// same replica ID may be retried under a new PrepareID.
	PrepareID string `json:"prepare_id"`
	TimeoutMs int64  `json:"timeout_ms,omitempty"`
}

// ProgramError is a classified program failure; codes are the engine's
// stable set: invalid_program, invalid_input, invalid_output, runtime,
// timeout, step_limit, memory_limit, policy_denied, out_of_scope.
type ProgramError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ProgramResult is one program call outcome. Output is set only on
// success; Stdout is bounded and secret-redacted.
type ProgramResult struct {
	ID         string          `json:"id"`
	Output     json.RawMessage `json:"output,omitempty"`
	Stdout     string          `json:"stdout,omitempty"`
	Error      *ProgramError   `json:"error,omitempty"`
	Waiting    *ProgramWaiting `json:"waiting,omitempty"`
	DurationMs int64           `json:"duration_ms"`
	Steps      uint64          `json:"steps"`
}

// ProgramWaiting reports that a step ended on interrupt.wait: the
// reserved interrupt id, the handler to dispatch on resolution, the
// optional on_expire handler, and the stored context. Context carries
// program data for the next step and is never logged.
type ProgramWaiting struct {
	InterruptID string          `json:"interrupt_id"`
	Handler     string          `json:"handler"`
	OnExpire    string          `json:"on_expire,omitempty"`
	Context     json.RawMessage `json:"context"`
	Kind        string          `json:"kind"`
	Title       string          `json:"title,omitempty"`
	Assignees   []string        `json:"assignees,omitempty"`
	EventSchema json.RawMessage `json:"event_schema,omitempty"`
	ExpiresInS  int64           `json:"expires_in_s,omitempty"`
	FireInS     int64           `json:"fire_in_s,omitempty"`
}

// InterruptSlot is a pre-reserved interrupt identity the API mints and
// hands to a step; interrupt.new claims the next unused slot. Token is
// the callback bearer credential: a secret, never logged.
type InterruptSlot struct {
	ID          string `json:"id"`
	ConsoleURL  string `json:"console_url"`
	CallbackURL string `json:"callback_url"`
	Token       string `json:"token"`
}

// PrepareResponse reports the prepare outcome. Result success implies
// the replica is registered and ready; a failed Result means no usable
// replica exists. Audit is always returned, including on known engine
// prepare failures, and may be empty for pre-engine rejections.
type PrepareResponse struct {
	Result ProgramResult `json:"result"`
	Audit  AuditBatch    `json:"audit"`
	NodeID string        `json:"node_id,omitempty"`
	Epoch  int64         `json:"epoch,omitempty"`
}

// InvokeRequest is a fenced call against a ready replica. All
// identifiers are required; Actor falls back to Owner when empty.
// ReleaseID, Generation and Epoch must exactly match the prepared
// replica -- nothing falls back to latest, and there are no
// transparent retries or invoke dedup: the caller mints a fresh
// InvokeID for every attempt.
type InvokeRequest struct {
	InstanceID string          `json:"instance_id"`
	Owner      Owner           `json:"owner"`
	Actor      Owner           `json:"actor,omitempty"`
	ReleaseID  string          `json:"release_id"`
	Generation int64           `json:"generation"`
	Epoch      int64           `json:"epoch"`
	InvokeID   string          `json:"invoke_id"`
	Input      json.RawMessage `json:"input"`
	TimeoutMs  int64           `json:"timeout_ms,omitempty"`
	// Entry selects the entry function: "" or "main" is main with the
	// input schema; otherwise a declared handler name and Input is the
	// full handler-context JSON object built by the API.
	Entry string `json:"entry,omitempty"`
	// RunID, Step and Interrupts carry the run's identity and the
	// pre-reserved interrupt slots interrupt.new may claim this step.
	RunID      string          `json:"run_id,omitempty"`
	Step       int             `json:"step,omitempty"`
	Interrupts []InterruptSlot `json:"interrupts,omitempty"`
}

// InvokeResponse reports one invoke. Audit carries this call's records
// only and is present on all engine-level outcomes.
type InvokeResponse struct {
	Result ProgramResult `json:"result"`
	Audit  AuditBatch    `json:"audit"`
}

// --- Log discipline -------------------------------------------------
// Program bundles, schemas, inputs and resolved secret values must
// never appear in logs. String and LogValue emit request metadata
// only; sizes are byte/file counts, never contents.

// String renders bundle metadata only.
func (b ProgramBundle) String() string {
	return fmt.Sprintf("program{entrypoint=%s sources=%d source_bytes=%d assets=%d asset_bytes=%d}",
		b.Entrypoint, len(b.Sources), byteLen(b.Sources), len(b.Assets), byteLen(b.Assets))
}

// LogValue renders bundle metadata only.
func (b ProgramBundle) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("entrypoint", b.Entrypoint),
		slog.Int("sources", len(b.Sources)),
		slog.Int("source_bytes", byteLen(b.Sources)),
		slog.Int("assets", len(b.Assets)),
		slog.Int("asset_bytes", byteLen(b.Assets)),
	)
}

func byteLen(m map[string][]byte) int {
	n := 0
	for _, v := range m {
		n += len(v)
	}
	return n
}

// String renders prepare metadata only. It deliberately does not defer
// to the embedded CreateRequest's formatter and never names program
// contents or secret values.
func (r PrepareRequest) String() string {
	return fmt.Sprintf("prepare{instance=%s prepare=%s release=%s gen=%d owner=%s secrets=%d %s}",
		r.InstanceID, r.PrepareID, r.ReleaseID, r.Generation, r.Owner.UserID, len(r.Secrets), r.Program)
}

// LogValue overrides the embedded CreateRequest logger with serving
// metadata only.
func (r PrepareRequest) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("instance_id", r.InstanceID),
		slog.String("prepare_id", r.PrepareID),
		slog.String("release_id", r.ReleaseID),
		slog.Int64("generation", r.Generation),
		slog.String("user_id", r.Owner.UserID),
		slog.Int("secrets", len(r.Secrets)),
		slog.Group("program",
			slog.String("entrypoint", r.Program.Entrypoint),
			slog.Int("sources", len(r.Program.Sources)),
			slog.Int("source_bytes", byteLen(r.Program.Sources)),
			slog.Int("assets", len(r.Program.Assets)),
			slog.Int("asset_bytes", byteLen(r.Program.Assets))),
	)
}

// String renders invoke metadata only -- never the input body,
// handler context or interrupt tokens.
func (r InvokeRequest) String() string {
	return fmt.Sprintf("invoke{instance=%s invoke=%s release=%s gen=%d epoch=%d owner=%s actor=%s input_bytes=%d entry=%s run_id=%s step=%d interrupts=%d}",
		r.InstanceID, r.InvokeID, r.ReleaseID, r.Generation, r.Epoch, r.Owner.UserID, r.Actor.UserID, len(r.Input),
		r.Entry, r.RunID, r.Step, len(r.Interrupts))
}

// LogValue renders invoke metadata only.
func (r InvokeRequest) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("instance_id", r.InstanceID),
		slog.String("invoke_id", r.InvokeID),
		slog.String("release_id", r.ReleaseID),
		slog.Int64("generation", r.Generation),
		slog.Int64("epoch", r.Epoch),
		slog.String("user_id", r.Owner.UserID),
		slog.String("actor_id", r.Actor.UserID),
		slog.Int("input_bytes", len(r.Input)),
		slog.String("entry", r.Entry),
		slog.String("run_id", r.RunID),
		slog.Int("step", r.Step),
		slog.Int("interrupts", len(r.Interrupts)),
	)
}

// ServingRuntime is the EP-0014 replica surface: prepare and invoke
// carry no dedup and no transparent retry; Runtime embeds it, and the
// small interface stays useful for test fixtures.
type ServingRuntime interface {
	// Prepare compiles, pure-initializes and registers an ephemeral
	// replica. The request is self-contained: resolved spec, policy
	// snapshot, in-memory secrets and a finite lease. A failed result
	// means no usable replica was registered; audit is always carried
	// on the 200 response.
	Prepare(ctx context.Context, req *PrepareRequest) (*PrepareResponse, error)
	// Invoke runs one fenced call against a ready replica. There are
	// no transparent retries and no dedup: the caller mints a fresh
	// InvokeID per attempt.
	Invoke(ctx context.Context, req *InvokeRequest) (*InvokeResponse, error)
}
