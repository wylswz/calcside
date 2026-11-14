package nodeagent

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"calcside/internal/runtime"
)

// managed is one owned replica context: metadata only — the prepared
// request, program and secrets never outlive bootstrap.
type managed struct {
	assignment      runtime.ReplicaAssignment
	desiredExpiry   time.Time
	confirmedExpiry time.Time
	cancel          context.CancelFunc
	update          chan struct{}
	ready           bool
	reserved        int
}

// replica runs one assignment's lifecycle: lease-bounded bootstrap,
// ready report, keepalive on updates, expiry/stop teardown. It never
// runs main() — invoke is the caller's path.
func (a *Agent) replica(ctx context.Context, m *managed) {
	defer a.wg.Done()
	defer func() {
		a.mu.Lock()
		if a.managed[m.assignment.ReplicaID] == m {
			delete(a.managed, m.assignment.ReplicaID)
			a.retired[m.assignment.ReplicaID] = struct{}{}
		}
		a.slots -= m.reserved
		m.reserved = 0
		a.mu.Unlock()
	}()

	// Lease-bounded bootstrap: sem acquire and fetch both die with it.
	a.mu.Lock()
	initialExpiry := m.desiredExpiry
	a.mu.Unlock()
	lctx, lcancel := context.WithDeadline(ctx, initialExpiry)
	result := a.bootstrap(lctx, m)
	lcancel()
	a.mu.Lock()
	stopping := a.closing || ctx.Err() != nil
	if result.ready && result.expiry.After(time.Now()) && !stopping {
		m.ready = true
		m.confirmedExpiry = result.expiry
	} else {
		result.ready = false
	}
	a.mu.Unlock()
	if !result.ready {
		if a.cleanup(m) {
			if stopping {
				a.enqueueReport(m, runtime.NodeReplicaStopped, "", result.audit)
			} else {
				a.enqueueReport(m, runtime.NodeReplicaFailed, stableCode(result.code), result.audit)
			}
		} else if len(result.audit.Events) > 0 || result.audit.Dropped > 0 {
			a.enqueueReport(m, runtime.NodeReplicaAuditOnly, "", result.audit)
		}
		return
	}
	a.enqueueReport(m, runtime.NodeReplicaReady, "", result.audit)

	// Ready loop: keepalive on expiry updates, teardown on deadline.
	for {
		a.mu.Lock()
		deadline := m.confirmedExpiry
		a.mu.Unlock()
		timer := time.NewTimer(time.Until(deadline))
		select {
		case <-ctx.Done():
			timer.Stop()
			a.teardown(m, runtime.NodeReplicaStopped)
			return
		case <-timer.C:
			// No renewal arrived: the lease lapsed.
			a.teardown(m, runtime.NodeReplicaStopped)
			return
		case <-m.update:
			timer.Stop()
			a.mu.Lock()
			d := m.desiredExpiry
			a.mu.Unlock()
			if !d.After(deadline) {
				continue
			}
			kctx, kcancel := context.WithTimeout(ctx, a.o.CallTimeout)
			kctx, leaseCancel := context.WithDeadline(kctx, deadline)
			_, kerr := a.o.Runtime.Keepalive(kctx, &runtime.KeepaliveRequest{
				InstanceID:       m.assignment.ReplicaID,
				Owner:            runtime.Owner{UserID: m.assignment.Owner.UserID},
				RenewedExpiresAt: d,
				Epoch:            m.assignment.Epoch,
			})
			leaseCancel()
			kcancel()
			if errors.Is(kerr, runtime.ErrNotRunning) || errors.Is(kerr, runtime.ErrNotFound) ||
				errors.Is(kerr, runtime.ErrStaleEpoch) {
				// The context is gone at runtime level — drop it and
				// never reprepare this dead assignment.
				a.teardown(m, runtime.NodeReplicaStopped)
				return
			}
			if kerr == nil {
				a.mu.Lock()
				if d.After(m.confirmedExpiry) {
					m.confirmedExpiry = d
				}
				a.mu.Unlock()
			}
			// Failed renewals never move the confirmed lease timer.
		}
	}
}

