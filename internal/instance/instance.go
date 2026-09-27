// Package instance manages sandboxed Starlark instances on an execution
// node: it builds an instance from a self-contained create request,
// wires the gate and capability bindings, routes execs, and reclaims
// memory for instances the API tier has stopped renewing.
//
// The node holds no database handle. Everything it needs arrives in the
// request (see internal/runtime) and everything the API tier must
// persist — execution results, audit events — leaves in the response.
// This is enforced structurally: this package must not import
// calcside/internal/store (see `make arch-check`).
package instance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	starjson "go.starlark.net/lib/json"
	"go.starlark.net/lib/math"
	"go.starlark.net/starlark"

	"calcside/internal/capability"
	capio "calcside/internal/capability/io"
	"calcside/internal/engine"
	"calcside/internal/policy"
	"calcside/internal/runtime"
	"calcside/internal/secrets"
	"calcside/internal/types"
)

// A Manager is the in-process implementation of the execution tier: in
// the single binary and in tests the API tier talks to it directly,
// and on a worker it is what the transport server sits in front of.
var _ runtime.Runtime = (*Manager)(nil)

// Snapshotter persists instance state (placeholder wiring: only Delete
// is called for now).
type Snapshotter interface {
	Save(ctx context.Context, instanceID string, snap *Snapshot) error
	Load(ctx context.Context, instanceID string) (*Snapshot, error)
	Delete(ctx context.Context, instanceID string) error
}

// Snapshot of an instance's files. Globals intentionally out of scope.
type Snapshot struct {
	Files     map[string][]byte
	CreatedAt time.Time
}

var ErrNoSnapshot = errors.New("instance: no snapshot")

// NoopSnapshotter does nothing; Load always returns ErrNoSnapshot.
type NoopSnapshotter struct{}

func (NoopSnapshotter) Save(ctx context.Context, id string, s *Snapshot) error { return nil }
func (NoopSnapshotter) Load(ctx context.Context, id string) (*Snapshot, error) {
	return nil, ErrNoSnapshot
}
func (NoopSnapshotter) Delete(ctx context.Context, id string) error { return nil }

// inst is one live instance. It holds no persistent identity beyond
// what the create request supplied.
type inst struct {
	id     string
	owner  runtime.Owner
	labels map[string]string

	sess    *engine.Session
	gate    *capability.Gate
	closers []io.Closer
	// capClosers indexes capability closers by capability name, for
	// host-side access (console file browse) without starlark types.
	capClosers map[types.CapabilityName]io.Closer
	out        *capio.Buffer
	vfs        interface{ Files() map[string][]byte } // *fs.Closer V, kept loose to avoid import
	capCfgs    map[string]any                         // validated capability configs, incl. implicit io
	spec       *runtime.Spec                          // normalized spec
	secrets    *secrets.Set
	sink       *auditSink

	mu        sync.Mutex // guards expiresAt and execLog
	expiresAt time.Time
	// epoch is the fencing token of the binding this instance was
	// created under; a request carrying any other epoch is rejected.
	epoch int64
	// execLog deduplicates Exec by exec_id: a transport retry replays
	// the recorded result instead of running external side effects
	// twice. Bounded FIFO; entries live at most as long as the instance.
	execLog   map[string]*execEntry
	execOrder []string
}

// execEntry is one in-flight or completed exec. Duplicates arriving
// while the first attempt still runs wait on done and share its result.
type execEntry struct {
	done chan struct{}
	resp *runtime.ExecResponse
	err  error
}

const maxExecLog = 128

// Manager owns all live instances on this node.
type Manager struct {
	eng          *engine.Engine
	reg          *capability.Registry
	evalTimeout  time.Duration
	limits       capability.ServerLimits
	maxInstances int
	snap         Snapshotter
	now          func() time.Time

	mu    sync.Mutex
	insts map[string]*inst

	reapInterval time.Duration
	reapGrace    time.Duration
	stop         chan struct{}
	wg           sync.WaitGroup
}

