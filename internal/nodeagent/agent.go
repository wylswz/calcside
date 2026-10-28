// Package nodeagent is the worker-side loop that restores and manages
// persistent replicas from the API's control plane. It owns no SQL —
// assignments arrive over runtime.NodeControl and are applied to
// runtime.Runtime with locally-derived monotonic deadlines.
//
// Lease conversion: the API stamps ServerNow at end of response. The
// agent subtracts the FULL round trip from the API-remaining lifetime
// so clocks never need to agree:
//
//	localExpiry = receivedAt + (apiExpiresAt - serverNow) - roundTrip
//
// A non-positive remainder means do not prepare. All deadlines passed
// to Runtime are these conservative local times, never API absolutes.
package nodeagent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"calcside/internal/runtime"
)

// Options configures the agent.
type Options struct {
	// Control is the signed control-plane client; Runtime executes.
	Control runtime.NodeControl
	Runtime runtime.Runtime
	// NodeID and BootID are fixed for this process — the agent never
	// mints a new boot.
	NodeID, BootID        string
	Capacity              int
	PollInterval          time.Duration
	CallTimeout           time.Duration
	MaxConcurrentPrepares int
}

const (
	maxReports      = 256
	maxReportSend   = 128
	maxReportEvents = 512
	maxSyncBody     = 4 << 20
	maxInventory    = 128
	cleanupTimeout  = 5 * time.Second
)

// Agent owns the managed replica set for one node boot.
type Agent struct {
	o Options

	runOnce atomic.Bool

	mu      sync.Mutex
	managed map[string]*managed      // replica id -> owned context
	reports []*runtime.ReplicaReport // ordered pending, oldest first
	ident   runtime.NodeIdentity
	retired map[string]struct{}
	slots   int
	closing bool

	wg  sync.WaitGroup
	sem chan struct{}
}

// New validates options.
func New(o Options) (*Agent, error) {
	if o.Control == nil || o.Runtime == nil {
		return nil, errors.New("nodeagent: control and runtime required")
	}
	if o.NodeID == "" || len(o.NodeID) > 128 || o.BootID == "" || len(o.BootID) > 128 {
		return nil, errors.New("nodeagent: invalid node identity")
	}
	if o.Capacity < 1 || o.Capacity > 128 {
		return nil, errors.New("nodeagent: capacity out of range")
	}
	if o.PollInterval <= 0 {
		o.PollInterval = 5 * time.Second
	}
	if o.CallTimeout <= 0 {
		o.CallTimeout = 10 * time.Second
	}
	if o.MaxConcurrentPrepares <= 0 {
		o.MaxConcurrentPrepares = 4
	}
	if o.MaxConcurrentPrepares > o.Capacity {
		o.MaxConcurrentPrepares = o.Capacity
	}
	return &Agent{o: o, managed: map[string]*managed{}, retired: map[string]struct{}{},
		sem: make(chan struct{}, o.MaxConcurrentPrepares)}, nil
}

// LeaseDeadline converts an API lease to a local monotonic deadline.
// remaining = apiExpiresAt - serverNow - fullRTT; valid only when
// positive. receivedAt is the real local time right after the RPC.
func LeaseDeadline(receivedAt time.Time, fullRTT time.Duration, apiExpiresAt, serverNow time.Time) (time.Time, bool) {
	if fullRTT < 0 || serverNow.IsZero() || apiExpiresAt.IsZero() {
		return time.Time{}, false
	}
	remaining := apiExpiresAt.Sub(serverNow) - fullRTT
	if remaining <= 0 {
		return time.Time{}, false
	}
	return receivedAt.Add(remaining), true
}

func newReportID() string {
	var b [16]byte
	rand.Read(b[:])
	return "rpt_" + hex.EncodeToString(b[:])
}

func terminal(err error) bool {
	return errors.Is(err, runtime.ErrStaleEpoch) ||
		errors.Is(err, runtime.ErrBadSpec) ||
		errors.Is(err, runtime.ErrNotOwner) ||
		errors.Is(err, runtime.ErrNotFound)
}

