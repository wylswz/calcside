// Package instance manages sandboxed Starlark instances: creation from
// specs, gate/binding wiring, sliding TTL with a reaper, and exec routing.
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
	"calcside/internal/hostmatch"
	"calcside/internal/policy"
	"calcside/internal/secrets"
	"calcside/internal/store"
	"calcside/internal/types"
)

// Spec is the JSON document POSTed to create an instance.
type Spec struct {
	TTLSeconds   int64                      `json:"ttl_seconds"`
	Labels       map[string]string          `json:"labels"`
	Capabilities map[string]json.RawMessage `json:"capabilities"`
	Limits       Limits                     `json:"limits"`
	Env          map[string]string          `json:"env"`
	Secrets      map[string]SecretSpec      `json:"secrets"`
}

// SecretSpec is one entry of spec.secrets: either a vault ref (name of a
// vault secret owned by the instance owner) or an inline value.
type SecretSpec struct {
	Ref            string             `json:"ref,omitempty"`
	Value          string             `json:"value,omitempty"`
	AllowedDomains []string           `json:"allowed_domains,omitempty"`
	Source         types.SecretSource `json:"source,omitempty"` // set on output; input "source" is validated for consistency
}

// Limits requested per instance (clamped by server limits).
type Limits struct {
	ExecTimeoutMs  int64  `json:"exec_timeout_ms"`
	MaxSteps       uint64 `json:"max_steps"`
	MaxOutputBytes int64  `json:"max_output_bytes"`
}

// Defaults applied to instance limits.
const (
	defaultExecTimeoutMs = 30000
	defaultMaxSteps      = 10000000
	defaultMaxOutput     = 1 << 20

	maxEnvEntries   = 64
	maxEnvValueByte = 4 << 10
)

// ServerLimits bound what specs may request.
type ServerLimits struct {
	MaxInstancesPerUser int
	DefaultTTL          time.Duration
	MaxTTL              time.Duration
	MaxExecTimeout      time.Duration
	MaxSteps            uint64
	MaxFSQuotaBytes     int64
	MaxOutputBytes      int64
	NetAllowPrivate     bool
	MaxNetResponseBytes int64
	SecretsAllowHTTP    bool
}

// Snapshotter persists instance state (placeholder wiring: only Delete is
// called for now).
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

var (
	ErrNotFound       = errors.New("instance: not found")
	ErrNotRunning     = errors.New("instance: not running")
	ErrTooMany        = errors.New("instance: per-user instance limit reached")
	ErrCapabilityName = errors.New("instance: unknown capability")
)

type inst struct {
	meta     *store.Instance
	sess     *engine.Session
	gate     *capability.Gate
	closers  []io.Closer
	out      *capio.Buffer
	vfs      interface{ Files() map[string][]byte } // *fs.Closer V, kept loose to avoid import
	capCfgs  map[string]any                         // validated capability configs, incl. implicit io
	limits   Limits
	userID   string
	userMail string
	secrets  *secrets.Set
	metaMu   sync.Mutex // guards meta timestamp/status updates
}

// Manager owns all live instances.
type Manager struct {
	st          store.Store
	eng         *engine.Engine
	reg         *capability.Registry
	obs         capability.Observer
	policyDir   string
	evalTimeout time.Duration
	limits      ServerLimits
	snap        Snapshotter
	cipher      *secrets.Cipher // nil = vault secrets disabled
	now         func() time.Time

	mu    sync.Mutex
	insts map[string]*inst

	reapInterval time.Duration
	stop         chan struct{}
	wg           sync.WaitGroup
}

// New builds the manager. MarkRunningAsLost must be invoked by caller after
// store recovery decisions are made.
func New(st store.Store, eng *engine.Engine, reg *capability.Registry, obs capability.Observer, policyDir string, evalTimeout time.Duration, limits ServerLimits, snap Snapshotter, cipher *secrets.Cipher, now func() time.Time, reapInterval time.Duration) *Manager {
	if snap == nil {
		snap = NoopSnapshotter{}
	}
	if now == nil {
		now = time.Now
	}
	if reapInterval <= 0 {
		reapInterval = 10 * time.Second
	}
	m := &Manager{
		st: st, eng: eng, reg: reg, obs: obs, policyDir: policyDir,
		evalTimeout: evalTimeout, limits: limits, snap: snap, cipher: cipher, now: now,
		insts: map[string]*inst{}, reapInterval: reapInterval,
		stop: make(chan struct{}),
	}
	return m
}