// bootstrap fetches the prepare payload under the prepare semaphore,
// validates every fence, converts the lease, and prepares once. It
// returns ready status, a stable error code and the prepare audit —
// deliberately a separate call so the 16 MiB payload isn't held by a
// long-lived stack frame.
type bootstrapResult struct {
	ready  bool
	code   string
	audit  runtime.AuditBatch
	expiry time.Time
}

func stableCode(code string) string {
	switch code {
	case "invalid_program", "invalid_input", "invalid_output", "runtime",
		"timeout", "step_limit", "memory_limit", "policy_denied", "out_of_scope":
		return code
	default:
		return "runtime"
	}
}

func (a *Agent) bootstrap(ctx context.Context, m *managed) bootstrapResult {
	select {
	case a.sem <- struct{}{}:
		defer func() { <-a.sem }()
	case <-ctx.Done():
		return bootstrapResult{code: "out_of_scope"}
	}

	start := time.Now()
	fctx, cancel := context.WithTimeout(ctx, a.o.CallTimeout)
	resp, err := a.o.Control.PrepareReplica(fctx, &runtime.NodePrepareRequest{
		Node:       a.ident,
		Assignment: m.assignment,
	})
	cancel()
	received := time.Now()
	if err != nil || resp == nil {
		return bootstrapResult{code: "out_of_scope"}
	}
	// Validate the payload's fences against the assignment.
	req := resp.Request
	if resp.ServerNow.IsZero() || req.InstanceID != m.assignment.ReplicaID ||
		req.ReleaseID != m.assignment.ReleaseID ||
		req.Generation != m.assignment.Generation ||
		req.Epoch != m.assignment.Epoch ||
		req.Owner.UserID != m.assignment.Owner.UserID ||
		req.PrepareID == "" {
		return bootstrapResult{code: "out_of_scope"}
	}
	// Convert the fetch's own lease to local time; clamp to the
	// assignment's sync-derived deadline.
	deadline, valid := LeaseDeadline(received, received.Sub(start), req.ExpiresAt, resp.ServerNow)
	if !valid {
		return bootstrapResult{code: "out_of_scope"}
	}
	a.mu.Lock()
	if deadline.After(m.desiredExpiry) {
		deadline = m.desiredExpiry
	}
	a.mu.Unlock()
	req.ExpiresAt = deadline

	pctx, pcancel := context.WithDeadline(ctx, deadline)
	pres, err := a.o.Runtime.Prepare(pctx, &req)
	pcancel()
	result := bootstrapResult{expiry: deadline}
	if pres != nil {
		result.audit = pres.Audit
	}
	if err != nil {
		result.code = "runtime"
		if errors.Is(err, runtime.ErrBadSpec) {
			result.code = "invalid_program"
		}
		return result
	}
	if pres == nil {
		result.code = "runtime"
		return result
	}
	if pres.Result.Error != nil {
		result.code = stableCode(pres.Result.Error.Code)
		return result
	}
	result.ready = true
	result.code = "timeout"
	return result
}

// teardown marks the context unready (so inventory can't claim it),
// deletes it and reports terminal only after a known-stopped ack.
func (a *Agent) teardown(m *managed, state string) {
	a.mu.Lock()
	m.ready = false
	a.mu.Unlock()
	if a.cleanup(m) {
		a.enqueueReport(m, state, "", runtime.AuditBatch{})
	}
}

// cleanup deletes the owned context under an independent bounded ctx
// (a cancelled parent mustn't abort teardown). It returns true only on
// a verifiably-stopped runtime; an unknown error means the context may
// still run, so the DB lease must be allowed to expire instead.
func (a *Agent) cleanup(m *managed) bool {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	_, err := a.o.Runtime.Delete(ctx, &runtime.DeleteRequest{
		InstanceID: m.assignment.ReplicaID,
		Owner:      runtime.Owner{UserID: m.assignment.Owner.UserID},
		Epoch:      m.assignment.Epoch,
	})
	switch {
	case err == nil || errors.Is(err, runtime.ErrNotFound) || errors.Is(err, runtime.ErrNotRunning):
		return true
	default:
		slog.Warn("nodeagent delete failed", "replica", m.assignment.ReplicaID)
		return false
	}
}
