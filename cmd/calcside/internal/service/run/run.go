// Package run implements the EP-0016 application run lifecycle:
// begin/complete step transitions over the durable RunStore, plus the
// background dispatcher that drives due expirations, stale-dispatch
// recovery and handler dispatch. All ciphertext (run output, interrupt
// context, interrupt event) is sealed with the vault cipher; without a
// cipher runs record but waits fail with interrupts_unavailable.
package run

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"calcside/cmd/calcside/internal/auth"
	"calcside/cmd/calcside/internal/service"
	"calcside/cmd/calcside/internal/service/access"
	"calcside/cmd/calcside/internal/service/sandbox"
	"calcside/cmd/calcside/internal/vaultcipher"
	"calcside/internal/runtime"
	"calcside/internal/serving"
	"calcside/internal/store"
	"calcside/internal/types"
)

// Tunables.
const (
	Slots               = 4               // pre-reserved interrupt slots per step
	defaultMaxSteps     = 64              // run chain limit
	DispatchConcurrency = 4               // claimed interrupts per tick
	Tick                = time.Second     // dispatcher cadence
	ClaimFor            = 2 * time.Minute // dispatch claim lease
	keySafetyDays       = 31              // bound-key expiry safety cap
	defaultExpireS      = 7 * 86400       // default interrupt deadline
	prepareLease        = 45 * time.Second
)

// AuditSink persists workspace audit batches (the application service's
// shape).
type AuditSink interface {
	RecordWorkspace(workspaceID string, b runtime.AuditBatch)
}

type nopAudit struct{}

func (nopAudit) RecordWorkspace(string, runtime.AuditBatch) {}

// Options are the run service's collaborators.
type Options struct {
	Store   store.Store
	Runtime runtime.Runtime
	Access  *access.Checker
	Sandbox *sandbox.Service // release resolution (spec/policies/secrets)
	Audit   AuditSink
	// Cipher seals outputs, contexts and events; nil disables them.
	Cipher        *vaultcipher.Cipher
	ConsoleOrigin string
	Now           func() time.Time
	// MaxSteps bounds the step chain (0 = defaultMaxSteps).
	MaxSteps int
}

type Service struct {
	o    Options
	stop chan struct{}
	done chan struct{}
	wake chan struct{} // buffered(1); a fresh resolution nudges the loop
}

