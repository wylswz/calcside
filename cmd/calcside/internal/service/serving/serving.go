// Package serving is the API-side scheduler for EP-0014 persistent
// instances. It owns the durable desired state: releases, logical
// instances, node boots and replica leases. Workers never touch SQL —
// they pull their assignment set and prepare payloads through
// runtime.NodeControl.
//
// Errors: invalid input -> ErrBadSpec, stale boot/CAS -> ErrStaleEpoch,
// missing -> ErrNotFound, capacity -> ErrTooMany. Raw store/resolver
// failures are never exposed — they may carry refs or source text —
// and cancellation propagates.
package serving

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"
	"unicode/utf8"

	"calcside/cmd/calcside/internal/scheduler"
	"calcside/internal/runtime"
	"calcside/internal/store"
)

var errInternal = errors.New("serving: control failed")

const (
	maxCapacity    = 128
	maxReports     = 128
	maxAuditEvents = 512
	maxIDLen       = 128
	pageSize       = 200
	maxArgsBytes   = 4 << 10
	maxTextBytes   = 1 << 10
	maxPrepareBody = 16 << 20
	maxPolicies    = 64
	maxPolicyBytes = 1 << 20
)

// SecretResolver resolves vault refs into plaintext values for a
// prepare payload — the same boundary the sandbox service uses.
type SecretResolver interface {
	Resolve(ctx context.Context, userID string, in map[string]runtime.SecretSpec) ([]runtime.Secret, map[string]runtime.SecretSpec, error)
}

// Options configures the scheduler.
type Options struct {
	Store store.Store
	// Secrets resolves release vault refs at prepare time; may be nil
	// only when no release can carry secrets.
	Secrets SecretResolver
	// Scheduler owns the node allowlist, placement and replica claims.
	Scheduler *scheduler.Scheduler
	// GlobalPolicies are the server's own policy modules, applied
	// alongside (never instead of) the release's.
	GlobalPolicies map[string]string
	Now            func() time.Time
}

// Service implements runtime.NodeControl.
type Service struct {
	o Options
}

var _ runtime.NodeControl = (*Service)(nil)

