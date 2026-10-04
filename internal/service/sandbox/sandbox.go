// Package sandbox implements the instance domain on the API tier:
// lifecycle, exec, execution records, the agent prompt, and file
// browsing.
//
// It owns everything that touches the database. The execution tier it
// drives through runtime.Runtime has no store handle of its own, so
// this service resolves an instance's whole context before a request
// (normalized spec, policy snapshot, decrypted secrets) and persists
// everything that comes back (execution records, audit batches, the
// sliding TTL).
package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"calcside/internal/capability"
	"calcside/internal/runtime"
	"calcside/internal/service"
	"calcside/internal/store"
	"calcside/internal/types"
)

// ExecView is the exec result as returned to the API layer (a type
// alias so the api layer needn't import the runtime contract).
type ExecView = runtime.ExecResult

// MaxCodeBytes caps the exec request code size.
const MaxCodeBytes = 256 << 10

const snippetBytes = 2 << 10

// SecretResolver turns a spec's secret entries into the plaintext set
// shipped to the execution tier plus the descriptors persisted with the
// instance. Implemented by the vault service, which holds the cipher.
type SecretResolver interface {
	Resolve(ctx context.Context, userID string, in map[string]runtime.SecretSpec) ([]runtime.Secret, map[string]runtime.SecretSpec, error)
}

// AuditSink persists the audit batches execution nodes report.
type AuditSink interface {
	Record(b runtime.AuditBatch)
}

type nopAudit struct{}

func (nopAudit) Record(runtime.AuditBatch) {}

// Options wires the service.
type Options struct {
	Store    store.Store
	Runtime  runtime.Runtime
	Secrets  SecretResolver
	Audit    AuditSink
	Registry *capability.Registry
	Limits   capability.ServerLimits
	// GlobalPolicies is the --policy-dir snapshot, loaded once at
	// startup and shipped with every create request so that every node
	// evaluates the same modules.
	GlobalPolicies map[string]string
	// MaxInstancesPerUser is a cluster-wide quota, counted from the
	// store because no single node can see the whole picture.
	MaxInstancesPerUser int
	Now                 func() time.Time
}

type Service struct {
	st         store.Store
	rt         runtime.Runtime
	secrets    SecretResolver
	audit      AuditSink
	reg        *capability.Registry
	limits     capability.ServerLimits
	globalPols map[string]string
	maxPerUser int
	now        func() time.Time
}

func New(o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Audit == nil {
		o.Audit = nopAudit{}
	}
	return &Service{
		st: o.Store, rt: o.Runtime, secrets: o.Secrets, audit: o.Audit,
		reg: o.Registry, limits: o.Limits, globalPols: o.GlobalPolicies,
		maxPerUser: o.MaxInstancesPerUser, now: o.Now,
	}
}

// fail maps an execution-tier error onto the wire error code. It routes
// on the sentinel kind, never on the message, so the mapping survives a
// process boundary.
func fail(err error) error {
	switch {
	case errors.Is(err, runtime.ErrNotFound), errors.Is(err, runtime.ErrNotOwner):
		return service.NotFound("instance not found")
	case errors.Is(err, runtime.ErrTooMany):
		return service.Errf(types.ErrCodeTooMany, "%s", err.Error())
	case errors.Is(err, runtime.ErrBadCapability):
		return service.Errf(types.ErrCodeBadCapability, "%s", err.Error())
	case errors.Is(err, runtime.ErrNoCapability):
		return service.Errf(types.ErrCodeNoFS, "instance has no fs capability")
	case errors.Is(err, runtime.ErrNoSuchPath):
		return service.NotFound(err.Error())
	case errors.Is(err, runtime.ErrFS):
		return service.Errf(types.ErrCodeFSError, "%s", err.Error())
	case errors.Is(err, runtime.ErrBadSpec):
		return service.Errf(types.ErrCodeBadSpec, "%s", err.Error())
	default:
		return service.Errf(types.ErrCodeBadSpec, "%s", err.Error())
	}
}

// owned fetches the instance enforcing ownership: other users'
// instances are not_found.
//
// This is a store read on the hot path. It is the natural place for the
// instance cache once Redis lands — the row is immutable apart from
// status and the sliding TTL.
func (s *Service) owned(ctx context.Context, a service.Actor, id string) (*store.Instance, error) {
	in, err := s.st.GetInstance(ctx, id)
	if err != nil || in.UserID != a.UserID {
		return nil, service.NotFound("instance not found")
	}
	return in, nil
}

// running fetches an owned instance and requires it to be live.
func (s *Service) running(ctx context.Context, a service.Actor, id string) (*store.Instance, error) {
	in, err := s.owned(ctx, a, id)
	if err != nil {
		return nil, err
	}
	if in.Status != types.InstanceRunning {
		return nil, service.Errf(types.ErrCodeNotRunning, "instance not running")
	}
	return in, nil
}

func owner(a service.Actor) runtime.Owner {
	return runtime.Owner{UserID: a.UserID, Email: a.Email}
}