// Recover marks any store rows still "running" as lost (no snapshot
// restore yet).
func (m *Manager) Recover(ctx context.Context) (int, error) {
	return m.st.MarkRunningAsLost(ctx)
}

// StartReaper launches the background TTL reaper.
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

// ParseSpec validates spec JSON against registry + server limits and
// returns the normalized spec plus typed capability configs.
func (m *Manager) ParseSpec(raw []byte) (*Spec, map[string]any, error) {
	var spec Spec
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &spec); err != nil {
			return nil, nil, fmt.Errorf("invalid spec JSON: %w", err)
		}
	}
	// TTL
	if spec.TTLSeconds <= 0 {
		spec.TTLSeconds = int64(m.limits.DefaultTTL / time.Second)
	}
	if m.limits.MaxTTL > 0 && time.Duration(spec.TTLSeconds)*time.Second > m.limits.MaxTTL {
		return nil, nil, fmt.Errorf("ttl_seconds exceeds server max %d", int64(m.limits.MaxTTL/time.Second))
	}
	// Limits
	if spec.Limits.ExecTimeoutMs <= 0 {
		spec.Limits.ExecTimeoutMs = defaultExecTimeoutMs
	}
	if m.limits.MaxExecTimeout > 0 && time.Duration(spec.Limits.ExecTimeoutMs)*time.Millisecond > m.limits.MaxExecTimeout {
		return nil, nil, fmt.Errorf("exec_timeout_ms exceeds server max %d", m.limits.MaxExecTimeout.Milliseconds())
	}
	if spec.Limits.MaxSteps == 0 {
		spec.Limits.MaxSteps = m.limits.MaxSteps
		if spec.Limits.MaxSteps == 0 {
			spec.Limits.MaxSteps = defaultMaxSteps
		}
	}
	if m.limits.MaxSteps > 0 && spec.Limits.MaxSteps > m.limits.MaxSteps {
		spec.Limits.MaxSteps = m.limits.MaxSteps
	}
	if spec.Limits.MaxOutputBytes <= 0 {
		spec.Limits.MaxOutputBytes = m.limits.MaxOutputBytes
		if spec.Limits.MaxOutputBytes == 0 {
			spec.Limits.MaxOutputBytes = defaultMaxOutput
		}
	}
	if m.limits.MaxOutputBytes > 0 && spec.Limits.MaxOutputBytes > m.limits.MaxOutputBytes {
		spec.Limits.MaxOutputBytes = m.limits.MaxOutputBytes
	}
	// Capabilities
	sl := capability.ServerLimits{
		MaxFSQuotaBytes:     m.limits.MaxFSQuotaBytes,
		MaxTTL:              m.limits.MaxTTL,
		DefaultTTL:          m.limits.DefaultTTL,
		MaxExecTimeout:      m.limits.MaxExecTimeout,
		MaxSteps:            m.limits.MaxSteps,
		MaxOutputBytes:      m.limits.MaxOutputBytes,
		NetAllowPrivate:     m.limits.NetAllowPrivate,
		MaxNetResponseBytes: m.limits.MaxNetResponseBytes,
		SecretsAllowHTTP:    m.limits.SecretsAllowHTTP,
	}
	typed := map[string]any{}
	for name, rawCfg := range spec.Capabilities {
		f, ok := m.reg.Get(types.CapabilityName(name))
		if !ok {
			return nil, nil, fmt.Errorf("%w: %q", ErrCapabilityName, name)
		}
		cfg, err := f.Validate(rawCfg, sl)
		if err != nil {
			return nil, nil, err
		}
		typed[name] = cfg
	}
	// env validation
	if len(spec.Env) > maxEnvEntries {
		return nil, nil, fmt.Errorf("env: more than %d entries", maxEnvEntries)
	}
	for k, v := range spec.Env {
		if !secrets.ValidName(k) {
			return nil, nil, fmt.Errorf("env: invalid name %q", k)
		}
		if len(v) > maxEnvValueByte {
			return nil, nil, fmt.Errorf("env: value for %s exceeds %d bytes", k, maxEnvValueByte)
		}
	}
	// secret name validation (resolution happens in Create, needs user ctx)
	for name := range spec.Secrets {
		if !secrets.ValidName(name) {
			return nil, nil, fmt.Errorf("secrets: invalid name %q", name)
		}
		if _, isEnv := spec.Env[name]; isEnv {
			return nil, nil, fmt.Errorf("secrets: name %q also used by env", name)
		}
	}
	return &spec, typed, nil
}

