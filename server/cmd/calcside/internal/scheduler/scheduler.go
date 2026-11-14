// Package scheduler owns deploy-time placement: it picks a worker for a
// fresh instance or replica and claims replicas for persistent instances.
// Runtime routing of already-bound work lives in the worker router.
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
	// Nodes is the configured node allowlist: node id -> trusted
	// address. A worker's declared address is never used.
	Nodes         map[string]string
	Now           func() time.Time
	LeaseDuration time.Duration
	NodeTTL       time.Duration
}

// Scheduler implements placement policy for dev instances and EP-0014
// persistent replicas.
type Scheduler struct {
	o    Options
	refs []placement.NodeRef

	rr atomic.Uint64

	reconcileMu sync.Mutex
	lastID      string
}

// New validates options and freezes the configured node set.
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
	nodes := make(map[string]string, len(o.Nodes))
	for id, addr := range o.Nodes {
		nodes[id] = addr
	}
	o.Nodes = nodes
	refs := make([]placement.NodeRef, 0, len(nodes))
	for id, addr := range nodes {
		refs = append(refs, placement.NodeRef{NodeID: id, Addr: addr})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].NodeID < refs[j].NodeID })
	return &Scheduler{o: o, refs: refs}, nil
}

// Pick chooses a live configured worker for a fresh dev instance.
func (s *Scheduler) Pick(context.Context) (placement.NodeRef, error) {
	if len(s.refs) == 0 {
		return placement.NodeRef{}, runtime.Errf(runtime.ErrTooMany, "no live worker nodes")
	}
	return s.refs[s.rr.Add(1)%uint64(len(s.refs))], nil
}

// Known reports whether nodeID is in the configured allowlist.
func (s *Scheduler) Known(nodeID string) bool {
	_, ok := s.o.Nodes[nodeID]
	return ok
}

// Addr returns the trusted address of a configured node.
func (s *Scheduler) Addr(nodeID string) (string, bool) {
	addr, ok := s.o.Nodes[nodeID]
	return addr, ok
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

// Reconcile is the scheduler pass: one process-local winner, CAS
// authority for cross-process races. It claims at most one replica per
// enabled instance whose lease is dead and backoff elapsed.
func (s *Scheduler) Reconcile(ctx context.Context) error {
	if !s.reconcileMu.TryLock() {
		return nil
	}
	defer s.reconcileMu.Unlock()

	nodes, err := s.o.Store.ListServingNodes(ctx, s.o.Now())
	if err != nil {
		return errInternal
	}
	candidates := nodes[:0]
	for _, n := range nodes {
		if addr, ok := s.o.Nodes[n.NodeID]; ok {
			n.Addr = addr // configured address wins, never the stored one
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
	for _, inst := range page {
		s.lastID = inst.ID
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := s.claimOne(ctx, inst, candidates); err != nil {
			return ctxErr(ctx, err)
		}
	}
	return nil
}

// claimOne attempts to place one dead-or-absent instance.
func (s *Scheduler) claimOne(ctx context.Context, inst *store.PersistentInstance, nodes []*store.ServingNode) error {
	if !inst.Enabled || len(nodes) == 0 {
		return nil
	}
	now := s.o.Now()
	if inst.RetryAfter != nil && now.Before(*inst.RetryAfter) {
		return nil
	}
	// An unexpired non-terminal replica owns the instance — even a
	// dead-boot lease must expire before handoff.
	cur, err := s.o.Store.GetCurrentServingReplica(ctx, inst.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return errInternal
	}
	if err == nil && cur.LeaseUntil.After(now) &&
		(cur.State == store.ServingReplicaPreparing || cur.State == store.ServingReplicaReady) {
		return nil
	}
	// Rendezvous order: stable placement, deterministic fallback.
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
	for _, n := range ordered {
		until := now.Add(s.o.LeaseDuration)
		if until.After(n.HeartbeatUntil) {
			until = n.HeartbeatUntil
		}
		if !until.After(now) {
			continue
		}
		_, err := s.o.Store.ClaimServingReplica(ctx, store.ServingReplicaClaim{
			InstanceID: inst.ID, ReleaseID: inst.ReleaseID,
			NodeID: n.NodeID, BootID: n.BootID, ReplicaID: store.NewID(store.PrefixServingReplica),
			NodeIncarnation: n.Incarnation, Generation: inst.Generation,
			ExpectedEpoch: inst.LastEpoch, Now: now, LeaseUntil: until,
		})
		if errors.Is(err, store.ErrConflict) {
			continue // lost the CAS or full node: next candidate
		}
		if errors.Is(err, store.ErrNotFound) {
			return nil // instance vanished mid-pass
		}
		if err != nil {
			return errInternal
		}
		return nil // claimed: exactly one winner per pass
	}
	return nil
}