// Run registers the boot once, then syncs every PollInterval. Terminal
// fence errors cancel all owned contexts and return; transient errors
// never act as an authoritative empty set — owned contexts live until
// their local lease expires.
func (a *Agent) Run(ctx context.Context) error {
	if !a.runOnce.CompareAndSwap(false, true) {
		return errors.New("nodeagent: already running")
	}
	defer a.shutdown()

	// Register once: transient failures retry on the poll cadence;
	// terminal fence errors end the run without re-registering.
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		cctx, cancel := context.WithTimeout(ctx, a.o.CallTimeout)
		resp, err := a.o.Control.RegisterNode(cctx, &runtime.NodeRegisterRequest{
			NodeID: a.o.NodeID, BootID: a.o.BootID, Capacity: a.o.Capacity})
		cancel()
		if err == nil && (resp == nil || resp.Node.NodeID != a.o.NodeID ||
			resp.Node.BootID != a.o.BootID || resp.Node.Incarnation <= 0 || resp.ServerNow.IsZero()) {
			err = errors.New("nodeagent: malformed registration response")
		}
		if err == nil {
			a.ident = resp.Node
			slog.Info("nodeagent registered", "node", a.ident.NodeID,
				"incarnation", a.ident.Incarnation)
			break
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if terminal(err) {
			return fmt.Errorf("nodeagent: registration rejected: %w", err)
		}
		slog.Warn("nodeagent register retry", "err_kind", "transient")
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(a.o.PollInterval):
		}
	}

	tick := time.NewTicker(a.o.PollInterval)
	defer tick.Stop()
	for {
		if err := a.sync(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if terminal(err) {
				return fmt.Errorf("nodeagent: sync rejected: %w", err)
			}
			slog.Warn("nodeagent sync retry")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

// sync performs one heartbeat/report/inventory exchange and applies a
// validated snapshot. It returns the control error unchanged.
func (a *Agent) sync(ctx context.Context) error {
	start := time.Now()
	a.mu.Lock()
	inv := make([]runtime.ReplicaAssignment, 0, len(a.managed))
	now := start
	for _, m := range a.managed {
		if m.ready && m.confirmedExpiry.After(now) {
			inv = append(inv, m.assignment)
		}
	}
	batch := a.reportBatch(inv)
	ident := a.ident
	capacity := a.o.Capacity
	a.mu.Unlock()

	cctx, cancel := context.WithTimeout(ctx, a.o.CallTimeout)
	resp, err := a.o.Control.SyncNode(cctx, &runtime.NodeSyncRequest{
		Node: ident, Capacity: capacity, Inventory: inv, Reports: batch,
	})
	cancel()
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	received := time.Now()
	rtt := received.Sub(start)

	// Validate the snapshot before touching owned state.
	if resp == nil || resp.ServerNow.IsZero() || len(resp.Assignments) > a.o.Capacity {
		return fmt.Errorf("nodeagent: malformed sync response")
	}
	seen := map[string]bool{}
	for _, as := range resp.Assignments {
		if as.ReplicaID == "" || as.InstanceID == "" || as.ReleaseID == "" ||
			len(as.ReplicaID) > 128 || len(as.InstanceID) > 128 || len(as.ReleaseID) > 128 ||
			as.Owner.UserID == "" || len(as.Owner.UserID) > 128 || as.ExpiresAt.IsZero() ||
			as.Generation <= 0 || as.Epoch <= 0 || seen[as.ReplicaID] {
			return fmt.Errorf("nodeagent: malformed sync response")
		}
		seen[as.ReplicaID] = true
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	for _, as := range resp.Assignments {
		if m := a.managed[as.ReplicaID]; m != nil && !sameAssignment(m.assignment, as) {
			return errors.New("nodeagent: malformed sync response")
		}
	}
	if a.closing {
		return context.Canceled
	}
	// Ack reports the API actually saw — only ids we sent.
	a.ackReports(batch, resp.AcknowledgedReports)

	// Reconcile the full snapshot: remove absent, update existing
	// same-fence, launch new.
	for id, m := range a.managed {
		if !seen[id] {
			m.ready = false
			m.cancel()
		}
	}
	for id := range a.retired {
		if !seen[id] {
			delete(a.retired, id)
		}
	}
	for _, as := range resp.Assignments {
		m, ok := a.managed[as.ReplicaID]
		if ok {
			deadline, valid := LeaseDeadline(received, rtt, as.ExpiresAt, resp.ServerNow)
			if !valid {
				m.cancel()
				continue
			}
			m.desiredExpiry = deadline
			select {
			case m.update <- struct{}{}:
			default:
			}
			continue
		}
		if _, retired := a.retired[as.ReplicaID]; retired || a.slots+2 > maxReports {
			continue
		}
		deadline, valid := LeaseDeadline(received, rtt, as.ExpiresAt, resp.ServerNow)
		if !valid {
			continue // already dead on arrival
		}
		nm := &managed{
			assignment:    as,
			desiredExpiry: deadline,
			update:        make(chan struct{}, 1),
			reserved:      2,
		}
		var cctx context.Context
		cctx, nm.cancel = context.WithCancel(context.Background())
		a.managed[as.ReplicaID] = nm
		a.slots += 2
		a.wg.Add(1)
		go a.replica(cctx, nm)
	}
	return nil
}

// shutdown cancels every owned context and waits for bounded cleanup.
func (a *Agent) shutdown() {
	a.mu.Lock()
	a.closing = true
	for _, m := range a.managed {
		m.ready = false
		m.cancel()
	}
	a.mu.Unlock()

	a.wg.Wait()
	// One final best-effort sync reports terminal state — never
	// inventory or new desired work; skip when never registered.
	if a.ident.NodeID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	a.flushReports(ctx)
}

func sameAssignment(a, b runtime.ReplicaAssignment) bool {
	return a.ReplicaID == b.ReplicaID && a.InstanceID == b.InstanceID &&
		a.ReleaseID == b.ReleaseID && a.Generation == b.Generation &&
		a.Epoch == b.Epoch && a.Owner.UserID == b.Owner.UserID
}