// resolveSecrets decrypts vault refs / accepts inline values into a
// secrets.Set, and returns the sanitized spec.secrets map for persistence
// (inline values stripped, refs carrying effective allowed_domains).
func (m *Manager) resolveSecrets(ctx context.Context, userID string, spec *Spec) (*secrets.Set, map[string]SecretSpec, error) {
	set := secrets.NewSet()
	sanitized := map[string]SecretSpec{}
	for name, ss := range spec.Secrets {
		if ss.Ref != "" && ss.Value != "" {
			return nil, nil, fmt.Errorf("secret %s: exactly one of ref or value", name)
		}
		if ss.Source != "" && ss.Source != types.SecretVault && ss.Source != types.SecretInline {
			return nil, nil, fmt.Errorf("secret %s: invalid source %q", name, ss.Source)
		}
		if ss.Source == types.SecretVault && ss.Ref == "" {
			return nil, nil, fmt.Errorf("secret %s: source %q requires ref", name, ss.Source)
		}
		if ss.Source == types.SecretInline && ss.Value == "" {
			return nil, nil, fmt.Errorf("secret %s: source %q requires value", name, ss.Source)
		}
		var value []byte
		var rules []hostmatch.Rule
		var effDomains []string
		if ss.Ref != "" {
			if m.cipher == nil {
				return nil, nil, fmt.Errorf("secret %s: vault secrets disabled (no --secret-key)", name)
			}
			rec, err := m.st.GetSecretByName(ctx, userID, ss.Ref)
			if err != nil {
				return nil, nil, fmt.Errorf("secret %s: unknown vault ref %q", name, ss.Ref)
			}
			value, err = m.cipher.Open(rec.Ciphertext, userID+"/"+rec.Name)
			if err != nil {
				return nil, nil, fmt.Errorf("secret %s: vault decrypt failed", name)
			}
			vaultRules, err := hostmatch.ParseAll(rec.AllowedDomains)
			if err != nil {
				return nil, nil, fmt.Errorf("secret %s: bad vault domains", name)
			}
			if len(ss.AllowedDomains) == 0 {
				rules = vaultRules
				effDomains = rec.AllowedDomains
			} else {
				narrowed, err := hostmatch.ParseAll(ss.AllowedDomains)
				if err != nil {
					return nil, nil, fmt.Errorf("secret %s: %w", name, err)
				}
				for _, nr := range narrowed {
					covered := false
					for _, vr := range vaultRules {
						if hostmatch.Covers(vr, nr) {
							covered = true
							break
						}
					}
					if !covered {
						entry := nr.Host
						if nr.Port != "" {
							entry += ":" + nr.Port
						}
						return nil, nil, fmt.Errorf("secret %s: domain %q not covered by vault allowlist", name, entry)
					}
				}
				rules = narrowed
				effDomains = ss.AllowedDomains
			}
			sanitized[name] = SecretSpec{Ref: ss.Ref, AllowedDomains: effDomains, Source: types.SecretVault}
		} else if ss.Value != "" {
			if len(ss.Value) > secrets.MaxValueBytes {
				return nil, nil, fmt.Errorf("secret %s: value exceeds %d bytes", name, secrets.MaxValueBytes)
			}
			var err error
			rules, err = secrets.ValidateDomains(ss.AllowedDomains)
			if err != nil {
				return nil, nil, fmt.Errorf("secret %s: %w", name, err)
			}
			value = []byte(ss.Value)
			sanitized[name] = SecretSpec{AllowedDomains: ss.AllowedDomains, Source: types.SecretInline}
		} else {
			return nil, nil, fmt.Errorf("secret %s: exactly one of ref or value", name)
		}
		set.Add(name, value, rules)
	}
	return set, sanitized, nil
}