func New(o Options) (*Service, error) {
	if o.Store == nil || o.Runtime == nil || o.Sandbox == nil || o.Access == nil {
		return nil, errors.New("run: missing dependency")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Audit == nil {
		o.Audit = nopAudit{}
	}
	if o.MaxSteps <= 0 {
		o.MaxSteps = defaultMaxSteps
	}
	return &Service{o: o, stop: make(chan struct{}), done: make(chan struct{}), wake: make(chan struct{}, 1)}, nil
}

// --- sealing helpers -------------------------------------------------

func (s *Service) seal(pt []byte, aad string) []byte {
	if s.o.Cipher == nil || len(pt) == 0 {
		return nil
	}
	b, err := s.o.Cipher.Seal(pt, aad)
	if err != nil {
		return nil
	}
	return b
}

func (s *Service) open(ct []byte, aad string) ([]byte, error) {
	if s.o.Cipher == nil || len(ct) == 0 {
		return nil, errors.New("run: ciphertext unavailable")
	}
	return s.o.Cipher.Open(ct, aad)
}

func (s *Service) sealOK(ct []byte, aad string) ([]byte, error) {
	if s.o.Cipher == nil {
		return nil, errors.New("interrupts unavailable")
	}
	return s.o.Cipher.Seal(ct, aad)
}

// --- run begin -------------------------------------------------------

// Started is the begin result: the stored run and the wire slots the
// invoke request carries.
type Started struct {
	Run        *store.Run
	Interrupts []runtime.InterruptSlot
}

// Begin records a running run, its first step and the step's reserved
// interrupt slots with bound resume keys. Programs that never reference
// `interrupt`, and servers without a cipher, get no slots.
func (s *Service) Begin(ctx context.Context, app *store.Application, rel *store.ServingRelease, trigger string, actor service.Actor) (*Started, error) {
	now := s.o.Now().UTC()
	run := &store.Run{
		ID: store.NewID(store.PrefixRun), WorkspaceID: app.WorkspaceID,
		ApplicationID: app.ID, ReleaseID: rel.ID, UserID: actor.UserID,
		Trigger: trigger, Status: store.RunRunning, Steps: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	step := &store.RunStep{
		RunID: run.ID, Seq: 0, Attempt: 1, Entry: "main",
		Status: store.StepRunning, StartedAt: now,
	}
	var slots []*store.Interrupt
	var wire []runtime.InterruptSlot
	if s.o.Cipher != nil && serving.UsesInterrupt(rel.Program.Sources) {
		var err error
		slots, wire, err = s.mintSlots(ctx, app, run.ID, 0, now)
		if err != nil {
			return nil, err
		}
	}
	if err := s.o.Store.StartRun(ctx, run, step, slots); err != nil {
		return nil, service.Internal(err)
	}
	return &Started{Run: run, Interrupts: wire}, nil
}

// mintSlots creates Slots reserved interrupts for one step, each with a
// fresh API key bound to its int_ id, and returns both the stored rows
// and the wire slots (with plaintext tokens).
func (s *Service) mintSlots(ctx context.Context, app *store.Application, runID string, stepSeq int, now time.Time) ([]*store.Interrupt, []runtime.InterruptSlot, error) {
	keyExp := now.Add(keySafetyDays * 24 * time.Hour)
	var slots []*store.Interrupt
	var wire []runtime.InterruptSlot
	for i := 0; i < Slots; i++ {
		iid := store.NewID(store.PrefixInterrupt)
		secret, key := auth.NewAPIKey(app.UserID, "interrupt "+iid, &keyExp)
		key.BoundID = iid
		key.WorkspaceID = app.WorkspaceID
		if err := s.o.Store.CreateAPIKey(ctx, key); err != nil {
			return nil, nil, service.Internal(err)
		}
		slots = append(slots, &store.Interrupt{
			ID: iid, WorkspaceID: app.WorkspaceID, ApplicationID: app.ID,
			RunID: runID, StepSeq: stepSeq, Status: store.InterruptSlotReserved,
			KeyID: key.ID, CreatedAt: now, UpdatedAt: now,
		})
		wire = append(wire, runtime.InterruptSlot{
			ID:        iid,
			ResumeURL: s.resumeURL(iid),
			Token:     secret,
		})
	}
	return slots, wire, nil
}

// --- step completion -------------------------------------------------

// Outcome is the completed step's run state for the wire.
type Outcome struct {
	RunID     string
	Status    string
	Run       *store.Run
	Waiting   *WaitingRef
	OutputRaw []byte // plaintext run output when succeeded
}

// WaitingRef is the wire-visible handle of a committed interrupt.
type WaitingRef struct {
	InterruptID string `json:"interrupt_id"`
	ConsoleURL  string `json:"console_url"`
}

// Complete classifies an invoke outcome and commits it atomically:
// waiting commits the reserved slot, succeeded seals the output,
// timeout/transport records unknown, everything else fails.
func (s *Service) Complete(ctx context.Context, st *Started, seq, attempt int, entry, dispatchedInterruptID string, resp *runtime.InvokeResponse, invokeErr error) (*Outcome, error) {
	now := s.o.Now().UTC()
	out := store.StepOutcome{
		RunID: st.Run.ID, Seq: seq, Attempt: attempt, Now: now,
		DispatchedInterruptID: dispatchedInterruptID,
	}
	var res *runtime.ProgramResult
	if resp != nil {
		res = &resp.Result
	}
	switch {
	case invokeErr != nil || res == nil:
		out.Status = store.StepUnknown
	case res.Waiting != nil:
		// Chain limit: a waiting step that exhausts the step budget
		// fails instead of queuing another interrupt.
		if st.Run.Steps >= s.o.MaxSteps {
			out.Status = store.StepFailed
			out.ErrorCode = "chain_limit"
			break
		}
		if s.o.Cipher == nil {
			out.Status = store.StepFailed
			out.ErrorCode = "interrupts_unavailable"
			break
		}
		w := res.Waiting
		ctxSeal, err := s.sealOK(w.Context, "interrupt-context:"+w.InterruptID)
		if err != nil {
			out.Status = store.StepFailed
			out.ErrorCode = "interrupts_unavailable"
			break
		}
		wc := &store.WaitCommit{
			InterruptID: w.InterruptID,
			Handler:     w.Handler, OnExpire: w.OnExpire, Context: ctxSeal,
		}
		if w.ExpiresInS > 0 {
			t := now.Add(time.Duration(w.ExpiresInS) * time.Second)
			wc.ExpiresAt = &t
		}
		if wc.ExpiresAt == nil {
			t := now.Add(defaultExpireS * time.Second)
			wc.ExpiresAt = &t
		}
		out.Status = store.StepWaiting
		out.Wait = wc
	case res.Error != nil:
		if res.Error.Code == "timeout" {
			out.Status = store.StepUnknown
		} else {
			out.Status = store.StepFailed
			out.ErrorCode = res.Error.Code
		}
	default:
		out.Status = store.StepSucceeded
		if res.Output != nil {
			out.Output = s.seal(res.Output, "run:"+st.Run.ID)
		}
	}
	if err := s.o.Store.FinishStep(ctx, out, s.acceptEarlyFor(out.Wait)); err != nil {
		return nil, service.Internal(err)
	}
	if out.Status == store.StepWaiting {
		// An early (reserved-state) resolution accepted at commit is
		// already queued for dispatch — don't wait for the tick.
		s.nudge()
	}
	run, err := s.o.Store.GetRun(ctx, st.Run.ID)
	if err != nil {
		return nil, service.Internal(err)
	}
	oc := &Outcome{RunID: run.ID, Status: run.Status, Run: run}
	if out.Status == store.StepWaiting && out.Wait != nil {
		oc.Waiting = &WaitingRef{
			InterruptID: out.Wait.InterruptID,
			ConsoleURL:  s.o.ConsoleOrigin + "/inbox?interrupt=" + out.Wait.InterruptID,
		}
	}
	if run.Status == store.RunSucceeded {
		pt, err := s.open(run.Output, "run:"+run.ID)
		if err == nil {
			oc.OutputRaw = pt
		}
	}
	return oc, nil
}

// acceptEarlyFor decides inside the FinishStep transaction whether an
// event stored while the slot was reserved counts as the resolution:
// events that decrypt and validate as a JSON object. The stored event
// is sealed under the interrupt id.
func (s *Service) acceptEarlyFor(w *store.WaitCommit) func([]byte) bool {
	return func(event []byte) bool {
		if s.o.Cipher == nil || w == nil {
			return false
		}
		pt, err := s.open(event, "interrupt-event:"+w.InterruptID)
		if err != nil {
			return false
		}
		return s.validateEvent(pt) == nil
	}
}

// eventSchema is the only event contract: any bounded JSON object.
var eventSchema = json.RawMessage(`{"type":"object","additionalProperties":true}`)

// validateEvent bounds and shape-checks a plaintext event body.
func (s *Service) validateEvent(event []byte) error {
	sch, err := serving.CompileSchema(eventSchema, serving.DefaultLimits())
	if err != nil {
		return err
	}
	return sch.Validate(event)
}

// auditEvent records a synthetic workspace capability event for a
// run/interrupt transition; args carry ids and kind only, never
// contexts or event bodies.
func (s *Service) auditEvent(ws, userID, runID, op, reason string) {
	s.o.Audit.RecordWorkspace(ws, runtime.AuditBatch{Events: []runtime.AuditEvent{{
		Ts: s.o.Now().UTC(), UserID: userID, InstanceID: runID,
		Capability: types.CapInterrupt, Op: types.Op(op),
		Phase: types.PhaseAfter, Decision: types.DecisionAllow, Reason: reason,
	}}})
}