// Options configure a Manager.
type Options struct {
	Engine      *engine.Engine
	Registry    *capability.Registry
	Limits      capability.ServerLimits
	EvalTimeout time.Duration
	// MaxInstances caps live instances on this node; 0 is unlimited.
	// The per-user quota is the API tier's job — only it has the
	// cluster-wide view — while this protects a single node.
	MaxInstances int
	Snapshotter  Snapshotter
	Now          func() time.Time
	// ReapInterval is how often the local memory-reclaim sweep runs.
	ReapInterval time.Duration
	// ReapGrace is how long past its deadline an instance is kept before
	// the node reclaims it on its own. The API tier is the authority for
	// expiry and normally deletes the instance first; this only bounds
	// memory for instances whose API tier stopped talking to us.
	ReapGrace time.Duration
}

const defaultReapGrace = 5 * time.Minute

// New builds the manager.
func New(o Options) *Manager {
	if o.Snapshotter == nil {
		o.Snapshotter = NoopSnapshotter{}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.ReapInterval <= 0 {
		o.ReapInterval = 10 * time.Second
	}
	if o.ReapGrace <= 0 {
		o.ReapGrace = defaultReapGrace
	}
	return &Manager{
		eng: o.Engine, reg: o.Registry, evalTimeout: o.EvalTimeout,
		limits: o.Limits, maxInstances: o.MaxInstances,
		snap: o.Snapshotter, now: o.Now,
		insts: map[string]*inst{}, reapInterval: o.ReapInterval,
		reapGrace: o.ReapGrace, stop: make(chan struct{}),
	}
}

// StartReaper launches the background memory-reclaim sweep.
func (m *Manager) StartReaper() {
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		t := time.NewTicker(m.reapInterval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				m.Reap(context.Background())
			case <-m.stop:
				return
			}
		}
	}()
}

// StopReaper stops the reaper goroutine.
func (m *Manager) StopReaper() {
	select {
	case <-m.stop:
	default:
		close(m.stop)
	}
	m.wg.Wait()
}

// buildSecrets turns the resolved secrets from the request into the
// in-memory set. Vault decryption and allowlist narrowing already
// happened on the API tier; the node only reparses the allowlist.
func buildSecrets(in []runtime.Secret) (*secrets.Set, error) {
	set := secrets.NewSet()
	for _, s := range in {
		rules, err := secrets.ValidateDomains(s.AllowedDomains)
		if err != nil {
			return nil, fmt.Errorf("secret %s: %w", s.Name, err)
		}
		set.Add(s.Name, []byte(s.Value), rules)
	}
	return set, nil
}