// renewal is the sliding TTL deadline after activity at now. The API
// tier owns the clock: a node only stores what it is told, so expiry
// never depends on node clock skew.
func renewal(in *store.Instance, now time.Time) time.Time {
	var spec runtime.Spec
	if err := json.Unmarshal(in.Spec, &spec); err != nil || spec.TTLSeconds <= 0 {
		return in.ExpiresAt
	}
	return now.Add(time.Duration(spec.TTLSeconds) * time.Second)
}

func (s *Service) List(ctx context.Context, a service.Actor, status types.InstanceStatus) ([]*store.Instance, error) {
	lst, err := s.st.ListInstances(ctx, a.UserID, status)
	if err != nil {
		return nil, service.Internal(err)
	}
	if lst == nil {
		lst = []*store.Instance{}
	}
	return lst, nil
}

// selectedPolicies snapshots the owner's library policies named by the
// spec, keyed by name. An unknown name is a spec error: silently
// creating an instance without a policy it asked for would fail open.
// Compilation happens on the node; only sources cross the boundary.
func (s *Service) selectedPolicies(ctx context.Context, userID string, names []string) (map[string]string, error) {
	if len(names) == 0 {
		return nil, nil
	}
	pols, err := s.st.ListPolicies(ctx, userID)
	if err != nil {
		return nil, service.Internal(err)
	}
	byName := make(map[string]string, len(pols))
	for _, p := range pols {
		byName[p.Name] = p.Rego
	}
	out := make(map[string]string, len(names))
	for _, n := range names {
		src, ok := byName[n]
		if !ok {
			return nil, service.Errf(types.ErrCodeBadSpec, "policies: unknown policy %q", n)
		}
		out[n] = src
	}
	return out, nil
}

func (s *Service) Create(ctx context.Context, a service.Actor, rawSpec []byte) (*store.Instance, error) {
	// Validate here as well as on the node: a bad spec should be a 400
	// before a placement is spent on it.
	spec, _, err := capability.NormalizeSpec(rawSpec, s.reg, s.limits)
	if err != nil {
		return nil, fail(err)
	}
	if s.maxPerUser > 0 {
		live, err := s.st.ListInstances(ctx, a.UserID, types.InstanceRunning)
		if err != nil {
			return nil, service.Internal(err)
		}
		if len(live) >= s.maxPerUser {
			return nil, service.Errf(types.ErrCodeTooMany, "per-user instance limit reached")
		}
	}
	resolved, sanitized, err := s.secrets.Resolve(ctx, a.UserID, spec.Secrets)
	if err != nil {
		return nil, service.Errf(types.ErrCodeBadSpec, "%s", err.Error())
	}
	userPols, err := s.selectedPolicies(ctx, a.UserID, spec.Policies)
	if err != nil {
		return nil, err
	}

	now := s.now().UTC()
	meta := &store.Instance{
		ID:           store.NewID(store.PrefixInstance),
		UserID:       a.UserID,
		Spec:         spec.Sanitized(sanitized),
		Labels:       spec.Labels,
		Status:       types.InstanceRunning,
		CreatedAt:    now,
		LastActiveAt: now,
		ExpiresAt:    now.Add(time.Duration(spec.TTLSeconds) * time.Second),
	}
	if meta.Labels == nil {
		meta.Labels = map[string]string{}
	}

	// Place first, persist second. A row marked running that no node
	// actually holds is the worse failure to be left with; an instance
	// the store never learned about is reclaimed by the node's own
	// grace timer.
	cresp, err := s.rt.Create(ctx, &runtime.CreateRequest{
		InstanceID: meta.ID,
		Owner:      owner(a),
		Labels:     meta.Labels,
		Spec:       meta.Spec,
		Policies:   runtime.PolicyBundle{Global: s.globalPols, User: userPols},
		Secrets:    resolved,
		ExpiresAt:  meta.ExpiresAt,
	})
	if err != nil {
		return nil, fail(err)
	}
	// A remote runtime reports which node took the instance and under
	// what fencing epoch; persist the binding so later requests route
	// and fence correctly. An in-process runtime returns zeros.
	if cresp != nil {
		meta.NodeID = cresp.NodeID
		meta.LeaseEpoch = cresp.Epoch
	}
	if err := s.st.CreateInstance(ctx, meta); err != nil {
		_, _ = s.rt.Delete(ctx, &runtime.DeleteRequest{InstanceID: meta.ID, Owner: owner(a), Epoch: meta.LeaseEpoch})
		return nil, service.Internal(err)
	}
	return meta, nil
}

func (s *Service) Get(ctx context.Context, a service.Actor, id string) (*store.Instance, error) {
	return s.owned(ctx, a, id)
}

func (s *Service) Delete(ctx context.Context, a service.Actor, id string) error {
	in, err := s.owned(ctx, a, id)
	if err != nil {
		return err
	}
	if _, err := s.rt.Delete(ctx, &runtime.DeleteRequest{InstanceID: in.ID, Owner: owner(a), Epoch: in.LeaseEpoch}); err != nil {
		// A node that no longer knows the instance is not a client
		// error: settle the row either way.
		if !errors.Is(err, runtime.ErrNotFound) {
			return fail(err)
		}
	}
	return s.settle(ctx, in, types.InstanceDeleted)
}