// sanitizeSpec returns the spec JSON persisted for the instance: env and
// structure intact, inline secret values replaced by {"inline": true}.
func sanitizeSpec(spec *Spec, sanitized map[string]SecretSpec) json.RawMessage {
	out := *spec
	if len(spec.Secrets) > 0 {
		out.Secrets = sanitized
	}
	b, err := json.Marshal(&out)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

// Create instantiates bindings + policies and registers the instance.
func (m *Manager) Create(ctx context.Context, user *store.User, rawSpec []byte) (*store.Instance, error) {
	spec, typed, err := m.ParseSpec(rawSpec)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	count := 0
	for _, in := range m.insts {
		if in.userID == user.ID {
			count++
		}
	}
	m.mu.Unlock()
	if m.limits.MaxInstancesPerUser > 0 && count >= m.limits.MaxInstancesPerUser {
		return nil, ErrTooMany
	}

	// Compile the owner's enabled policies at this moment.
	pols, err := m.st.ListPolicies(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	userPols := map[string]string{}
	for _, p := range pols {
		if p.Enabled {
			userPols[p.ID] = p.Rego
		}
	}
	hook, err := policy.NewHook(m.policyDir, userPols, m.evalTimeout)
	if err != nil {
		return nil, err
	}

	// Resolve secrets (decrypt vault refs / accept inline values) at
	// creation — snapshot semantics like policies. The persisted spec is
	// sanitized: inline values are stripped.
	secSet, sanitizedSecrets, err := m.resolveSecrets(ctx, user.ID, spec)
	if err != nil {
		return nil, err
	}

	now := m.now().UTC()
	meta := &store.Instance{
		ID:           store.NewID(store.PrefixInstance),
		UserID:       user.ID,
		Spec:         sanitizeSpec(spec, sanitizedSecrets),
		Labels:       spec.Labels,
		Status:       types.InstanceRunning,
		CreatedAt:    now,
		LastActiveAt: now,
		ExpiresAt:    now.Add(time.Duration(spec.TTLSeconds) * time.Second),
	}
	if meta.Labels == nil {
		meta.Labels = map[string]string{}
	}

	gate := capability.NewGate(capability.GateOwner{
		InstanceID: meta.ID,
		UserID:     user.ID,
		UserEmail:  user.Email,
		Labels:     meta.Labels,
	}, []capability.Hook{hook}, m.obs)

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
			cfg, err := f.Validate(json.RawMessage(fmt.Sprintf(`{"max_output_bytes":%d}`, spec.Limits.MaxOutputBytes)), capability.ServerLimits{MaxOutputBytes: m.limits.MaxOutputBytes})
			if err != nil {
				return nil, err
			}
			typed[ioName] = cfg
		}
	}

	names := make([]string, 0, len(caps))
	for n := range caps {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		f, _ := m.reg.Get(types.CapabilityName(name))
		val, closer, err := f.New(typed[name], capability.InstanceEnv{Gate: gate, Secrets: secSet})
		if err != nil {
			for _, c := range closers {
				_ = c.Close()
			}
			return nil, fmt.Errorf("capability %s: %w", name, err)
		}
		if closer != nil {
			closers = append(closers, closer)
			switch c := closer.(type) {
			case *capio.Closer:
				outBuf = c.B
			case interface{ Files() map[string][]byte }:
				vfs = c
			}
		}
		predeclared[name] = val
	}
	if outBuf == nil {
		outBuf = capio.NewBuffer(spec.Limits.MaxOutputBytes)
	}
	predeclared["print"] = capio.PrintBuiltin(outBuf, gate)

	if err := engine.ValidatePredeclared(predeclared); err != nil {
		return nil, err
	}

	if err := m.st.CreateInstance(ctx, meta); err != nil {
		for _, c := range closers {
			_ = c.Close()
		}
		return nil, err
	}

	in := &inst{
		meta:     meta,
		gate:     gate,
		closers:  closers,
		out:      outBuf,
		vfs:      vfs,
		capCfgs:  typed,
		limits:   spec.Limits,
		userID:   user.ID,
		userMail: user.Email,
		secrets:  secSet,
	}
	in.sess = &engine.Session{
		Gate:        gate,
		Predeclared: predeclared,
		Globals:     starlark.StringDict{},
		InstanceID:  meta.ID,
		UserID:      user.ID,
		UserEmail:   user.Email,
		Labels:      meta.Labels,
	}
	m.mu.Lock()
	m.insts[meta.ID] = in
	m.mu.Unlock()
	return meta, nil
}