// New validates options and clones all mutable maps.
func New(o Options) (*Service, error) {
	if o.Store == nil {
		return nil, fmt.Errorf("%w: store required", errInternal)
	}
	if o.Scheduler == nil {
		return nil, fmt.Errorf("%w: scheduler required", errInternal)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	pol := map[string]string{}
	for k, v := range o.GlobalPolicies {
		pol[k] = v
	}
	o.GlobalPolicies = pol
	return &Service{o: o}, nil
}

func ctxErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func bad(format string, args ...any) error {
	return runtime.Errf(runtime.ErrBadSpec, format, args...)
}

func stale() error { return runtime.ErrStaleEpoch }

func validID(s string) bool { return s != "" && len(s) <= maxIDLen }

// RegisterNode announces a worker boot. The same boot re-registers
// idempotently with the same incarnation; a new boot bumps it; an old
// boot is rejected forever. The configured address wins — the request
// never carries one.
func (s *Service) RegisterNode(ctx context.Context, req *runtime.NodeRegisterRequest) (*runtime.NodeRegisterResponse, error) {
	if req == nil || !validID(req.NodeID) || !validID(req.BootID) ||
		req.Capacity < 1 || req.Capacity > maxCapacity {
		return nil, bad("invalid node registration")
	}
	addr, ok := s.o.Scheduler.Addr(req.NodeID)
	if !ok {
		return nil, bad("unknown node")
	}
	now := s.o.Now()
	n, err := s.o.Store.RegisterServingNode(ctx, &store.ServingNode{
		NodeID: req.NodeID, BootID: req.BootID, Addr: addr,
		Capacity: req.Capacity, HeartbeatUntil: now.Add(s.o.Scheduler.NodeTTL()),
	}, now)
	if errors.Is(err, store.ErrConflict) {
		return nil, stale()
	}
	if err != nil {
		return nil, errInternal
	}
	// Best effort: registration stays idempotent even if a reconcile
	// pass fails; the error is surfaced for the caller to retry sync.
	if err := s.Reconcile(ctx); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	return &runtime.NodeRegisterResponse{
		Node:      runtime.NodeIdentity{NodeID: n.NodeID, BootID: n.BootID, Incarnation: n.Incarnation},
		ServerNow: s.o.Now(),
	}, nil
}

// trunc cuts a string at n bytes on a UTF-8 boundary.
func trunc(v string, n int) string {
	if len(v) <= n {
		return v
	}
	for !utf8.ValidString(v[:n]) {
		n--
	}
	return v[:n]
}

// auditEvents converts a report's audit into durable events with
// deterministic ids so retries dedup at ON CONFLICT. Logical identity
// is forced from stored authority, never worker fields.
func auditEvents(rep *store.ServingReplica, inst *store.PersistentInstance, reportID string, batch runtime.AuditBatch) []store.AuditEvent {
	out := make([]store.AuditEvent, 0, len(batch.Events))
	for i, e := range batch.Events {
		sum := sha256.Sum256([]byte(fmt.Sprintf("serving-report/v1\x00%s\x00%s\x00%d", rep.ID, reportID, i)))
		out = append(out, store.AuditEvent{
			ID:          "aud_" + hex.EncodeToString(sum[:]),
			WorkspaceID: rep.WorkspaceID,
			Ts:          e.Ts.UTC(),
			UserID:      inst.UserID,
			InstanceID:  rep.InstanceID,
			ExecID:      e.ExecID,
			Capability:  e.Capability,
			Op:          e.Op,
			Args:        trunc(e.Args, maxArgsBytes),
			Phase:       e.Phase,
			Decision:    e.Decision,
			Reason:      trunc(e.Reason, maxTextBytes),
			Error:       trunc(e.Error, maxTextBytes),
			DurationMs:  e.DurationMs,
		})
	}
	return out
}

// SyncNode heartbeats the current node tuple, records reports (audit
// first, then state — a stale-desired CAS false still acks because the
// historical report was processed), reconciles, renews the worker's
// ready inventory and returns the full authoritative assignment set.
func (s *Service) SyncNode(ctx context.Context, req *runtime.NodeSyncRequest) (*runtime.NodeSyncResponse, error) {
	if req == nil || !validID(req.Node.NodeID) || !validID(req.Node.BootID) ||
		req.Node.Incarnation <= 0 ||
		len(req.Inventory) > maxCapacity || len(req.Reports) > maxReports {
		return nil, bad("invalid sync")
	}
	if !s.o.Scheduler.Known(req.Node.NodeID) {
		return nil, bad("unknown node")
	}
	if req.Capacity < 1 || req.Capacity > maxCapacity {
		return nil, bad("invalid capacity")
	}
	totalEvents := 0
	for _, r := range req.Reports {
		a := r.Assignment
		if !validID(r.ReportID) || !validID(a.ReplicaID) || !validID(a.InstanceID) ||
			!validID(a.ReleaseID) || a.Generation <= 0 || a.Epoch <= 0 {
			return nil, bad("invalid report")
		}
		switch r.State {
		case store.ServingReplicaReady, store.ServingReplicaFailed, store.ServingReplicaStopped,
			runtime.NodeReplicaAuditOnly:
		default:
			return nil, bad("invalid report state")
		}
		switch r.ErrorCode {
		case "", "invalid_program", "invalid_input", "invalid_output", "runtime",
			"timeout", "step_limit", "memory_limit", "policy_denied", "out_of_scope":
		default:
			return nil, bad("invalid report error code")
		}
		if r.State == store.ServingReplicaFailed && r.ErrorCode == "" {
			return nil, bad("failed report missing error code")
		}
		if r.State == runtime.NodeReplicaAuditOnly && r.ErrorCode != "" {
			return nil, bad("audit-only report carries no state")
		}
		for _, e := range r.Audit.Events {
			if !e.Phase.Valid() || !e.Decision.Valid() || !e.Capability.Valid() {
				return nil, bad("invalid audit event")
			}
		}
		totalEvents += len(r.Audit.Events)
	}
	if totalEvents > maxAuditEvents {
		return nil, bad("audit batch too large")
	}
	// Inventory tuples must be complete and well-formed before any
	// renewal touches them; every entry is checked against the stored
	// replica so a forged tuple can't ride a renew.
	invRows := map[string]*store.ServingReplica{}
	for _, inv := range req.Inventory {
		if !validID(inv.ReplicaID) || !validID(inv.InstanceID) || !validID(inv.ReleaseID) ||
			inv.Generation <= 0 || inv.Epoch <= 0 {
			return nil, bad("invalid inventory")
		}
		row, err := s.o.Store.GetServingReplica(ctx, inv.ReplicaID)
		if errors.Is(err, store.ErrNotFound) {
			return nil, stale()
		}
		if err != nil {
			return nil, ctxErr(ctx, errInternal)
		}
		if row.InstanceID != inv.InstanceID || row.ReleaseID != inv.ReleaseID ||
			row.Generation != inv.Generation || row.Epoch != inv.Epoch ||
			row.NodeID != req.Node.NodeID || row.BootID != req.Node.BootID ||
			row.NodeIncarnation != req.Node.Incarnation {
			return nil, stale()
		}
		if inv.Owner.UserID != "" {
			inst, err := s.o.Store.GetPersistentInstance(ctx, row.WorkspaceID, row.InstanceID)
			if err != nil {
				if errors.Is(err, store.ErrNotFound) {
					return nil, stale()
				}
				return nil, ctxErr(ctx, errInternal)
			}
			if inst.UserID != inv.Owner.UserID {
				return nil, stale()
			}
		}
		invRows[inv.ReplicaID] = row
	}
	now := s.o.Now()
	// Heartbeat the CURRENT tuple first — a stale boot gets nothing.
	ok, err := s.o.Store.HeartbeatServingNode(ctx, store.ServingNodeFence{
		NodeID: req.Node.NodeID, BootID: req.Node.BootID, Incarnation: req.Node.Incarnation,
	}, now, now.Add(s.o.Scheduler.NodeTTL()), req.Capacity)
	if err != nil {
		return nil, errInternal
	}
	if !ok {
		return nil, stale()
	}

	var acked []string
	for _, rep := range req.Reports {
		a := rep.Assignment
		row, err := s.o.Store.GetServingReplica(ctx, a.ReplicaID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil, stale()
			}
			return nil, ctxErr(ctx, errInternal)
		}
		// Exact stored fence — a spoofed report must not write.
		if row.NodeID != req.Node.NodeID || row.BootID != req.Node.BootID ||
			row.NodeIncarnation != req.Node.Incarnation ||
			row.InstanceID != a.InstanceID || row.ReleaseID != a.ReleaseID ||
			row.Generation != a.Generation || row.Epoch != a.Epoch {
			return nil, stale()
		}
		inst, err := s.o.Store.GetPersistentInstance(ctx, row.WorkspaceID, row.InstanceID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil, stale()
			}
			return nil, ctxErr(ctx, errInternal)
		}
		if a.Owner.UserID != "" && a.Owner.UserID != inst.UserID {
			return nil, stale()
		}
		// Audit first — failure audit must survive even when the
		// state transition itself is stale.
		events := auditEvents(row, inst, rep.ReportID, rep.Audit)
		if len(events) > 0 {
			if err := s.o.Store.InsertServingAuditEvents(ctx, events); err != nil {
				return nil, errInternal
			}
		}
		if rep.Audit.Dropped > 0 {
			slog.Warn("serving report audit dropped events",
				"report_id", rep.ReportID, "dropped", rep.Audit.Dropped)
		}
		if rep.State == runtime.NodeReplicaAuditOnly {
			// Audit-only: preserve the events but never shorten the
			// lease — the context may still be running.
			acked = append(acked, rep.ReportID)
			continue
		}
		_, rerr := s.o.Store.ReportServingReplica(ctx, store.ServingReplicaFence{
			ReplicaID: row.ID, InstanceID: row.InstanceID, ReleaseID: row.ReleaseID,
			NodeID: row.NodeID, BootID: row.BootID, NodeIncarnation: row.NodeIncarnation,
			Generation: row.Generation, Epoch: row.Epoch,
		}, rep.State, rep.ErrorCode, now)
		if rerr != nil {
			return nil, errInternal
		}
		// Known historical reports ack even when the CAS was stale.
		acked = append(acked, rep.ReportID)
	}

	if err := s.Reconcile(ctx); err != nil {
		return nil, err
	}

	// Renew only the worker's locally-ready inventory — preparing rows
	// and unknown ids are never renewed by fiat.
	now = s.o.Now()
	fence := store.ServingNodeFence{NodeID: req.Node.NodeID, BootID: req.Node.BootID, Incarnation: req.Node.Incarnation}
	rows, err := s.o.Store.ListNodeServingReplicas(ctx, fence, now)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil, stale()
		}
		return nil, ctxErr(ctx, errInternal)
	}
	var out []runtime.ReplicaAssignment
	for _, row := range rows {
		if _, ok := invRows[row.ID]; ok {
			until := now.Add(s.o.Scheduler.LeaseDuration())
			ren, err := s.o.Store.RenewServingReplica(ctx, store.ServingReplicaFence{
				ReplicaID: row.ID, InstanceID: row.InstanceID, ReleaseID: row.ReleaseID,
				NodeID: row.NodeID, BootID: row.BootID, NodeIncarnation: row.NodeIncarnation,
				Generation: row.Generation, Epoch: row.Epoch,
			}, now, until)
			if err != nil {
				return nil, ctxErr(ctx, errInternal)
			}
			if !ren {
				continue // lapsed mid-sync: drop from the desired set
			}
			if until.After(row.LeaseUntil) {
				row.LeaseUntil = until
			}
		}
		inst, err := s.o.Store.GetPersistentInstance(ctx, row.WorkspaceID, row.InstanceID)
		if err != nil {
			return nil, errInternal
		}
		u, err := s.o.Store.GetUser(ctx, inst.UserID)
		if err != nil {
			return nil, errInternal // never a partial desired set
		}
		end := s.o.Now()
		if !row.LeaseUntil.After(end) {
			continue // already dead by response time
		}
		out = append(out, runtime.ReplicaAssignment{
			ReplicaID: row.ID, InstanceID: row.InstanceID, ReleaseID: row.ReleaseID,
			Generation: row.Generation, Epoch: row.Epoch, ExpiresAt: row.LeaseUntil,
			Owner: runtime.Owner{UserID: inst.UserID, Email: u.Email},
		})
	}
	end := s.o.Now()
	live := out[:0]
	for _, assignment := range out {
		if assignment.ExpiresAt.After(end) {
			live = append(live, assignment)
		}
	}
	return &runtime.NodeSyncResponse{
		Assignments: live, AcknowledgedReports: acked, ServerNow: end,
	}, nil
}