// settle records a terminal status for an instance.
func (s *Service) settle(ctx context.Context, in *store.Instance, status types.InstanceStatus) error {
	now := s.now().UTC()
	in.Status = status
	in.EndedAt = &now
	if err := s.st.UpdateInstance(ctx, in); err != nil {
		return service.Internal(err)
	}
	return nil
}

func (s *Service) Keepalive(ctx context.Context, a service.Actor, id string) (*store.Instance, error) {
	in, err := s.running(ctx, a, id)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	deadline := renewal(in, now)
	if _, err := s.rt.Keepalive(ctx, &runtime.KeepaliveRequest{
		InstanceID: in.ID, Owner: owner(a), RenewedExpiresAt: deadline, Epoch: in.LeaseEpoch,
	}); err != nil {
		return nil, fail(err)
	}
	in.LastActiveAt = now
	in.ExpiresAt = deadline
	if err := s.st.UpdateInstance(ctx, in); err != nil {
		return nil, service.Internal(err)
	}
	return in, nil
}

// Exec runs code on a live, owned instance and persists the execution
// record, the audit batch, and the renewed TTL.
func (s *Service) Exec(ctx context.Context, a service.Actor, id, code string, timeout time.Duration) (*ExecView, error) {
	in, err := s.running(ctx, a, id)
	if err != nil {
		return nil, err
	}
	if len(code) > MaxCodeBytes {
		return nil, service.Errf(types.ErrCodeTooLarge, "code exceeds 256KB")
	}
	now := s.now().UTC()
	deadline := renewal(in, now)
	resp, err := s.rt.Exec(ctx, &runtime.ExecRequest{
		InstanceID:       in.ID,
		Owner:            owner(a),
		ExecID:           store.NewID(store.PrefixExecution),
		Code:             code,
		TimeoutMs:        timeout.Milliseconds(),
		RenewedExpiresAt: deadline,
		Epoch:            in.LeaseEpoch,
	})
	// Audit first and unconditionally: a denied or failed call is
	// exactly what the audit log is for, and the batch is only ever
	// reported once.
	if resp != nil {
		s.audit.Record(resp.Audit)
	}
	if err != nil {
		return nil, fail(err)
	}
	s.recordExecution(ctx, in, a, code, &resp.Result)
	in.LastActiveAt = now
	in.ExpiresAt = deadline
	_ = s.st.UpdateInstance(ctx, in)
	return &resp.Result, nil
}

func (s *Service) recordExecution(ctx context.Context, in *store.Instance, a service.Actor, code string, res *runtime.ExecResult) {
	sum := sha256.Sum256([]byte(code))
	snippet := code
	if len(snippet) > snippetBytes {
		snippet = snippet[:snippetBytes]
	}
	status := types.ExecOK
	var errType types.ExecErrorType
	if res.Error != nil {
		status = types.ExecError
		errType = res.Error.Type
	}
	_ = s.st.CreateExecution(ctx, &store.Execution{
		ID: res.ExecID, InstanceID: in.ID, UserID: a.UserID,
		CodeSHA256: hex.EncodeToString(sum[:]), CodeSnippet: snippet, Code: code,
		Status: status, ErrorType: errType, DurationMs: res.DurationMs,
		Steps: res.Steps, OutputBytes: int64(len(res.Output)),
	})
}

func (s *Service) ListExecutions(ctx context.Context, a service.Actor, instanceID string, limit int) ([]*store.Execution, error) {
	in, err := s.owned(ctx, a, instanceID)
	if err != nil {
		return nil, err
	}
	lst, err := s.st.ListExecutions(ctx, in.ID, limit)
	if err != nil {
		return nil, service.Internal(err)
	}
	if lst == nil {
		lst = []*store.Execution{}
	}
	return lst, nil
}

// GetExecution returns one execution with its full code. Ownership is
// enforced via the execution's user_id: other users' execs are 404.
func (s *Service) GetExecution(ctx context.Context, a service.Actor, id string) (*store.Execution, error) {
	ex, err := s.st.GetExecution(ctx, id)
	if err != nil || ex.UserID != a.UserID {
		return nil, service.NotFound("execution not found")
	}
	return ex, nil
}

// ExpireDue transitions instances whose sliding TTL elapsed and tells
// their node to release them. Expiry is a status change, so it is the
// API tier's job; a node only reclaims memory, and only after a grace
// period, in case it can no longer reach us.
func (s *Service) ExpireDue(ctx context.Context, limit int) (int, error) {
	due, err := s.st.ListExpiredInstances(ctx, s.now().UTC(), limit)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, in := range due {
		_, err := s.rt.Delete(ctx, &runtime.DeleteRequest{
			InstanceID: in.ID,
			Owner:      runtime.Owner{UserID: in.UserID},
			Epoch:      in.LeaseEpoch,
		})
		if err != nil && !errors.Is(err, runtime.ErrNotFound) {
			continue
		}
		if err := s.settle(ctx, in, types.InstanceExpired); err != nil {
			continue
		}
		n++
	}
	return n, nil
}
