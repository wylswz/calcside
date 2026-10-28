package instance

import (
	"context"
	"errors"
	"maps"
	"math"
	"sync"
	"time"

	"calcside/internal/capability"
	"calcside/internal/engine"
	"calcside/internal/policy"
	"calcside/internal/runtime"
	"calcside/internal/secrets"
	"calcside/internal/serving"
)

// serving.go implements EP-0014 ephemeral serving replicas: dedicated,
// leased node objects hosting one compiled release program. A replica
// is not a development instance -- it has no persistent session, no
// exec log, no browse/export -- but shares the identifier namespace
// and the node's MaxInstances quota with dev instances.

// replicaSlots bounds in-flight calls against one replica: 1 active
// plus 32 queued. A 34th invocation is rejected, not queued.
const replicaSlots = 33

// programReplica is a prepared, fenced program replica.
type programReplica struct {
	id         string
	owner      runtime.Owner
	labels     map[string]string
	spec       *runtime.Spec
	hook       capability.Hook
	secrets    *secrets.Set
	releaseID  string
	generation int64
	epoch      int64

	program  *engine.Program        // nil until ready
	deps     engine.SessionDeps     // template; Context+Observer per call
	creation engine.SessionCreation // immutable normalized creation

	mu        sync.Mutex
	expiresAt time.Time
	ready     bool
	closing   bool
	lifecycle context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup // prepare + every admitted invoke
	wipeOnce  sync.Once
	slots     chan struct{} // admission, cap replicaSlots
	run       chan struct{} // serialization token, cap 1
}

func (r *programReplica) wipe() {
	r.wipeOnce.Do(func() {
		if r.secrets != nil {
			r.secrets.Wipe()
		}
	})
}

