package store

import (
	"context"
	"time"
)

// EP-0016 application runs: a run is a chain of short, independent
// steps (main plus handler calls). Interrupts are the durable handoff
// between steps; ciphertext bodies (context, event, output) are opaque
// to the store — the service seals and opens them.
const (
	PrefixRun       IDPrefix = "run_"
	PrefixInterrupt IDPrefix = "int_"
)

// Run triggers.
const (
	RunTriggerTest   = "test"
	RunTriggerInvoke = "invoke"
)

// Run statuses.
const (
	RunRunning   = "running"
	RunWaiting   = "waiting"
	RunSucceeded = "succeeded"
	RunFailed    = "failed"
	RunExpired   = "expired"
	RunCancelled = "cancelled"
	RunUnknown   = "unknown"
)

// Step statuses.
const (
	StepRunning   = "running"
	StepSucceeded = "succeeded"
	StepFailed    = "failed"
	StepWaiting   = "waiting"
	StepUnknown   = "unknown"
)

// Interrupt statuses.
const (
	InterruptSlotReserved = "reserved"
	InterruptPending      = "pending"
	InterruptResolved     = "resolved"
	InterruptExpired      = "expired"
	InterruptCancelled    = "cancelled"
)

// Interrupt dispatch states ("" = idle).
const (
	DispatchQueued  = "queued"
	DispatchRunning = "running"
	DispatchDone    = "done"
)

// Interrupt resolver identities.
const (
	ResolvedByToken  = "token" // the interrupt's bound resume token
	ResolvedByExpiry = "expiry"
)