func (m *Manager) get(id string) (*inst, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	in, ok := m.insts[id]
	return in, ok
}

// PromptPart pairs a granted capability's factory with its validated
// config for prompt rendering.
type PromptPart struct {
	Factory capability.Factory
	Config  any
}

// PromptSecret lists one secret for the prompt: name + allowed domains
// only. Values never leave the secrets package.
type PromptSecret struct {
	Name    string
	Domains []string
}

// PromptData carries everything the agent-prompt endpoint needs. Only
// live, running instances have it (the rendered instructions describe
// the instance's effective config).
type PromptData struct {
	Instance   *store.Instance
	Parts      []PromptPart // granted capabilities in registry order
	Env        map[string]string
	Secrets    []PromptSecret
	Limits     Limits
	TTLSeconds int64
	// NetHosts is the raw allow_hosts list from the persisted spec (for
	// the worked example).
	NetHosts []string
}

// PromptData returns prompt input for a live instance.
func (m *Manager) PromptData(id string) (*PromptData, error) {
	in, ok := m.get(id)
	if !ok {
		return nil, ErrNotFound
	}
	if in.meta.Status != types.InstanceRunning {
		return nil, ErrNotRunning
	}
	var spec Spec
	_ = json.Unmarshal(in.meta.Spec, &spec)
	d := &PromptData{
		Instance:   in.meta,
		Env:        spec.Env,
		Limits:     in.limits,
		TTLSeconds: spec.TTLSeconds,
	}
	for _, name := range m.reg.Names() {
		cfg, granted := in.capCfgs[string(name)]
		if !granted {
			continue
		}
		f, _ := m.reg.Get(name)
		d.Parts = append(d.Parts, PromptPart{Factory: f, Config: cfg})
	}
	if in.secrets != nil {
		for _, n := range in.secrets.Names() {
			d.Secrets = append(d.Secrets, PromptSecret{Name: n, Domains: in.secrets.Domains(n)})
		}
	}
	if raw := spec.Capabilities[string(types.CapNet)]; len(raw) > 0 {
		var nc struct {
			AllowHosts []string `json:"allow_hosts"`
		}
		if json.Unmarshal(raw, &nc) == nil {
			d.NetHosts = nc.AllowHosts
		}
	}
	return d, nil
}

// Get returns the store record if the instance is live.
func (m *Manager) Get(ctx context.Context, id string) (*store.Instance, error) {
	if in, ok := m.get(id); ok {
		return in.meta, nil
	}
	return m.st.GetInstance(ctx, id)
}

// List returns live instances for the user (status filter optional).
func (m *Manager) List(ctx context.Context, userID string, status types.InstanceStatus) ([]*store.Instance, error) {
	return m.st.ListInstances(ctx, userID, status)
}

// Keepalive bumps the sliding TTL.
func (m *Manager) Keepalive(ctx context.Context, id string) (*store.Instance, error) {
	in, ok := m.get(id)
	if !ok {
		return nil, ErrNotFound
	}
	if in.meta.Status != types.InstanceRunning {
		return nil, ErrNotRunning
	}
	bumpTTL(ctx, m, in)
	return in.meta, nil
}

// bumpTTL sets last_active=now and expires=last_active+spec ttl under the
// instance meta lock.
func bumpTTL(ctx context.Context, m *Manager, in *inst) {
	in.metaMu.Lock()
	defer in.metaMu.Unlock()
	in.meta.LastActiveAt = m.now().UTC()
	var spec Spec
	if err := json.Unmarshal(in.meta.Spec, &spec); err == nil && spec.TTLSeconds > 0 {
		in.meta.ExpiresAt = in.meta.LastActiveAt.Add(time.Duration(spec.TTLSeconds) * time.Second)
	}
	_ = m.st.UpdateInstance(ctx, in.meta)
}