// Prepare compiles and pure-initializes the bundle as an ephemeral
// replica: the reservation is registered before any expensive work so
// pending prepares hold a namespace slot and count against the node
// quota. A failed prepare leaves no usable replica; the request audit
// is returned on the 200 response for every engine-level failure.
func (m *Manager) Prepare(ctx context.Context, req *runtime.PrepareRequest) (*runtime.PrepareResponse, error) {
	if req.InstanceID == "" || req.PrepareID == "" || req.ReleaseID == "" ||
		req.Owner.UserID == "" || req.Generation <= 0 ||
		req.ExpiresAt.IsZero() || !req.ExpiresAt.After(m.now()) {
		return nil, runtime.Errf(runtime.ErrBadSpec, "invalid prepare request")
	}

	lifecycle, cancel := context.WithCancel(context.Background())
	// Every fence field is immutable once the record is published.
	r := &programReplica{
		id:         req.InstanceID,
		owner:      req.Owner,
		labels:     maps.Clone(req.Labels),
		releaseID:  req.ReleaseID,
		generation: req.Generation,
		epoch:      req.Epoch,
		expiresAt:  req.ExpiresAt,
		lifecycle:  lifecycle,
		cancel:     cancel,
		slots:      make(chan struct{}, replicaSlots),
		run:        make(chan struct{}, 1),
	}
	r.wg.Add(1) // prepare itself holds the replica until it resolves

	m.mu.Lock()
	if m.stopping {
		m.mu.Unlock()
		cancel()
		r.wg.Done()
		return nil, runtime.ErrNotRunning
	}
	if _, ok := m.insts[req.InstanceID]; ok {
		m.mu.Unlock()
		cancel()
		r.wg.Done()
		return nil, runtime.Errf(runtime.ErrBadSpec, "id is already in use")
	}
	if _, ok := m.replicas[req.InstanceID]; ok {
		m.mu.Unlock()
		cancel()
		r.wg.Done()
		return nil, runtime.Errf(runtime.ErrBadSpec, "id is already in use")
	}
	if m.creating[req.InstanceID] {
		m.mu.Unlock()
		cancel()
		r.wg.Done()
		return nil, runtime.Errf(runtime.ErrBadSpec, "id is already in use")
	}
	if m.maxInstances > 0 && len(m.insts)+len(m.replicas)+len(m.creating) >= m.maxInstances {
		m.mu.Unlock()
		cancel()
		r.wg.Done()
		return nil, runtime.ErrTooMany
	}
	m.replicas[req.InstanceID] = r
	m.mu.Unlock()

	resp := &runtime.PrepareResponse{Epoch: req.Epoch}
	failed := true
	defer func() {
		if failed {
			// The prepare's own wg ref is released before any waiter
			// may run, so discard does not wait on it.
			m.discardReplica(r)
		}
		r.wg.Done()
	}()

	// Normalize once: spec, policy hook and secrets are immutable for
	// the replica's lifetime.
	spec, typed, err := capability.NormalizeSpec(req.Spec, m.reg, m.limits)
	if err != nil {
		return nil, runtime.Errf(runtime.ErrBadSpec, "invalid spec")
	}
	hook, err := policy.NewHook(req.Policies.Global, req.Policies.User, m.evalTimeout)
	if err != nil {
		return nil, runtime.Errf(runtime.ErrBadSpec, "invalid policies")
	}
	secSet, err := buildSecrets(req.Secrets)
	if err != nil {
		return nil, runtime.Errf(runtime.ErrBadSpec, "invalid secrets")
	}

	// Publish normalized state under the replica lock; fields are
	// immutable afterwards.
	r.mu.Lock()
	r.spec = spec
	r.hook = hook
	r.secrets = secSet
	r.deps = engine.SessionDeps{
		Registry: m.reg,
		Limits:   m.limits,
		Hooks:    []capability.Hook{hook},
		Secrets:  secSet,
	}
	r.creation = engine.SessionCreation{
		InstanceID:     req.InstanceID,
		UserID:         req.Owner.UserID,
		UserEmail:      req.Owner.Email,
		Labels:         r.labels,
		Capabilities:   typed,
		Env:            spec.Env,
		Policies:       spec.Policies,
		MaxSteps:       spec.Limits.MaxSteps,
		MaxOutputBytes: spec.Limits.MaxOutputBytes,
	}
	r.mu.Unlock()

	// The call ctx is bounded by the normalized release timeout (the
	// request may only narrow it) and pinned to the lease expiry
	// snapshot -- a later renewal extends only future calls.
	dur, err := m.replicaTimeout(spec, req.TimeoutMs)
	if err != nil {
		return nil, err
	}
	callCtx, stop := r.callContext(ctx, dur)
	defer stop()

	sink := &auditSink{}
	deps := r.deps
	deps.Context = callCtx
	deps.Observer = sink
	cfg := engine.ProgramConfig{
		Entrypoint:   req.Program.Entrypoint,
		Sources:      req.Program.Sources,
		Assets:       req.Program.Assets,
		InputSchema:  req.Program.InputSchema,
		OutputSchema: req.Program.OutputSchema,
		JSONLimits:   serving.DefaultLimits(),
	}
	prog, res := m.eng.PrepareProgram(callCtx, cfg, deps, r.creation, engine.ProgramCall{
		ID:         req.PrepareID,
		ActorID:    req.Owner.UserID,
		ActorEmail: req.Owner.Email,
		Timeout:    dur,
	})
	resp.Result = programResultOf(res)
	resp.Audit = sink.drain()
	if res.Error != nil {
		return resp, nil
	}
	// Commit: a cancelled call, expired lease or a delete racing the
	// commit must never publish a ready replica.
	r.mu.Lock()
	if r.closing || callCtx.Err() != nil || r.expired(m.now()) {
		r.mu.Unlock()
		resp.Result = runtime.ProgramResult{
			ID:    req.PrepareID,
			Error: &runtime.ProgramError{Code: "timeout", Message: "execution timed out; the outcome may be unknown"},
		}
		return resp, nil
	}
	r.program = prog
	r.ready = true
	r.mu.Unlock()
	failed = false
	return resp, nil
}

