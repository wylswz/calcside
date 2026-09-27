package instance

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"calcside/internal/capability"
	capfs "calcside/internal/capability/fs"
	capio "calcside/internal/capability/io"
	"calcside/internal/engine"
	"calcside/internal/runtime"
)

// node is a test harness that plays the API tier in front of a
// Manager: it normalizes specs, resolves inline secrets, mints IDs and
// deadlines, and accumulates the audit batches responses carry back.
// Node tests can then keep speaking in spec JSON instead of
// hand-building requests.
//
// Vault references are deliberately not supported here. Resolving them
// needs the store and the cipher, which is the API tier's job and is
// tested where that code lives (internal/service/vault).
type node struct {
	t      *testing.T
	m      *Manager
	reg    *capability.Registry
	limits capability.ServerLimits
	now    *time.Time
	owner  runtime.Owner

	mu     sync.Mutex
	events []runtime.AuditEvent
}

func defaultLimits() capability.ServerLimits {
	return capability.ServerLimits{
		DefaultTTL:      15 * time.Minute,
		MaxTTL:          24 * time.Hour,
		MaxExecTimeout:  5 * time.Minute,
		MaxFSQuotaBytes: 256 << 20,
	}
}

func newNode(t *testing.T, o Options, now *time.Time) *node {
	t.Helper()
	if o.Registry == nil {
		reg := capability.NewRegistry()
		reg.Register(capfs.Factory())
		reg.Register(capio.Factory())
		o.Registry = reg
	}
	if o.Engine == nil {
		o.Engine = engine.New(8)
	}
	if o.EvalTimeout == 0 {
		o.EvalTimeout = time.Second
	}
	if o.ReapInterval == 0 {
		o.ReapInterval = time.Hour
	}
	if now != nil {
		o.Now = func() time.Time { return *now }
	}
	return &node{
		t: t, m: New(o), reg: o.Registry, limits: o.Limits, now: now,
		owner: runtime.Owner{UserID: "usr_test", Email: "u@x.com"},
	}
}

func (n *node) clock() time.Time {
	if n.now != nil {
		return *n.now
	}
	return time.Now()
}

// inlineSecrets mirrors what the API tier ships for inline secrets:
// name, value and allowlist, verbatim.
func inlineSecrets(spec *runtime.Spec) ([]runtime.Secret, map[string]runtime.SecretSpec, error) {
	var out []runtime.Secret
	sanitized := map[string]runtime.SecretSpec{}
	for name, ss := range spec.Secrets {
		if ss.Value == "" {
			return nil, nil, fmt.Errorf("secret %s: harness supports inline values only", name)
		}
		out = append(out, runtime.Secret{
			Name: name, Value: ss.Value, AllowedDomains: ss.AllowedDomains,
		})
		sanitized[name] = runtime.SecretSpec{AllowedDomains: ss.AllowedDomains}
	}
	return out, sanitized, nil
}

var idSeq atomic.Int64

// create normalizes the spec and places the instance, returning its id.
func (n *node) create(spec string) (string, error) {
	parsed, _, err := capability.NormalizeSpec([]byte(spec), n.reg, n.limits)
	if err != nil {
		return "", err
	}
	secrets, _, err := inlineSecrets(parsed)
	if err != nil {
		return "", err
	}
	id := fmt.Sprintf("ins_test%06d", idSeq.Add(1))
	_, err = n.m.Create(context.Background(), &runtime.CreateRequest{
		InstanceID: id,
		Owner:      n.owner,
		Labels:     parsed.Labels,
		Spec:       parsed.Sanitized(nil),
		Secrets:    secrets,
		ExpiresAt:  n.clock().Add(time.Duration(parsed.TTLSeconds) * time.Second),
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

func (n *node) mustCreate(spec string) string {
	n.t.Helper()
	id, err := n.create(spec)
	if err != nil {
		n.t.Fatalf("create(%s): %v", spec, err)
	}
	return id
}

func (n *node) collect(b runtime.AuditBatch) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.events = append(n.events, b.Events...)
}

// audit returns every audit event reported so far.
func (n *node) audit() []runtime.AuditEvent {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]runtime.AuditEvent(nil), n.events...)
}

// ttlOf reads the instance's configured TTL, the way the API tier does
// from the persisted spec.
func (n *node) ttlOf(id string) time.Duration {
	in, err := n.m.live(id, n.owner, 0)
	if err != nil {
		return 0
	}
	return time.Duration(in.spec.TTLSeconds) * time.Second
}

func (n *node) exec(id, code string) (*runtime.ExecResult, error) {
	return n.execTimeout(id, code, 0)
}

func (n *node) execTimeout(id, code string, timeoutMs int64) (*runtime.ExecResult, error) {
	resp, err := n.m.Exec(context.Background(), &runtime.ExecRequest{
		InstanceID:       id,
		Owner:            n.owner,
		ExecID:           fmt.Sprintf("exe_test%06d", idSeq.Add(1)),
		Code:             code,
		TimeoutMs:        timeoutMs,
		RenewedExpiresAt: n.clock().Add(n.ttlOf(id)),
	})
	if resp != nil {
		n.collect(resp.Audit)
	}
	if err != nil {
		return nil, err
	}
	return &resp.Result, nil
}

func (n *node) mustExec(id, code string) *runtime.ExecResult {
	n.t.Helper()
	res, err := n.exec(id, code)
	if err != nil {
		n.t.Fatalf("exec: %v", err)
	}
	if res.Error != nil {
		n.t.Fatalf("exec error: %+v", res.Error)
	}
	return res
}

func (n *node) keepalive(id string) error {
	_, err := n.m.Keepalive(context.Background(), &runtime.KeepaliveRequest{
		InstanceID: id, Owner: n.owner,
		RenewedExpiresAt: n.clock().Add(n.ttlOf(id)),
	})
	return err
}

func (n *node) delete(id string) error {
	_, err := n.m.Delete(context.Background(), &runtime.DeleteRequest{
		InstanceID: id, Owner: n.owner,
	})
	return err
}
