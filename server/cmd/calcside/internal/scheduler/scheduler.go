// Package scheduler owns placement: it picks a worker for a fresh
// instance or replica and claims replicas for persistent instances.
// Runtime routing of already-bound work lives in the worker router.
//
// Membership is always the shared store: workers self-register over
// the signed control channel with an advertised endpoint, heartbeat
// every NodeTTL window, and the DB row — not any per-API config — is
// the authority every API replica agrees on. The shared HMAC key is
// the enrollment boundary; advertised endpoints are shape-validated
// before they are persisted and again before they are used.
package scheduler

import (
	"context"
	"crypto/sha256"
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"calcside/internal/placement"
	"calcside/internal/runtime"
	"calcside/internal/store"
)

var errInternal = errors.New("scheduler: control failed")

const pageSize = 200

// Options configures the scheduler.
type Options struct {
	Store store.Store
	Now   func() time.Time
	// LeaseDuration is the replica lease granted by a claim or renewal.
	LeaseDuration time.Duration
	// NodeTTL is how long a node registration or heartbeat stays live.
	NodeTTL time.Duration
}

// Scheduler implements placement policy for dev instances and EP-0014
// persistent replicas.
type Scheduler struct {
	o Options

	rr atomic.Uint64

	reconcileMu sync.Mutex
	lastID      string
	// round rotates the rendezvous preference once per reconcile pass
	// so a budget-limited pass cannot starve the tail of the candidate
	// order when prefix nodes stay full or conflicting.
	round int
}