// Invoke runs one fenced call against a ready replica. There is no
// dedup and no retry: every attempt carries a caller-minted InvokeID.
func (m *Manager) Invoke(ctx context.Context, req *runtime.InvokeRequest) (*runtime.InvokeResponse, error) {
	m.mu.Lock()
	r, ok := m.replicas[req.InstanceID]
	m.mu.Unlock()
	if !ok {
		return nil, runtime.ErrNotFound
	}
	if req.Owner.UserID == "" || r.owner.UserID != req.Owner.UserID {
		return nil, runtime.ErrNotOwner
	}
	if r.epoch != req.Epoch || r.releaseID != req.ReleaseID || r.generation != req.Generation {
		return nil, runtime.ErrStaleEpoch
	}
	actor := req.Actor
	if actor.UserID == "" {
		actor = req.Owner
	}

	r.mu.Lock()
	if r.closing || !r.ready || r.expired(m.now()) {
		r.mu.Unlock()
		return nil, runtime.ErrNotRunning
	}
	spec := r.spec
	prog := r.program
	r.mu.Unlock()

	// The call deadline exists before queue admission so a queued call
	// cannot outlive its request.
	dur, err := m.replicaTimeout(spec, req.TimeoutMs)
	if err != nil {
		return nil, err
	}
	callCtx, stop := r.callContext(ctx, dur)
	defer stop()

	r.mu.Lock()
	if r.closing || !r.ready || r.expired(m.now()) {
		r.mu.Unlock()
		return nil, runtime.ErrNotRunning
	}
	select {
	case r.slots <- struct{}{}:
	default:
		r.mu.Unlock()
		return nil, runtime.ErrTooMany
	}
	r.wg.Add(1)
	r.mu.Unlock()
	defer func() {
		<-r.slots
		r.wg.Done()
	}()

	// Serialization: one engine call per replica at a time.
	select {
	case r.run <- struct{}{}:
	case <-callCtx.Done():
		return &runtime.InvokeResponse{Result: runtime.ProgramResult{
			ID:    req.InvokeID,
			Error: &runtime.ProgramError{Code: "timeout", Message: "execution timed out; the outcome may be unknown"},
		}}, nil
	}
	defer func() { <-r.run }()

	// Re-check the fence after the queue wait.
	r.mu.Lock()
	alive := !r.closing && r.ready && !r.expired(m.now())
	r.mu.Unlock()
	if !alive {
		return nil, runtime.ErrNotRunning
	}

	sink := &auditSink{}
	deps := r.deps
	deps.Context = callCtx
	deps.Observer = sink
	res := m.eng.InvokeProgram(callCtx, prog, req.Input, deps, r.creation, engine.ProgramCall{
		ID:         req.InvokeID,
		ActorID:    actor.UserID,
		ActorEmail: actor.Email,
		Timeout:    dur,
	})
	resp := &runtime.InvokeResponse{Result: programResultOf(res)}
	// The run token is released after draining so no next call's ops
	// can interleave into this call's audit batch.
	resp.Audit = sink.drain()
	return resp, nil
}