// Reconcile runs one scheduler pass over persistent instances. The
// scheduler serializes passes and claims at most one replica.
func (s *Service) Reconcile(ctx context.Context) error {
	if err := s.o.Scheduler.Reconcile(ctx); err != nil {
		return ctxErr(ctx, errInternal)
	}
	return nil
}

// PrepareReplica returns the full self-contained prepare payload for
// one assigned replica — vault secrets resolved in memory and never
// stored or logged.
func (s *Service) PrepareReplica(ctx context.Context, req *runtime.NodePrepareRequest) (*runtime.NodePrepareResponse, error) {
	if req == nil || !validID(req.Node.NodeID) || !validID(req.Node.BootID) ||
		req.Node.Incarnation <= 0 || !validID(req.Assignment.ReplicaID) {
		return nil, bad("invalid prepare request")
	}
	if !s.o.Scheduler.Known(req.Node.NodeID) {
		return nil, bad("unknown node")
	}
	now := s.o.Now()
	// Current node must be live.
	live, err := s.o.Store.ListServingNodes(ctx, now)
	if err != nil {
		return nil, errInternal
	}
	var node *store.ServingNode
	for _, n := range live {
		if n.NodeID == req.Node.NodeID {
			node = n
			break
		}
	}
	if node == nil || node.BootID != req.Node.BootID || node.Incarnation != req.Node.Incarnation {
		return nil, stale()
	}
	// Replica must match the assignment exactly and still be live.
	rep, err := s.o.Store.GetServingReplica(ctx, req.Assignment.ReplicaID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, stale()
	}
	if err != nil {
		return nil, errInternal
	}
	a := req.Assignment
	if rep.NodeID != req.Node.NodeID || rep.BootID != req.Node.BootID ||
		rep.NodeIncarnation != req.Node.Incarnation ||
		rep.InstanceID != a.InstanceID || rep.ReleaseID != a.ReleaseID ||
		rep.Generation != a.Generation || rep.Epoch != a.Epoch ||
		!rep.LeaseUntil.After(now) ||
		(rep.State != store.ServingReplicaPreparing && rep.State != store.ServingReplicaReady) {
		return nil, stale()
	}
	inst, err := s.o.Store.GetPersistentInstance(ctx, rep.WorkspaceID, rep.InstanceID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, stale()
		}
		return nil, errInternal
	}
	if a.Owner.UserID != "" && a.Owner.UserID != inst.UserID {
		return nil, stale()
	}
	if !inst.Enabled || inst.Generation != rep.Generation || inst.ReleaseID != rep.ReleaseID {
		return nil, stale()
	}
	rel, err := s.o.Store.GetServingRelease(ctx, rep.WorkspaceID, rep.ReleaseID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, stale()
		}
		return nil, errInternal
	}
	u, err := s.o.Store.GetUser(ctx, inst.UserID)
	if err != nil {
		return nil, errInternal
	}
	// Decode the pinned spec for secret resolution only.
	var spec runtime.Spec
	if err := json.Unmarshal(rel.Spec, &spec); err != nil {
		return nil, bad("invalid release spec")
	}
	var secrets []runtime.Secret
	if len(spec.Secrets) > 0 {
		if s.o.Secrets == nil {
			return nil, bad("secrets unresolvable")
		}
		resolved, _, err := s.o.Secrets.Resolve(ctx, inst.WorkspaceID, spec.Secrets)
		if err != nil {
			return nil, errInternal
		}
		secrets = resolved
	}
	// Policy union: server modules are added under a namespaced key —
	// they can never shadow or override the release's own snapshot.
	global := map[string]string{}
	for k, v := range rel.Policies.Global {
		global["release/"+k] = v
	}
	for k, v := range s.o.GlobalPolicies {
		global["server/"+k] = v
	}
	if len(global)+len(rel.Policies.User) > maxPolicies {
		return nil, bad("policy snapshot too large")
	}
	totalPol := 0
	for _, v := range global {
		totalPol += len(v)
	}
	for _, v := range rel.Policies.User {
		totalPol += len(v)
	}
	if totalPol > maxPolicyBytes {
		return nil, bad("policy snapshot too large")
	}
	out := &runtime.NodePrepareResponse{
		Request: runtime.PrepareRequest{
			CreateRequest: runtime.CreateRequest{
				InstanceID: rep.ID,
				Owner:      runtime.Owner{UserID: inst.UserID, Email: u.Email},
				Spec:       rel.Spec,
				Policies:   runtime.PolicyBundle{Global: global, User: rel.Policies.User},
				Secrets:    secrets,
				Labels:     inst.Labels,
				ExpiresAt:  rep.LeaseUntil,
				Epoch:      rep.Epoch,
			},
			ReleaseID:  rep.ReleaseID,
			Generation: rep.Generation,
			Program:    rel.Program,
			PrepareID:  store.NewID("prp_"),
		},
	}
	out.ServerNow = s.o.Now()
	body, err := json.Marshal(out)
	if err != nil {
		return nil, errInternal
	}
	if len(body)+32 > maxPrepareBody {
		return nil, bad("prepare payload too large")
	}
	out.ServerNow = time.Time{}
	// Conservative recheck after the expensive resolution: a disable,
	// generation bump or lease expiry may have raced the fetch, and
	// the node boot may have been superseded.
	end := s.o.Now()
	rows2, err := s.o.Store.ListNodeServingReplicas(ctx, store.ServingNodeFence{
		NodeID: req.Node.NodeID, BootID: req.Node.BootID, Incarnation: req.Node.Incarnation,
	}, end)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil, stale()
		}
		return nil, ctxErr(ctx, errInternal)
	}
	found := false
	for _, r := range rows2 {
		if r.ID == rep.ID && r.InstanceID == rep.InstanceID &&
			r.ReleaseID == rep.ReleaseID && r.Generation == rep.Generation &&
			r.Epoch == rep.Epoch && r.BootID == rep.BootID &&
			r.NodeID == rep.NodeID && r.NodeIncarnation == rep.NodeIncarnation {
			found = true
			break
		}
	}
	if !found || !out.Request.ExpiresAt.After(end) {
		return nil, stale()
	}
	out.ServerNow = end
	return out, nil
}