// New validates options.
func New(o Options) (*Scheduler, error) {
	if o.Store == nil {
		return nil, errors.New("scheduler: store required")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.LeaseDuration <= 0 {
		o.LeaseDuration = 30 * time.Second
	}
	if o.NodeTTL <= 0 {
		o.NodeTTL = 45 * time.Second
	}
	if o.LeaseDuration >= o.NodeTTL {
		return nil, errors.New("scheduler: lease must be below node ttl")
	}
	return &Scheduler{o: o}, nil
}

// Nodes lists placement candidates: live registered workers (heartbeat
// unexpired) whose stored endpoint still validates. The shared store is
// the membership authority, so every API replica reads the same rows — a
// worker that registers through one API is eligible on all of them, and a
// dead heartbeat stops counting on the same DB deadline for every reader
// (each API observes expiry within its own clock and snapshot skew).
func (s *Scheduler) Nodes(ctx context.Context) ([]placement.NodeRef, error) {
	nodes, err := s.o.Store.ListServingNodes(ctx, s.o.Now())
	if err != nil {
		return nil, errInternal
	}
	refs := make([]placement.NodeRef, 0, len(nodes))
	for _, n := range nodes {
		if !validStoredAddr(n.Addr) {
			continue // defense in depth: a malformed row never routes
		}
		refs = append(refs, placement.NodeRef{NodeID: n.NodeID, BootID: n.BootID, Addr: n.Addr})
	}
	return refs, nil
}

// Pick chooses a worker for a fresh dev instance, round-robin over
// the live registered set.
func (s *Scheduler) Pick(ctx context.Context) (placement.NodeRef, error) {
	nodes, err := s.Nodes(ctx)
	if err != nil {
		return placement.NodeRef{}, err
	}
	if len(nodes) == 0 {
		return placement.NodeRef{}, runtime.Errf(runtime.ErrTooMany, "no live worker nodes")
	}
	return nodes[s.rr.Add(1)%uint64(len(nodes))], nil
}

// validStoredAddr re-checks a persisted endpoint's shape. Registration
// already canonicalized it; this guards rows written by older or
// foreign writers.
func validStoredAddr(addr string) bool {
	_, err := placement.NormalizeNodeAddr(addr)
	return err == nil
}

// LeaseDuration is the replica lease granted by a claim or renewal.
func (s *Scheduler) LeaseDuration() time.Duration { return s.o.LeaseDuration }

// NodeTTL is how long a node registration or heartbeat stays live.
func (s *Scheduler) NodeTTL() time.Duration { return s.o.NodeTTL }

func ctxErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// rendezvousKey orders nodes deterministically per instance so
// placement is stable across API and worker boots.
func rendezvousKey(instanceID, nodeID string) []byte {
	h := sha256.Sum256([]byte(instanceID + "\x00" + nodeID))
	return h[:]
}

// maxClaimAttempts bounds ClaimServingReplica calls in one reconcile
// pass so P instances × N workers cannot explode a single pass; the
// instance cursor still advances, so skipped nodes fill on later wraps.
const maxClaimAttempts = 200

// Reconcile is the scheduler pass: one process-local winner, CAS
// authority for cross-process races. Each enabled instance targets one
// prepared replica per live node — the pass fills every unblocked node,
// bounded by the attempt budget.
func (s *Scheduler) Reconcile(ctx context.Context) error {
	if !s.reconcileMu.TryLock() {
		return nil
	}
	defer s.reconcileMu.Unlock()
	round := s.round
	s.round++

	nodes, err := s.o.Store.ListServingNodes(ctx, s.o.Now())
	if err != nil {
		return errInternal
	}
	// Registered endpoints are already trusted — the shared key
	// enrolled them at registration — so the stored address is used
	// as-is after a defensive shape check.
	var candidates []*store.ServingNode
	for _, n := range nodes {
		if validStoredAddr(n.Addr) {
			candidates = append(candidates, n)
		}
	}
	// One pass processes at most one 200-row page so reconcile work is
	// bounded; an empty tail page wraps the cursor once, then the next
	// pass resumes from the head.
	page, err := s.o.Store.ListPersistentInstances(ctx, s.lastID, pageSize)
	if err != nil {
		return errInternal
	}
	if len(page) == 0 && s.lastID != "" {
		s.lastID = ""
		page, err = s.o.Store.ListPersistentInstances(ctx, "", pageSize)
		if err != nil {
			return errInternal
		}
	}
	attempts := 0
	for _, inst := range page {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Budget exhausted: stop BEFORE advancing the cursor so the
		// first unprocessed instance is resumed by the next pass.
		if attempts >= maxClaimAttempts {
			break
		}
		s.lastID = inst.ID
		if err := s.ensureReplicas(ctx, inst, candidates, round, &attempts); err != nil {
			return ctxErr(ctx, err)
		}
	}
	return nil
}

// ensureReplicas places one replica on every live node that has no
// unexpired non-terminal lease for this instance. Nodes are tried in
// rendezvous order so capacity- or budget-limited passes prefer the
// same winners; the epoch chains through successful claims and any
// CAS conflict refreshes the instance row before the next candidate.
func (s *Scheduler) ensureReplicas(ctx context.Context, inst *store.PersistentInstance, nodes []*store.ServingNode, round int, attempts *int) error {
	if !inst.Enabled || len(nodes) == 0 {
		return nil
	}
	now := s.o.Now()
	if inst.RetryAfter != nil && now.Before(*inst.RetryAfter) {
		return nil
	}
	// Unlocked snapshot of live leases: every nonterminal unexpired
	// row blocks its node — across generations, boots and releases —
	// matching the per-node block inside ClaimServingReplica.
	rows, err := s.o.Store.ListInstanceServingReplicas(ctx, inst.ID, now)
	if err != nil {
		return errInternal
	}
	blocked := make(map[string]bool, len(rows))
	for _, r := range rows {
		blocked[r.NodeID] = true
	}
	// Rendezvous order: stable placement preference, deterministic.
	ordered := append([]*store.ServingNode(nil), nodes...)
	sort.Slice(ordered, func(i, j int) bool {
		ki, kj := rendezvousKey(inst.ID, ordered[i].NodeID), rendezvousKey(inst.ID, ordered[j].NodeID)
		for x := range ki {
			if ki[x] != kj[x] {
				return ki[x] > kj[x]
			}
		}
		return ordered[i].NodeID < ordered[j].NodeID
	})
	// Rotate the deterministic preference by this pass's round so the
	// attempt budget cannot permanently starve the tail candidates —
	// placement preference only, never notebook routing.
	if k := round % len(ordered); k > 0 {
		ordered = append(ordered[k:], ordered[:k]...)
	}
	cur := inst
	expected := inst.LastEpoch
	for _, n := range ordered {
		if *attempts >= maxClaimAttempts {
			break
		}
		if blocked[n.NodeID] {
			continue
		}
		until := now.Add(s.o.LeaseDuration)
		if until.After(n.HeartbeatUntil) {
			until = n.HeartbeatUntil
		}
		if !until.After(now) {
			continue
		}
		(*attempts)++
		rep, err := s.o.Store.ClaimServingReplica(ctx, store.ServingReplicaClaim{
			InstanceID: inst.ID, ReleaseID: cur.ReleaseID,
			NodeID: n.NodeID, BootID: n.BootID, ReplicaID: store.NewID(store.PrefixServingReplica),
			NodeIncarnation: n.Incarnation, Generation: cur.Generation,
			ExpectedEpoch: expected, Now: now, LeaseUntil: until,
		})
		if err == nil {
			expected = rep.Epoch // chain the allocation for the next node
			continue
		}
		if errors.Is(err, store.ErrNotFound) {
			return nil // instance vanished mid-pass
		}
		if !errors.Is(err, store.ErrConflict) {
			return errInternal
		}
		// CAS lost (peer claimed, node full, backoff or stale epoch):
		// refresh the instance row and keep trying later candidates —
		// the next pass heals anything still skipped.
		fresh, gerr := s.o.Store.GetPersistentInstance(ctx, inst.WorkspaceID, inst.ID)
		if gerr != nil {
			if errors.Is(gerr, store.ErrNotFound) {
				return nil
			}
			return errInternal
		}
		if !fresh.Enabled || (fresh.RetryAfter != nil && now.Before(*fresh.RetryAfter)) {
			return nil
		}
		cur, expected = fresh, fresh.LastEpoch
	}
	return nil
}