// replicaLiveKeepalive renews a replica's lease. An expired lease
// cannot be revived; renewal only extends future calls.
func (m *Manager) keepaliveReplica(req *runtime.KeepaliveRequest) error {
	m.mu.Lock()
	r, ok := m.replicas[req.InstanceID]
	m.mu.Unlock()
	if !ok {
		return runtime.ErrNotFound
	}
	if req.Owner.UserID == "" || r.owner.UserID != req.Owner.UserID {
		return runtime.ErrNotOwner
	}
	if r.epoch != req.Epoch {
		return runtime.ErrStaleEpoch
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closing || r.expired(m.now()) || r.expiresAt.IsZero() {
		return runtime.ErrNotRunning
	}
	if req.RenewedExpiresAt.IsZero() || !req.RenewedExpiresAt.After(m.now()) {
		return runtime.Errf(runtime.ErrBadSpec, "renewal deadline must be a future time")
	}
	r.expiresAt = req.RenewedExpiresAt
	return nil
}

// deleteReplica stops a replica: closing bars new admissions, the
// lifecycle ctx cancels in-flight calls, the map entry is dropped only
// if it is this record, and secrets are wiped after all calls drain.
// Deleting an already-expired replica is allowed.
func (m *Manager) deleteReplica(req *runtime.DeleteRequest) error {
	m.mu.Lock()
	r, ok := m.replicas[req.InstanceID]
	m.mu.Unlock()
	if !ok {
		return runtime.ErrNotFound
	}
	if req.Owner.UserID == "" || r.owner.UserID != req.Owner.UserID {
		return runtime.ErrNotOwner
	}
	if r.epoch != req.Epoch {
		return runtime.ErrStaleEpoch
	}
	m.stopReplica(r)
	return nil
}

// stopReplica bars new admissions, cancels in-flight calls and drains
// them while the record stays registered -- the namespace slot and the
// quota stay occupied until every call has returned, so a deleted id
// cannot be re-claimed mid-drain.
func (m *Manager) stopReplica(r *programReplica) {
	r.mu.Lock()
	r.closing = true
	r.mu.Unlock()
	r.cancel()
	r.wg.Wait()
	m.mu.Lock()
	if m.replicas[r.id] == r {
		delete(m.replicas, r.id)
	}
	m.mu.Unlock()
	r.wipe()
}

// stopExpiredReplica is the reaper path: it only stops a replica that
// is still expired and not already closing at decision time, checked
// atomically under r.mu. A renewal that lands first wins.
func (m *Manager) stopExpiredReplica(r *programReplica) {
	r.mu.Lock()
	if r.closing || !r.expired(m.now()) {
		r.mu.Unlock()
		return
	}
	r.closing = true
	r.mu.Unlock()
	r.cancel()
	r.wg.Wait()
	m.mu.Lock()
	if m.replicas[r.id] == r {
		delete(m.replicas, r.id)
	}
	m.mu.Unlock()
	r.wipe()
}

// discardReplica cleans up a failed prepare: the only outstanding wg
// ref is the caller's own, so no wait is needed or wanted.
func (m *Manager) discardReplica(r *programReplica) {
	r.mu.Lock()
	r.closing = true
	r.mu.Unlock()
	r.cancel()
	m.mu.Lock()
	if m.replicas[r.id] == r {
		delete(m.replicas, r.id)
	}
	m.mu.Unlock()
	r.wipe()
}

// stopReplicas cancels every replica; called on shutdown.
func (m *Manager) stopReplicas() {
	m.mu.Lock()
	rs := make([]*programReplica, 0, len(m.replicas))
	for _, r := range m.replicas {
		rs = append(rs, r)
	}
	m.mu.Unlock()
	for _, r := range rs {
		m.stopReplica(r)
	}
}

func (r *programReplica) expired(now time.Time) bool {
	return r.expiresAt.IsZero() || !r.expiresAt.After(now)
}

// replicaTimeout resolves the call timeout: the normalized release
// exec timeout (already the server-capped maximum), narrowed -- never
// widened -- by the request. A negative, overflowing or widening
// TimeoutMs is rejected, not clamped.
func (m *Manager) replicaTimeout(spec *runtime.Spec, reqMs int64) (time.Duration, error) {
	var d time.Duration
	if spec.Limits.ExecTimeoutMs > 0 && spec.Limits.ExecTimeoutMs <= math.MaxInt64/int64(time.Millisecond) {
		d = time.Duration(spec.Limits.ExecTimeoutMs) * time.Millisecond
	}
	if d <= 0 {
		d = 30 * time.Second
	}
	if m.limits.MaxExecTimeout > 0 && d > m.limits.MaxExecTimeout {
		d = m.limits.MaxExecTimeout
	}
	if reqMs == 0 {
		return d, nil
	}
	if reqMs < 0 || reqMs > math.MaxInt64/int64(time.Millisecond) {
		return 0, runtime.Errf(runtime.ErrBadSpec, "timeout_ms out of range")
	}
	rd := time.Duration(reqMs) * time.Millisecond
	if rd > d {
		return 0, runtime.Errf(runtime.ErrBadSpec, "timeout_ms exceeds the release limit")
	}
	return rd, nil
}

// callContext bounds a prepare/invoke by the request ctx, the resolved
// timeout and the lease expiry snapshot, and cancels with the replica
// lifecycle.
func (r *programReplica) callContext(ctx context.Context, dur time.Duration) (context.Context, context.CancelFunc) {
	r.mu.Lock()
	exp := r.expiresAt
	lctx := r.lifecycle
	r.mu.Unlock()
	deadline := time.Now().Add(dur)
	if !exp.IsZero() && exp.Before(deadline) {
		deadline = exp
	}
	tctx, tcancel := context.WithDeadline(ctx, deadline)
	cctx, ccancel := context.WithCancelCause(tctx)
	stop := context.AfterFunc(lctx, func() {
		ccancel(errors.New("runtime: replica stopped"))
	})
	return cctx, func() {
		stop()
		ccancel(nil)
		tcancel()
	}
}

// programResultOf converts the engine result to the wire contract.
func programResultOf(res engine.ProgramResult) runtime.ProgramResult {
	out := runtime.ProgramResult{
		ID:         res.ID,
		Output:     res.Output,
		Stdout:     res.Stdout,
		DurationMs: res.DurationMs,
		Steps:      res.Steps,
	}
	if res.Error != nil {
		out.Error = &runtime.ProgramError{Code: res.Error.Code, Message: res.Error.Message}
	}
	return out
}