// Exec runs code on a live instance.
func (m *Manager) Exec(ctx context.Context, id, code string, timeoutOverride time.Duration, record func(res *engine.Result, execID string)) (*engine.Result, error) {
	in, ok := m.get(id)
	if !ok {
		return nil, ErrNotFound
	}
	if in.meta.Status != types.InstanceRunning {
		return nil, ErrNotRunning
	}
	timeout := time.Duration(in.limits.ExecTimeoutMs) * time.Millisecond
	if timeoutOverride > 0 {
		if m.limits.MaxExecTimeout > 0 && timeoutOverride > m.limits.MaxExecTimeout {
			return nil, fmt.Errorf("timeout_ms exceeds server max %d", m.limits.MaxExecTimeout.Milliseconds())
		}
		timeout = timeoutOverride
	}
	execID := store.NewID(store.PrefixExecution)
	in.out.Reset()
	res := m.eng.Exec(ctx, in.sess, execID, code, timeout, in.limits.MaxSteps, in.out.String)
	// Defense in depth: scrub any secret value that escaped into output.
	res.Output = in.secrets.Redact(res.Output)
	if res.Error != nil {
		res.Error.Message = in.secrets.Redact(res.Error.Message)
		res.Error.Backtrace = in.secrets.Redact(res.Error.Backtrace)
	}
	// Exec bumps the sliding TTL regardless of outcome.
	bumpTTL(ctx, m, in)
	if record != nil {
		record(&res, execID)
	}
	return &res, nil
}

// FSAccess performs a gated fs op for the files API using a synthetic
// exec id. fn receives the session's gate; the caller invokes the fs
// binding through it. Returns ErrNotFound/ErrNotRunning as appropriate.
func (m *Manager) WithSession(id string, fn func(s *engine.Session, gate *capability.Gate) error) error {
	in, ok := m.get(id)
	if !ok {
		return ErrNotFound
	}
	if in.meta.Status != types.InstanceRunning {
		return ErrNotRunning
	}
	in.sess.ExecMu.Lock()
	defer in.sess.ExecMu.Unlock()
	in.gate.Arm(capability.ExecContext{
		ExecID:     "console",
		InstanceID: in.meta.ID,
		UserID:     in.userID,
		UserEmail:  in.userMail,
		Labels:     in.meta.Labels,
	})
	defer in.gate.Disarm()
	return fn(in.sess, in.gate)
}

// HasCapability reports whether the live instance has the named binding.
func (m *Manager) HasCapability(id, name string) bool {
	in, ok := m.get(id)
	if !ok {
		return false
	}
	_, ok = in.sess.Predeclared[name]
	return ok
}

// Delete ends a live instance: revoke gate, close resources, snapshot
// cleanup, store update.
func (m *Manager) Delete(ctx context.Context, id string) error {
	in, ok := m.get(id)
	if !ok {
		return ErrNotFound
	}
	m.end(ctx, in, types.InstanceDeleted)
	return nil
}

// Redact scrubs secret values out of a string destined for the caller
// (used by the files API on file contents).
func (m *Manager) Redact(id, s string) string {
	if in, ok := m.get(id); ok && in.secrets != nil {
		return in.secrets.Redact(s)
	}
	return s
}

func (m *Manager) end(ctx context.Context, in *inst, status types.InstanceStatus) {
	in.gate.Revoke()
	if in.secrets != nil {
		in.secrets.Wipe()
	}
	for _, c := range in.closers {
		_ = c.Close()
	}
	_ = m.snap.Delete(ctx, in.meta.ID)
	in.metaMu.Lock()
	now := m.now().UTC()
	in.meta.Status = status
	in.meta.EndedAt = &now
	_ = m.st.UpdateInstance(ctx, in.meta)
	in.metaMu.Unlock()
	m.mu.Lock()
	delete(m.insts, in.meta.ID)
	m.mu.Unlock()
}

// Reap expires instances whose sliding TTL elapsed. Also called by tests.
func (m *Manager) Reap(ctx context.Context) {
	now := m.now()
	var doomed []*inst
	m.mu.Lock()
	for _, in := range m.insts {
		if in.meta.Status == types.InstanceRunning && !in.meta.ExpiresAt.After(now) {
			doomed = append(doomed, in)
		}
	}
	m.mu.Unlock()
	for _, in := range doomed {
		m.end(ctx, in, types.InstanceExpired)
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