// Run is one application execution from main to a terminal outcome.
// Output is service-sealed ciphertext; the store treats it as opaque.
type Run struct {
	ID            string `json:"id"`
	WorkspaceID   string `json:"workspace_id"`
	ApplicationID string `json:"application_id"`
	ReleaseID     string `json:"release_id"`
	UserID        string `json:"user_id"`
	Trigger       string `json:"trigger"`
	Status        string `json:"status"`
	// Steps counts distinct steps started; retries of a step do not.
	Steps       int        `json:"steps"`
	InterruptID string     `json:"interrupt_id,omitempty"`
	ErrorCode   string     `json:"error_code,omitempty"`
	Output      []byte     `json:"-"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	EndedAt     *time.Time `json:"ended_at,omitempty"`
}

// RunStep is one call of an entry function. The primary key is
// (run_id, seq, attempt); seq 0 is main.
type RunStep struct {
	RunID string `json:"run_id"`
	Seq   int    `json:"seq"`
	// Attempt is 1-based; a retried handler call reuses seq with
	// attempt + 1.
	Attempt     int        `json:"attempt"`
	Entry       string     `json:"entry"` // "main" or a handler name
	InterruptID string     `json:"interrupt_id,omitempty"`
	Status      string     `json:"status"`
	ErrorCode   string     `json:"error_code,omitempty"`
	DurationMs  int64      `json:"duration_ms"`
	StartedAt   time.Time  `json:"started_at"`
	EndedAt     *time.Time `json:"ended_at,omitempty"`
}

// Interrupt is one durable request for exactly one external
// resolution. Context/Event are service-sealed ciphertext.
type Interrupt struct {
	ID            string     `json:"id"`
	WorkspaceID   string     `json:"workspace_id"`
	ApplicationID string     `json:"application_id"`
	RunID         string     `json:"run_id"`
	StepSeq       int        `json:"step_seq"`
	Status        string     `json:"status"`
	Handler       string     `json:"handler,omitempty"`
	OnExpire      string     `json:"on_expire,omitempty"`
	Context       []byte     `json:"-"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	// Event holds the winning resolution — also for an early
	// (reserved-state) resolution, stored until commit decides.
	Event      []byte     `json:"-"`
	ResolvedBy string     `json:"resolved_by,omitempty"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
	// KeyID is the bound resume API key id.
	KeyID                string     `json:"key_id,omitempty"`
	DispatchState        string     `json:"dispatch_state,omitempty"`
	DispatchAfter        *time.Time `json:"dispatch_after,omitempty"`
	DispatchClaimedUntil *time.Time `json:"dispatch_claimed_until,omitempty"`
	DispatchAttempts     int        `json:"dispatch_attempts,omitempty"`
	// Attempt is the handler attempt counter, starting at 1 when the
	// interrupt is queued for dispatch.
	Attempt   int       `json:"attempt,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// RunFilter scopes ListRuns; WorkspaceID is required.
type RunFilter struct {
	WorkspaceID   string
	ApplicationID string
	Status        string
	// Before is the run id cursor of the last row of the previous
	// page (newest-first ordering).
	Before string
	// Limit is 1..200; 0 selects the default 50.
	Limit int
}

// InterruptFilter scopes ListInterrupts; WorkspaceID is required.
type InterruptFilter struct {
	WorkspaceID   string
	Status        string
	RunID         string
	ApplicationID string
	Before        string
	Limit         int
}

// WaitCommit carries the wait() payload that turns a reserved slot
// into a committed interrupt.
type WaitCommit struct {
	InterruptID string
	Handler     string
	OnExpire    string
	Context     []byte
	ExpiresAt   *time.Time
}

// StepOutcome is the atomic end-of-step transition written by
// FinishStep.
type StepOutcome struct {
	RunID      string
	Seq        int
	Attempt    int
	Status     string // succeeded|failed|waiting|unknown
	ErrorCode  string
	DurationMs int64
	// Output is the run output ciphertext when Status == succeeded.
	Output []byte
	// Wait is required iff Status == waiting.
	Wait *WaitCommit
	// DispatchedInterruptID names the interrupt whose handler
	// dispatch this step served; its dispatch_state becomes done for
	// every non-unknown outcome.
	DispatchedInterruptID string
	Now                   time.Time
}

// RunStore persists application runs, steps and interrupts. Every
// compound method is a single transaction; transitions are
// compare-and-swap on the current status.
type RunStore interface {
	// StartRun inserts a running run, its first running step and the
	// step's reserved interrupt slots atomically.
	StartRun(ctx context.Context, run *Run, step *RunStep, slots []*Interrupt) error
	GetRun(ctx context.Context, id string) (*Run, error)
	ListRuns(ctx context.Context, f RunFilter) ([]*Run, error)
	ListRunSteps(ctx context.Context, runID string) ([]*RunStep, error)

	GetInterrupt(ctx context.Context, id string) (*Interrupt, error)
	ListInterrupts(ctx context.Context, f InterruptFilter) ([]*Interrupt, error)

	// FinishStep commits a step outcome: the step must be running,
	// unused reserved siblings are cancelled (and their bound keys
	// revoked), and the run/interrupt transitions of the outcome
	// apply atomically. acceptEarly decides whether an event that
	// arrived while the slot was reserved counts as the resolution.
	FinishStep(ctx context.Context, out StepOutcome, acceptEarly func(event []byte) bool) error
	// ResolveInterrupt records the single resolution of a pending or
	// reserved interrupt; first arriver wins. early reports that the
	// interrupt is still reserved (resolution stored for commit).
	ResolveInterrupt(ctx context.Context, id string, event []byte, resolvedBy string, now time.Time) (early bool, err error)

	// ListDueInterrupts returns pending interrupts whose expires_at
	// is at/before now.
	ListDueInterrupts(ctx context.Context, now time.Time, limit int) ([]*Interrupt, error)
	// ExpireInterrupt CAS-transitions a due pending interrupt to
	// expired; it queues the on_expire dispatch or ends
	// the run expired. Reports whether this call committed.
	ExpireInterrupt(ctx context.Context, id string, now time.Time) (bool, error)

	// ClaimDispatch atomically claims one queued interrupt whose
	// dispatch_after is due, moving it to running until claimUntil.
	ClaimDispatch(ctx context.Context, now, claimUntil time.Time) (*Interrupt, error)
	// DeferDispatch returns a running dispatch to queued, bumping
	// dispatch_attempts (the step never started).
	DeferDispatch(ctx context.Context, id string, after time.Time) error
	// StartHandlerStep moves a claimed interrupt's run from waiting
	// back to running and inserts the new step plus its reserved
	// slots.
	StartHandlerStep(ctx context.Context, interruptID string, step *RunStep, slots []*Interrupt, now time.Time) error
	// ListStaleDispatches lists running dispatches whose claim
	// lapsed.
	ListStaleDispatches(ctx context.Context, now time.Time, limit int) ([]*Interrupt, error)
	// RecoverStaleDispatch resolves a stale claim: a step that
	// started goes unknown with its run (dispatch freed for explicit
	// retry), a never-started dispatch is requeued. started reports
	// which branch ran.
	RecoverStaleDispatch(ctx context.Context, id string, now time.Time) (started bool, err error)

	// CancelRun cancels a waiting or unknown run, its pending
	// interrupt and any queued dispatch, revoking all run keys.
	CancelRun(ctx context.Context, runID string, now time.Time) (bool, error)
	// RetryRun re-queues a handler step whose run went unknown; a
	// main-step unknown has no interrupt to retry.
	RetryRun(ctx context.Context, runID string, now time.Time) (bool, error)
}