// Create instantiates bindings + policies and registers the instance.
// The request must be self-contained: the node performs no lookups.
func (m *Manager) Create(ctx context.Context, req *runtime.CreateRequest) (*runtime.CreateResponse, error) {
	spec, typed, err := capability.NormalizeSpec(req.Spec, m.reg, m.limits)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	n := len(m.insts)
	m.mu.Unlock()
	if m.maxInstances > 0 && n >= m.maxInstances {
		return nil, runtime.ErrTooMany
	}

	// Compile the policy snapshot the API tier captured for us.
	hook, err := policy.NewHook(req.Policies.Global, req.Policies.User, m.evalTimeout)
	if err != nil {
		return nil, runtime.Errf(runtime.ErrBadSpec, "%s", err)
	}

	secSet, err := buildSecrets(req.Secrets)
	if err != nil {
		return nil, runtime.Errf(runtime.ErrBadSpec, "%s", err)
	}

	labels := req.Labels
	if labels == nil {
		labels = map[string]string{}
	}
	sink := &auditSink{}
	gate := capability.NewGate(capability.GateOwner{
		InstanceID: req.InstanceID,
		UserID:     req.Owner.UserID,
		UserEmail:  req.Owner.Email,
		Labels:     labels,
	}, []capability.Hook{hook}, sink)

	predeclared := starlark.StringDict{
		"json": starjson.Module,
		"math": math.Module,
	}

	// env: frozen dict (env.get / env["X"] / env.keys()); secrets: names
	// only. Neither goes through the gate.
	envDict := starlark.NewDict(len(spec.Env))
	for k, v := range spec.Env {
		_ = envDict.SetKey(starlark.String(k), starlark.String(v))
	}
	envDict.Freeze()
	predeclared["env"] = envDict
	predeclared["secrets"] = secretsStruct{set: secSet}
	var closers []io.Closer
	var outBuf *capio.Buffer
	var vfs interface{ Files() map[string][]byte }

	// io is always granted, even when not listed in the spec.
	caps := map[string]json.RawMessage{}
	for k, v := range spec.Capabilities {
		caps[k] = v
	}
	ioName := string(types.CapIO)
	if _, ok := caps[ioName]; !ok {
		caps[ioName] = nil
		if f, ok2 := m.reg.Get(types.CapIO); ok2 {
			cfg, err := f.Validate(json.RawMessage(fmt.Sprintf(`{"max_output_bytes":%d}`, spec.Limits.MaxOutputBytes)), m.limits)
			if err != nil {
				return nil, runtime.Errf(runtime.ErrBadSpec, "%s", err)
			}
			typed[ioName] = cfg
		}
	}

	names := make([]string, 0, len(caps))
	for n := range caps {
		if n != string(types.CapExt) {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	// ext is built last: its bindings compose the other capabilities.
	if _, ok := caps[string(types.CapExt)]; ok {
		names = append(names, string(types.CapExt))
	}
	bindings := map[types.CapabilityName]starlark.Value{}
	capClosers := map[types.CapabilityName]io.Closer{}
	for _, name := range names {
		f, _ := m.reg.Get(types.CapabilityName(name))
		val, closer, err := f.New(typed[name], capability.InstanceEnv{Gate: gate, Secrets: secSet, Bindings: bindings, MaxSteps: spec.Limits.MaxSteps})
		if err != nil {
			for _, c := range closers {
				_ = c.Close()
			}
			return nil, runtime.Errf(runtime.ErrBadSpec, "capability %s: %s", name, err)
		}
		if closer != nil {
			closers = append(closers, closer)
			capClosers[types.CapabilityName(name)] = closer
			switch c := closer.(type) {
			case *capio.Closer:
				outBuf = c.B
			case interface{ Files() map[string][]byte }:
				vfs = c
			}
		}
		predeclared[name] = val
		bindings[types.CapabilityName(name)] = val
	}
	if outBuf == nil {
		outBuf = capio.NewBuffer(spec.Limits.MaxOutputBytes)
	}
	predeclared["print"] = capio.PrintBuiltin(outBuf, gate)

	if err := engine.ValidatePredeclared(predeclared); err != nil {
		for _, c := range closers {
			_ = c.Close()
		}
		return nil, runtime.Errf(runtime.ErrBadSpec, "%s", err)
	}

	in := &inst{
		id:         req.InstanceID,
		owner:      req.Owner,
		labels:     labels,
		gate:       gate,
		closers:    closers,
		capClosers: capClosers,
		out:        outBuf,
		vfs:        vfs,
		capCfgs:    typed,
		spec:       spec,
		secrets:    secSet,
		sink:       sink,
		expiresAt:  req.ExpiresAt,
		epoch:      req.Epoch,
		execLog:    map[string]*execEntry{},
	}
	in.sess = &engine.Session{
		Gate:        gate,
		Predeclared: predeclared,
		Globals:     starlark.StringDict{},
		InstanceID:  req.InstanceID,
		UserID:      req.Owner.UserID,
		UserEmail:   req.Owner.Email,
		Labels:      labels,
	}
	m.mu.Lock()
	m.insts[req.InstanceID] = in
	m.mu.Unlock()

	granted := make([]types.CapabilityName, 0, len(m.reg.Names()))
	for _, name := range m.reg.Names() {
		if _, ok := typed[string(name)]; ok {
			granted = append(granted, name)
		}
	}
	return &runtime.CreateResponse{Capabilities: granted}, nil
}

// live resolves an instance and verifies the caller owns it. Ownership
// is re-checked here even though the API tier already did: a node is
// reachable by anything on the internal network, so it must not treat
// the caller's word as sufficient.
func (m *Manager) live(id string, owner runtime.Owner, epoch int64) (*inst, error) {
	m.mu.Lock()
	in, ok := m.insts[id]
	m.mu.Unlock()
	if !ok {
		return nil, runtime.ErrNotFound
	}
	if owner.UserID != "" && in.owner.UserID != owner.UserID {
		return nil, runtime.ErrNotOwner
	}
	// An instance created under a binding checks every request's epoch;
	// instances created before fencing (epoch 0) skip the check.
	if in.epoch != 0 && epoch != in.epoch {
		return nil, runtime.ErrStaleEpoch
	}
	return in, nil
}

func (in *inst) renew(deadline time.Time) {
	if deadline.IsZero() {
		return
	}
	in.mu.Lock()
	in.expiresAt = deadline
	in.mu.Unlock()
}

// Exec runs code on a live instance. The exec_id is the idempotency
// key: a repeated request replays the recorded result — or waits for
// the in-flight attempt — instead of running the code twice.
func (m *Manager) Exec(ctx context.Context, req *runtime.ExecRequest) (*runtime.ExecResponse, error) {
	in, err := m.live(req.InstanceID, req.Owner, req.Epoch)
	if err != nil {
		return nil, err
	}

	in.mu.Lock()
	if e, ok := in.execLog[req.ExecID]; ok {
		in.mu.Unlock()
		select {
		case <-e.done:
			return e.resp, e.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	e := &execEntry{done: make(chan struct{})}
	in.execLog[req.ExecID] = e
	in.execOrder = append(in.execOrder, req.ExecID)
	for len(in.execOrder) > maxExecLog {
		oldest := in.execOrder[0]
		in.execOrder = in.execOrder[1:]
		// Evicting an in-flight entry is harmless: a late duplicate
		// would re-run, same as if dedup were off.
		delete(in.execLog, oldest)
	}
	in.mu.Unlock()
	defer close(e.done)

	resp, err := m.exec(ctx, req, in)
	e.resp, e.err = resp, err
	return resp, err
}

func (m *Manager) exec(ctx context.Context, req *runtime.ExecRequest, in *inst) (*runtime.ExecResponse, error) {
	timeout := time.Duration(in.spec.Limits.ExecTimeoutMs) * time.Millisecond
	if req.TimeoutMs > 0 {
		d := time.Duration(req.TimeoutMs) * time.Millisecond
		if m.limits.MaxExecTimeout > 0 && d > m.limits.MaxExecTimeout {
			return nil, runtime.Errf(runtime.ErrBadSpec, "timeout_ms exceeds server max %d", m.limits.MaxExecTimeout.Milliseconds())
		}
		timeout = d
	}
	in.out.Reset()
	res := m.eng.Exec(ctx, in.sess, req.ExecID, req.Code, timeout, in.spec.Limits.MaxSteps, in.out.String)
	// Defense in depth: scrub any secret value that escaped into output.
	res.Output = in.secrets.Redact(res.Output)
	if res.Error != nil {
		res.Error.Message = in.secrets.Redact(res.Error.Message)
		res.Error.Backtrace = in.secrets.Redact(res.Error.Backtrace)
	}
	// Exec renews the sliding TTL regardless of outcome.
	in.renew(req.RenewedExpiresAt)
	return &runtime.ExecResponse{
		Result: execResult(&res),
		Audit:  in.sink.drain(),
	}, nil
}

func execResult(r *engine.Result) runtime.ExecResult {
	out := runtime.ExecResult{
		ExecID: r.ExecID, Output: r.Output,
		DurationMs: r.DurationMs, Steps: r.Steps,
	}
	if r.Error != nil {
		out.Error = &runtime.ExecError{
			Type: r.Error.Type, Message: r.Error.Message, Backtrace: r.Error.Backtrace,
		}
	}
	return out
}

// Keepalive renews the sliding TTL the API tier computed.
func (m *Manager) Keepalive(ctx context.Context, req *runtime.KeepaliveRequest) (*runtime.KeepaliveResponse, error) {
	in, err := m.live(req.InstanceID, req.Owner, req.Epoch)
	if err != nil {
		return nil, err
	}
	in.renew(req.RenewedExpiresAt)
	return &runtime.KeepaliveResponse{}, nil
}

// Prompt renders the per-capability prompt fragments for a live
// instance. Capability factories are not serializable, so the node
// renders and the API tier composes the final prompt.
func (m *Manager) Prompt(ctx context.Context, req *runtime.PromptRequest) (*runtime.PromptResponse, error) {
	in, err := m.live(req.InstanceID, req.Owner, req.Epoch)
	if err != nil {
		return nil, err
	}
	out := &runtime.PromptResponse{
		Env:            in.spec.Env,
		ExecTimeoutMs:  in.spec.Limits.ExecTimeoutMs,
		MaxSteps:       in.spec.Limits.MaxSteps,
		MaxOutputBytes: in.spec.Limits.MaxOutputBytes,
		TTLSeconds:     in.spec.TTLSeconds,
	}
	for _, name := range m.reg.Names() {
		cfg, granted := in.capCfgs[string(name)]
		if !granted {
			continue
		}
		f, _ := m.reg.Get(name)
		out.Fragments = append(out.Fragments, runtime.PromptFragment{
			Capability: name, Text: f.Prompt(cfg),
		})
	}
	if in.secrets != nil {
		for _, n := range in.secrets.Names() {
			out.Secrets = append(out.Secrets, runtime.PromptSecret{Name: n, Domains: in.secrets.Domains(n)})
		}
	}
	if raw := in.spec.Capabilities[string(types.CapNet)]; len(raw) > 0 {
		var nc struct {
			AllowHosts []string `json:"allow_hosts"`
		}
		if json.Unmarshal(raw, &nc) == nil {
			out.NetHosts = nc.AllowHosts
		}
	}
	return out, nil
}

// withConsole arms the gate for a host-side console operation and hands
// fn the gate plus the named capability's closer.
func (m *Manager) withConsole(in *inst, cap types.CapabilityName, fn func(gate *capability.Gate, closer io.Closer) error) error {
	closer, ok := in.capClosers[cap]
	if !ok {
		return runtime.ErrNoCapability
	}
	in.sess.ExecMu.Lock()
	defer in.sess.ExecMu.Unlock()
	in.gate.Arm(capability.ExecContext{
		ExecID:     "console",
		InstanceID: in.id,
		UserID:     in.owner.UserID,
		UserEmail:  in.owner.Email,
		Labels:     in.labels,
	})
	defer in.gate.Disarm()
	return fn(in.gate, closer)
}

// Delete ends a live instance: revoke gate, close resources, snapshot
// cleanup.
func (m *Manager) Delete(ctx context.Context, req *runtime.DeleteRequest) (*runtime.DeleteResponse, error) {
	in, err := m.live(req.InstanceID, req.Owner, req.Epoch)
	if err != nil {
		return nil, err
	}
	m.end(ctx, in)
	return &runtime.DeleteResponse{}, nil
}

func (m *Manager) end(ctx context.Context, in *inst) {
	in.gate.Revoke()
	if in.secrets != nil {
		in.secrets.Wipe()
	}
	for _, c := range in.closers {
		_ = c.Close()
	}
	_ = m.snap.Delete(ctx, in.id)
	m.mu.Lock()
	delete(m.insts, in.id)
	m.mu.Unlock()
}

// Reap reclaims instances the API tier has stopped renewing. Expiry as
// a *status* is the API tier's call; this only bounds memory on a node
// whose API tier went away, so it waits out a grace period first.
func (m *Manager) Reap(ctx context.Context) {
	cutoff := m.now().Add(-m.reapGrace)
	var doomed []*inst
	m.mu.Lock()
	for _, in := range m.insts {
		in.mu.Lock()
		exp := in.expiresAt
		in.mu.Unlock()
		if !exp.IsZero() && !exp.After(cutoff) {
			doomed = append(doomed, in)
		}
	}
	m.mu.Unlock()
	for _, in := range doomed {
		m.end(ctx, in)
	}
}

// Count reports live instance count (tests/metrics).
func (m *Manager) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.insts)
}

// secretsStruct is the predeclared `secrets` global: exposes only
// names() — values are never reachable from Starlark.
type secretsStruct struct{ set *secrets.Set }

func (s secretsStruct) String() string        { return "<secrets>" }
func (s secretsStruct) Type() string          { return "secrets" }
func (s secretsStruct) Freeze()               {}
func (s secretsStruct) Truth() starlark.Bool  { return true }
func (s secretsStruct) Hash() (uint32, error) { return 0, fmt.Errorf("unhashable: secrets") }

func (s secretsStruct) Attr(name string) (starlark.Value, error) {
	if name == "names" {
		set := s.set
		return starlark.NewBuiltin("secrets.names", func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			if err := starlark.UnpackArgs("secrets.names", args, kwargs); err != nil {
				return nil, err
			}
			var names []string
			if set != nil {
				names = set.Names()
			}
			l := starlark.NewList(make([]starlark.Value, len(names)))
			for i, n := range names {
				l.SetIndex(i, starlark.String(n))
			}
			return l, nil
		}), nil
	}
	return nil, nil
}

func (s secretsStruct) AttrNames() []string { return []string{"names"} }
