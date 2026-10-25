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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"

	"calcside/cmd/calcside/internal/service"
	"calcside/cmd/calcside/internal/service/access"
	"calcside/internal/capability"
	capext "calcside/internal/capability/ext"
	"calcside/internal/llm"
	"calcside/internal/runtime"
	"calcside/internal/store"
	"calcside/internal/types"
)

// ExecView is the exec result as returned to the API layer (a type
// alias so the api layer needn't import the runtime contract).
type ExecView = runtime.ExecResult

// MaxCodeBytes caps the exec request code size.
const MaxCodeBytes = 256 << 10

const snippetBytes = 2 << 10

// storedOutputBytes bounds output/error detail kept per execution.
const storedOutputBytes = 64 << 10

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
	Ext      *capext.Options
	Limits   capability.ServerLimits
	Access   *access.Checker
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
	ext        *capext.Inspector
	limits     capability.ServerLimits
	access     *access.Checker
	globalPols map[string]string
	maxPerUser int
	now        func() time.Time
}

// Activity aggregates run stats per instance for the list view; the
// sandbox service owns instance-adjacent reads so the api layer does
// not need the store.
func (s *Service) Activity(ctx context.Context, ids []string) (map[string]*store.ExecActivity, error) {
	return s.st.ExecutionActivity(ctx, ids)
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
		reg: o.Registry, ext: capext.NewInspector(o.Ext), limits: o.Limits,
		access: o.Access, globalPols: o.GlobalPolicies,
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

// owned fetches the instance enforcing access: invisible instances are
// not_found. All ownership rules live in service/access.
func (s *Service) owned(ctx context.Context, a service.Actor, id string, act access.Action) (*store.Instance, error) {
	return s.access.Instance(ctx, a, id, act)
}

// running fetches an owned instance and requires it to be live.
func (s *Service) running(ctx context.Context, a service.Actor, id string, act access.Action) (*store.Instance, error) {
	in, err := s.owned(ctx, a, id, act)
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

// actorOwner is the request actor carried to the node: audit events and
// policy input name who acted, which may differ from the instance
// creator in a shared workspace.
func actorOwner(a service.Actor) runtime.Owner {
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
	f, err := s.access.Instances(ctx, a)
	if err != nil {
		return nil, err
	}
	f.Status = status
	lst, err := s.st.ListInstances(ctx, f)
	if err != nil {
		return nil, service.Internal(err)
	}
	if lst == nil {
		lst = []*store.Instance{}
	}
	return lst, nil
}

// selectedPolicies snapshots the owner's library policies named by the
// spec, keyed by name; built-ins run natively. Unknown names fail: silently
// creating an instance without a policy it asked for would fail open.
// Compilation happens on the node; only sources cross the boundary.
func (s *Service) selectedPolicies(ctx context.Context, wsID string, names []string) (map[string]string, error) {
	if len(names) == 0 {
		return nil, nil
	}
	pols, err := s.st.ListPolicies(ctx, wsID)
	if err != nil {
		return nil, service.Internal(err)
	}
	byName := make(map[string]string, len(pols))
	for _, p := range pols {
		byName[p.Name] = p.Rego
	}
	out := make(map[string]string, len(names))
	for _, n := range names {
		if runtime.IsBuiltinPolicy(n) {
			continue
		}
		src, ok := byName[n]
		if !ok {
			return nil, service.Errf(types.ErrCodeBadSpec, "policies: unknown policy %q", n)
		}
		out[n] = src
	}
	return out, nil
}

// ValidateSpec normalizes a raw spec and checks policy selection and
// secret resolution without placing an instance. Returns the sanitized
// spec bytes. Key specs (keySpec=true) additionally reject inline secret
// values — a stored key may only hold vault references.
func (s *Service) ValidateSpec(ctx context.Context, a service.Actor, rawSpec []byte, keySpec bool) (json.RawMessage, error) {
	spec, _, secs, err := s.validateSpec(ctx, a, rawSpec, keySpec)
	if err != nil {
		return nil, err
	}
	return spec.Sanitized(secs), nil
}

// resolveLLMProviders snapshots every workspace provider into each llm
// capability ext alias. The instance sees the whole provider registry;
// provider and model selection happen in code at call time. Unknown
// providers/models therefore fail in the ext, not at instance creation.
// An alias that already carries a snapshot passes through untouched.
func (s *Service) resolveLLMProviders(ctx context.Context, wsID string, raw []byte) ([]byte, error) {
	var spec map[string]json.RawMessage
	if err := json.Unmarshal(raw, &spec); err != nil {
		return raw, nil // malformed JSON is reported by NormalizeSpec
	}
	var caps map[string]json.RawMessage
	if err := json.Unmarshal(spec["capabilities"], &caps); err != nil || caps == nil {
		return raw, nil
	}
	var aliases map[string]json.RawMessage
	if err := json.Unmarshal(caps["ext"], &aliases); err != nil || aliases == nil {
		return raw, nil
	}
	var targets []string
	for a, ra := range aliases {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(ra, &obj); err != nil || obj == nil {
			continue
		}
		var source, sum string
		if err := json.Unmarshal(obj["source"], &source); err != nil || source == "" {
			continue
		}
		if rawSum, ok := obj["sum"]; ok {
			_ = json.Unmarshal(rawSum, &sum)
		}
		kind, err := s.ext.Capability(source, sum)
		if err != nil || kind != llm.Capability {
			continue // normal extension (or invalid source reported later)
		}
		if _, resolved := obj["llm"]; resolved {
			continue // already-resolved spec
		}
		targets = append(targets, a)
	}
	if len(targets) == 0 {
		return raw, nil
	}
	provs, err := s.st.ListLLMProviders(ctx, wsID)
	if err != nil {
		return nil, service.Internal(err)
	}
	snapshots := make([]map[string]any, 0, len(provs))
	for _, p := range provs {
		cfg := p.Config
		if len(cfg) == 0 {
			cfg = json.RawMessage(`{}`)
		}
		snapshots = append(snapshots, map[string]any{
			"name": p.Name, "source": p.Source, "sum": p.Sum,
			"models": p.Models, "config": json.RawMessage(cfg),
		})
	}
	blob, err := json.Marshal(snapshots)
	if err != nil {
		return nil, service.Internal(err)
	}
	// Provider configs reference workspace vault secrets. Carry those
	// references into the instance so adding the llm ext also installs
	// the credentials it needs, just like an ext's secret config field.
	secretNames := map[string]bool{}
	for _, p := range provs {
		for _, name := range llm.SecretRefs(string(p.Config)) {
			secretNames[name] = true
		}
	}
	if len(secretNames) > 0 {
		secs := map[string]json.RawMessage{}
		if rawSecs, ok := spec["secrets"]; ok {
			if err := json.Unmarshal(rawSecs, &secs); err != nil {
				secs = nil // invalid shape is reported by NormalizeSpec
			}
		}
		if secs != nil {
			for name := range secretNames {
				if _, exists := secs[name]; exists {
					continue
				}
				ref, _ := json.Marshal(map[string]string{"ref": name})
				secs[name] = ref
			}
			spec["secrets"], _ = json.Marshal(secs)
		}
	}
	for _, a := range targets {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(aliases[a], &obj); err != nil {
			continue
		}
		obj["llm"] = blob
		if b, err := json.Marshal(obj); err == nil {
			aliases[a] = b
		}
	}
	if b, err := json.Marshal(aliases); err == nil {
		caps["ext"] = b
	}
	if b, err := json.Marshal(caps); err == nil {
		spec["capabilities"] = b
	}
	return json.Marshal(spec)
}

// validateSpec runs normalization, secret resolution and policy
// selection for a raw spec; no placement happens.
func (s *Service) validateSpec(ctx context.Context, a service.Actor, rawSpec []byte, keySpec bool) (*runtime.Spec, []runtime.Secret, map[string]runtime.SecretSpec, error) {
	rawSpec, err := s.resolveLLMProviders(ctx, a.WorkspaceID, rawSpec)
	if err != nil {
		return nil, nil, nil, err
	}
	spec, _, err := capability.NormalizeSpec(rawSpec, s.reg, s.limits)
	if err != nil {
		return nil, nil, nil, fail(err)
	}
	if keySpec {
		for name, sec := range spec.Secrets {
			if sec.Value != "" {
				return nil, nil, nil, service.Errf(types.ErrCodeBadSpec, "secrets.%s: key specs hold vault references only, not inline values", name)
			}
		}
	}
	resolved, sanitized, err := s.secrets.Resolve(ctx, a.WorkspaceID, spec.Secrets)
	if err != nil {
		return nil, nil, nil, service.Errf(types.ErrCodeBadSpec, "%s", err.Error())
	}
	if _, err := s.selectedPolicies(ctx, a.WorkspaceID, spec.Policies); err != nil {
		return nil, nil, nil, err
	}
	return spec, resolved, sanitized, nil
}

// place builds the instance row, places it on the runtime and persists
// the binding — rolling the placement back if the store write fails.
// ttl is the effective TTL: it is written into the stored spec so
// renewal slides by the same amount the instance was created with.
func (s *Service) place(ctx context.Context, a service.Actor, spec *runtime.Spec, resolved []runtime.Secret, secs map[string]runtime.SecretSpec, labels map[string]string, ttl int64, keyID, workspaceID string) (*store.Instance, error) {
	userPols, err := s.selectedPolicies(ctx, a.WorkspaceID, spec.Policies)
	if err != nil {
		return nil, err
	}
	spec.TTLSeconds = ttl
	if labels == nil {
		labels = map[string]string{}
	}
	now := s.now().UTC()
	meta := &store.Instance{
		ID:           store.NewID(store.PrefixInstance),
		UserID:       a.UserID,
		Spec:         spec.Sanitized(secs),
		Labels:       labels,
		Status:       types.InstanceRunning,
		CreatedAt:    now,
		LastActiveAt: now,
		ExpiresAt:    now.Add(time.Duration(ttl) * time.Second),
		KeyID:        keyID,
		WorkspaceID:  workspaceID,
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
		_, _ = s.rt.Delete(ctx, &runtime.DeleteRequest{InstanceID: meta.ID, Owner: runtime.Owner{UserID: meta.UserID}, Epoch: meta.LeaseEpoch})
		return nil, service.Internal(err)
	}
	return meta, nil
}

func (s *Service) Create(ctx context.Context, a service.Actor, rawSpec []byte) (*store.Instance, error) {
	// Restricted keys create from their stored spec; the request body may
	// only carry labels and ttl_seconds (capped at the spec's TTL).
	if a.Restricted() {
		return s.createRestricted(ctx, a, rawSpec)
	}
	if err := s.access.Workspace(ctx, a, a.WorkspaceID, access.Run); err != nil {
		return nil, err
	}
	spec, resolved, secs, err := s.validateSpec(ctx, a, rawSpec, false)
	if err != nil {
		return nil, err
	}
	if err := s.checkUserLimit(ctx, a); err != nil {
		return nil, err
	}
	return s.place(ctx, a, spec, resolved, secs, spec.Labels, spec.TTLSeconds, "", a.WorkspaceID)
}

// checkUserLimit enforces the cluster-wide live-instance quota.
func (s *Service) checkUserLimit(ctx context.Context, a service.Actor) error {
	if s.maxPerUser <= 0 {
		return nil
	}
	live, err := s.st.ListInstances(ctx, store.InstanceFilter{WorkspaceID: a.WorkspaceID, UserID: a.UserID, Status: types.InstanceRunning})
	if err != nil {
		return service.Internal(err)
	}
	if len(live) >= s.maxPerUser {
		return service.Errf(types.ErrCodeTooMany, "per-user instance limit reached")
	}
	return nil
}

// createRestricted builds an instance from the key's stored spec; the
// request body may only carry labels (merged under the spec's — spec
// wins) and ttl_seconds (≤ the spec's TTL).
func (s *Service) createRestricted(ctx context.Context, a service.Actor, rawSpec []byte) (*store.Instance, error) {
	var req struct {
		Labels     map[string]string `json:"labels"`
		TTLSeconds *int64            `json:"ttl_seconds"`
	}
	if len(rawSpec) > 0 {
		dec := json.NewDecoder(bytes.NewReader(rawSpec))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			return nil, service.Forbidden("a restricted key's create accepts only labels and ttl_seconds")
		}
	}
	if err := s.access.Workspace(ctx, a, a.WorkspaceID, access.Run); err != nil {
		return nil, err
	}
	spec, resolved, secs, err := s.validateSpec(ctx, a, a.KeySpec, false)
	if err != nil {
		return nil, err
	}
	ttl := spec.TTLSeconds
	if req.TTLSeconds != nil {
		if *req.TTLSeconds <= 0 || *req.TTLSeconds > int64(spec.TTLSeconds) {
			return nil, service.Errf(types.ErrCodeBadSpec, "ttl_seconds must be 1..%d", spec.TTLSeconds)
		}
		ttl = *req.TTLSeconds
	}
	if err := s.checkUserLimit(ctx, a); err != nil {
		return nil, err
	}
	live, err := s.st.ListInstances(ctx, store.InstanceFilter{WorkspaceID: a.WorkspaceID, Status: types.InstanceRunning, KeyID: a.KeyID})
	if err != nil {
		return nil, service.Internal(err)
	}
	if a.KeyMaxInstances > 0 && len(live) >= a.KeyMaxInstances {
		return nil, service.Errf(types.ErrCodeTooMany, "restricted key instance limit reached")
	}
	labels := map[string]string{}
	for k, v := range req.Labels {
		labels[k] = v
	}
	for k, v := range spec.Labels {
		labels[k] = v
	}
	return s.place(ctx, a, spec, resolved, secs, labels, ttl, a.KeyID, a.WorkspaceID)
}

func (s *Service) Get(ctx context.Context, a service.Actor, id string) (*store.Instance, error) {
	return s.owned(ctx, a, id, access.View)
}

func (s *Service) Delete(ctx context.Context, a service.Actor, id string) error {
	in, err := s.access.Delete(ctx, a, id)
	if err != nil {
		return err
	}
	if _, err := s.rt.Delete(ctx, &runtime.DeleteRequest{InstanceID: in.ID, Owner: runtime.Owner{UserID: in.UserID}, Epoch: in.LeaseEpoch}); err != nil {
		// A node that no longer knows the instance is not a client
		// error: settle the row either way.
		if !errors.Is(err, runtime.ErrNotFound) {
			return fail(err)
		}
	}
	return s.settle(ctx, in, types.InstanceDeleted)
}

// markAuditWs stamps the instance's workspace on audit events so the
// audit index can filter by workspace directly.
func (s *Service) markAuditWs(in *store.Instance, events []store.AuditEvent) {
	for i := range events {
		if events[i].WorkspaceID == "" {
			events[i].WorkspaceID = in.WorkspaceID
		}
	}
}

// settle records a terminal status for an instance.
func (s *Service) settle(ctx context.Context, in *store.Instance, status types.InstanceStatus) error {
	now := s.now().UTC()
	in.Status = status
	in.EndedAt = &now
	if err := s.st.UpdateInstance(ctx, in); err != nil {
		return service.Internal(err)
	}
	// Keys bound to this instance die with it.
	if err := s.st.RevokeKeysByInstance(ctx, in.ID); err != nil {
		return service.Internal(err)
	}
	return nil
}

func (s *Service) Keepalive(ctx context.Context, a service.Actor, id string) (*store.Instance, error) {
	in, err := s.running(ctx, a, id, access.Run)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	deadline := renewal(in, now)
	if _, err := s.rt.Keepalive(ctx, &runtime.KeepaliveRequest{
		InstanceID: in.ID, Owner: runtime.Owner{UserID: in.UserID}, RenewedExpiresAt: deadline, Epoch: in.LeaseEpoch,
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
	in, err := s.running(ctx, a, id, access.Run)
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
		Owner:            runtime.Owner{UserID: in.UserID},
		Actor:            actorOwner(a),
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
	var errMsg, errTrace string
	if res.Error != nil {
		status = types.ExecError
		errType = res.Error.Type
		errMsg, _ = capStored(res.Error.Message)
		errTrace, _ = capStored(res.Error.Backtrace)
	}
	output, outputTruncated := capStored(res.Output)
	_ = s.st.CreateExecution(ctx, &store.Execution{
		ID: res.ExecID, InstanceID: in.ID, UserID: a.UserID,
		CodeSHA256: hex.EncodeToString(sum[:]), CodeSnippet: snippet, Code: code,
		Status: status, ErrorType: errType, DurationMs: res.DurationMs,
		Steps: res.Steps, OutputBytes: int64(len(res.Output)),
		Via: a.Kind, Output: output, OutputTruncated: outputTruncated,
		ErrorMessage: errMsg, ErrorBacktrace: errTrace,
	})
}

// capStored bounds stored detail text at storedOutputBytes, cutting at a
// UTF-8 rune boundary so the kept prefix is always valid text.
func capStored(s string) (string, bool) {
	if len(s) <= storedOutputBytes {
		return s, false
	}
	cut := storedOutputBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], true
}

func (s *Service) ListExecutions(ctx context.Context, a service.Actor, instanceID string, limit int) ([]*store.Execution, error) {
	in, err := s.owned(ctx, a, instanceID, access.View)
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
	return s.access.Execution(ctx, a, id)
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
