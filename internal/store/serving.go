package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"calcside/internal/runtime"
	"calcside/internal/secrets"
	"calcside/internal/serving"
	"calcside/internal/types"
)

// ErrInvalidServingRecord marks a precondition violation on a serving
// record: malformed content, boundary plaintext, or an out-of-range
// field — distinct from ErrConflict (CAS/lease/uniqueness) and
// ErrNotFound.
var ErrInvalidServingRecord = errors.New("store: invalid serving record")

const (
	PrefixServingRelease     IDPrefix = "rel_"
	PrefixPersistentInstance IDPrefix = "pin_"
	PrefixServingReplica     IDPrefix = "rep_"
)

// Serving replica lifecycle states.
const (
	ServingReplicaPreparing = "preparing"
	ServingReplicaReady     = "ready"
	ServingReplicaFailed    = "failed"
	ServingReplicaStopped   = "stopped"
)

// ServingRelease is the immutable content-addressed snapshot a replica
// is built from. No update or delete exists: identical content dedups
// to one row and callers reuse it via Get.
type ServingRelease struct {
	ID          string                `json:"id"`
	WorkspaceID string                `json:"workspace_id"`
	UserID      string                `json:"user_id"`
	Spec        json.RawMessage       `json:"spec"`
	Policies    runtime.PolicyBundle  `json:"policies"`
	Program     runtime.ProgramBundle `json:"program"`
	CreatedAt   time.Time             `json:"created_at"`
}

// releaseFrame is the exact hashed document for a release id. Any
// field or role change changes the digest; map keys sort and byte
// slices length-escape, so a source/asset role swap cannot collide.
type releaseFrame struct {
	Format      string                `json:"format"`
	WorkspaceID string                `json:"workspace_id"`
	UserID      string                `json:"user_id"`
	Spec        json.RawMessage       `json:"spec"`
	Policies    runtime.PolicyBundle  `json:"policies"`
	Program     runtime.ProgramBundle `json:"program"`
}

const releaseFrameFormat = "calcside.serving-release/v1"

// NewServingRelease normalizes and content-addresses a release
// snapshot. Everything is defensively cloned; CreatedAt is excluded
// from the hash so identical content dedups. Plaintext secrets and
// extension snapshots are rejected — the boundary carries refs only.
func NewServingRelease(workspaceID, userID string, spec json.RawMessage, policies runtime.PolicyBundle, program runtime.ProgramBundle, createdAt time.Time) (*ServingRelease, error) {
	if workspaceID == "" || userID == "" {
		return nil, fmt.Errorf("%w: workspace and user required", ErrInvalidServingRecord)
	}
	// Decode the spec generically for normalization, and through the
	// typed contract for shape validation (Secrets is a name->spec
	// map, not a list).
	specObj, err := serving.DecodeJSON(spec, serving.DefaultLimits())
	if err != nil {
		return nil, fmt.Errorf("%w: spec must be a JSON object", ErrInvalidServingRecord)
	}
	specMap, ok := specObj.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: spec must be a JSON object", ErrInvalidServingRecord)
	}
	var typed runtime.Spec
	if err := json.Unmarshal(spec, &typed); err != nil {
		return nil, fmt.Errorf("%w: spec", ErrInvalidServingRecord)
	}
	if err := SanitizeSpecSecrets(specMap); err != nil {
		return nil, err
	}
	for name, sec := range typed.Secrets {
		if _, err := secrets.ValidateDomains(sec.AllowedDomains); err != nil {
			return nil, fmt.Errorf("%w: invalid secret domains", ErrInvalidServingRecord)
		}
		_ = name
	}
	// Extension snapshots are not yet persisted; do not pretend
	// local refs are immutable. A non-object capabilities map fails
	// the typed decode above; a present non-empty ext is rejected.
	if caps, ok := specMap["capabilities"]; ok && caps != nil {
		cm, ok := caps.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%w: spec", ErrInvalidServingRecord)
		}
		if ext, has := cm["ext"]; has && ext != nil {
			em, ok := ext.(map[string]any)
			if !ok || len(em) > 0 {
				return nil, fmt.Errorf("%w: extension snapshots not supported", ErrInvalidServingRecord)
			}
		}
	}
	// Schemas must be JSON objects and compile under the pinned
	// subset; the canonical marshal feeds both storage and the hash.
	canonSchema := map[string][]byte{}
	for name, raw := range map[string]json.RawMessage{"input_schema": program.InputSchema, "output_schema": program.OutputSchema} {
		obj, err := serving.DecodeJSON(raw, serving.DefaultLimits())
		if err != nil {
			return nil, fmt.Errorf("%w: %s must be a JSON object", ErrInvalidServingRecord, name)
		}
		if _, ok := obj.(map[string]any); !ok {
			return nil, fmt.Errorf("%w: %s must be a JSON object", ErrInvalidServingRecord, name)
		}
		if _, err := serving.CompileSchema(raw, serving.DefaultLimits()); err != nil {
			return nil, fmt.Errorf("%w: %s does not compile", ErrInvalidServingRecord, name)
		}
		canon, err := json.Marshal(obj)
		if err != nil {
			return nil, fmt.Errorf("%w: %s", ErrInvalidServingRecord, name)
		}
		canonSchema[name] = canon
	}
	// File bounds mirror the engine prepare contract; entrypoint must
	// name a real, portable source path.
	if len(program.Sources) == 0 {
		return nil, fmt.Errorf("%w: sources required", ErrInvalidServingRecord)
	}
	if len(program.Sources) > 64 || len(program.Sources)+len(program.Assets) > 128 {
		return nil, fmt.Errorf("%w: file count out of bounds", ErrInvalidServingRecord)
	}
	var srcTotal, assetTotal int64
	for p, b := range program.Sources {
		if err := portablePath(p); err != nil {
			return nil, err
		}
		if len(b) > 1<<20 {
			return nil, fmt.Errorf("%w: file exceeds per-file limit", ErrInvalidServingRecord)
		}
		srcTotal += int64(len(b))
	}
	for p, b := range program.Assets {
		if err := portablePath(p); err != nil {
			return nil, err
		}
		if len(b) > 1<<20 {
			return nil, fmt.Errorf("%w: file exceeds per-file limit", ErrInvalidServingRecord)
		}
		assetTotal += int64(len(b))
	}
	if srcTotal > 1<<20 || assetTotal > 8<<20 {
		return nil, fmt.Errorf("%w: aggregate size out of bounds", ErrInvalidServingRecord)
	}
	prog := cloneProgram(program)
	if prog.Entrypoint == "" {
		prog.Entrypoint = "main.star"
	}
	if err := portablePath(prog.Entrypoint); err != nil {
		return nil, fmt.Errorf("%w: entrypoint path", ErrInvalidServingRecord)
	}
	if _, ok := prog.Sources[prog.Entrypoint]; !ok {
		return nil, fmt.Errorf("%w: entrypoint not in sources", ErrInvalidServingRecord)
	}
	normSpec, err := json.Marshal(specObj)
	if err != nil {
		return nil, fmt.Errorf("%w: spec", ErrInvalidServingRecord)
	}
	pol := clonePolicies(policies)

	frame, err := json.Marshal(releaseFrame{
		Format: releaseFrameFormat, WorkspaceID: workspaceID, UserID: userID,
		Spec: json.RawMessage(normSpec), Policies: pol,
		Program: runtime.ProgramBundle{
			Entrypoint: prog.Entrypoint, Sources: prog.Sources, Assets: prog.Assets,
			InputSchema: canonSchema["input_schema"], OutputSchema: canonSchema["output_schema"],
		},
	})
	if err != nil {
		return nil, fmt.Errorf("%w: release frame", ErrInvalidServingRecord)
	}
	sum := sha256.Sum256(frame)
	prog.InputSchema = json.RawMessage(canonSchema["input_schema"])
	prog.OutputSchema = json.RawMessage(canonSchema["output_schema"])
	return &ServingRelease{
		ID:          string(PrefixServingRelease) + hex.EncodeToString(sum[:]),
		WorkspaceID: workspaceID,
		UserID:      userID,
		Spec:        json.RawMessage(normSpec),
		Policies:    pol,
		Program:     prog,
		CreatedAt:   createdAt.UTC(),
	}, nil
}

// portablePath matches the engine's portable-path boundary: clean,
// root-relative, no empty/dot/dotdot segments, no absolute, backslash,
// NUL or control characters. A benign name like "foo..bar" is valid.
func portablePath(p string) error {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
		return fmt.Errorf("%w: unsafe path", ErrInvalidServingRecord)
	}
	if path.Clean(p) != p {
		return fmt.Errorf("%w: unsafe path", ErrInvalidServingRecord)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("%w: unsafe path", ErrInvalidServingRecord)
		}
		for _, c := range seg {
			if c < 0x20 || c == 0x7f {
				return fmt.Errorf("%w: unsafe path", ErrInvalidServingRecord)
			}
		}
	}
	return nil
}

func cloneProgram(p runtime.ProgramBundle) runtime.ProgramBundle {
	out := runtime.ProgramBundle{
		Entrypoint:   p.Entrypoint,
		InputSchema:  append(json.RawMessage(nil), p.InputSchema...),
		OutputSchema: append(json.RawMessage(nil), p.OutputSchema...),
		Sources:      map[string][]byte{},
		Assets:       map[string][]byte{},
	}
	for k, v := range p.Sources {
		out.Sources[k] = append([]byte(nil), v...)
	}
	for k, v := range p.Assets {
		out.Assets[k] = append([]byte(nil), v...)
	}
	return out
}

func clonePolicies(p runtime.PolicyBundle) runtime.PolicyBundle {
	out := runtime.PolicyBundle{
		Global: map[string]string{},
		User:   map[string]string{},
	}
	for k, v := range p.Global {
		out.Global[k] = v
	}
	for k, v := range p.User {
		out.User[k] = v
	}
	return out
}

// PersistentInstance is the desired logical instance the scheduler
// reconciles. Generation bumps on enable/disable; LastEpoch fences
// replica claims; ActiveReplicaID points at the ready replica.
type PersistentInstance struct {
	ID              string            `json:"id"`
	WorkspaceID     string            `json:"workspace_id"`
	UserID          string            `json:"user_id"`
	ReleaseID       string            `json:"release_id"`
	Labels          map[string]string `json:"labels,omitempty"`
	Enabled         bool              `json:"enabled"`
	Generation      int64             `json:"generation"`
	LastEpoch       int64             `json:"last_epoch"`
	ActiveReplicaID string            `json:"active_replica_id,omitempty"`
	// ConsecutiveFailures and RetryAfter form the persisted
	// exponential backoff that stops a poison release from hot-looping
	// claims. Reset on a ready report for the current generation.
	ConsecutiveFailures int        `json:"consecutive_failures"`
	RetryAfter          *time.Time `json:"retry_after,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

// ServingNode is a registered worker node. (NodeID, BootID) history is
// recorded in a separate table keyed by the pair.
type ServingNode struct {
	NodeID         string    `json:"node_id"`
	BootID         string    `json:"boot_id"`
	Addr           string    `json:"addr"`
	Incarnation    int64     `json:"incarnation"`
	Capacity       int       `json:"capacity"`
	HeartbeatUntil time.Time `json:"heartbeat_until"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// ServingNodeFence identifies the current incarnation of a node.
type ServingNodeFence struct {
	NodeID      string `json:"node_id"`
	BootID      string `json:"boot_id"`
	Incarnation int64  `json:"incarnation"`
}

// ServingReplica is one ephemeral placement of a persistent instance
// on a node boot. No error message or payload is persisted — only the
// bounded code.
type ServingReplica struct {
	ID string `json:"id"`
	// WorkspaceID is derived from the locked instance at claim time —
	// never request-supplied — for trusted audit/workspace lookups.
	WorkspaceID     string    `json:"workspace_id"`
	InstanceID      string    `json:"instance_id"`
	ReleaseID       string    `json:"release_id"`
	NodeID          string    `json:"node_id"`
	BootID          string    `json:"boot_id"`
	NodeIncarnation int64     `json:"node_incarnation"`
	Generation      int64     `json:"generation"`
	Epoch           int64     `json:"epoch"`
	State           string    `json:"state"`
	LeaseUntil      time.Time `json:"lease_until"`
	ErrorCode       string    `json:"error_code,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// ServingReplicaFence is the exact tuple a CAS report/renew must
// match — every field is checked, nothing is approximate.
type ServingReplicaFence struct {
	ReplicaID       string `json:"replica_id"`
	InstanceID      string `json:"instance_id"`
	ReleaseID       string `json:"release_id"`
	NodeID          string `json:"node_id"`
	BootID          string `json:"boot_id"`
	NodeIncarnation int64  `json:"node_incarnation"`
	Generation      int64  `json:"generation"`
	Epoch           int64  `json:"epoch"`
}

// ServingReplicaClaim is the input to ClaimServingReplica.
type ServingReplicaClaim struct {
	InstanceID      string    `json:"instance_id"`
	ReleaseID       string    `json:"release_id"`
	NodeID          string    `json:"node_id"`
	BootID          string    `json:"boot_id"`
	ReplicaID       string    `json:"replica_id"`
	NodeIncarnation int64     `json:"node_incarnation"`
	Generation      int64     `json:"generation"`
	ExpectedEpoch   int64     `json:"expected_epoch"`
	Now             time.Time `json:"now"`
	LeaseUntil      time.Time `json:"lease_until"`
}

// ServingStore is the durable foundation for the EP-0014 scheduler.
// The API tier owns it; workers never touch SQL. Lock order inside
// every multi-row transaction is node then instance.
type ServingStore interface {
	CreateServingRelease(ctx context.Context, r *ServingRelease) error
	GetServingRelease(ctx context.Context, workspaceID, id string) (*ServingRelease, error)
	CreatePersistentInstance(ctx context.Context, in *PersistentInstance) error
	GetPersistentInstance(ctx context.Context, workspaceID, id string) (*PersistentInstance, error)
	ListPersistentInstances(ctx context.Context, afterID string, limit int) ([]*PersistentInstance, error)
	SetPersistentEnabled(ctx context.Context, workspaceID, id string, expectedGeneration int64, enabled bool, now time.Time) (bool, error)
	RegisterServingNode(ctx context.Context, n *ServingNode, now time.Time) (*ServingNode, error)
	HeartbeatServingNode(ctx context.Context, f ServingNodeFence, now, until time.Time, capacity int) (bool, error)
	ListServingNodes(ctx context.Context, now time.Time) ([]*ServingNode, error)
	ClaimServingReplica(ctx context.Context, c ServingReplicaClaim) (*ServingReplica, error)
	GetServingReplica(ctx context.Context, id string) (*ServingReplica, error)
	// GetCurrentServingReplica returns the replica claimed at the
	// instance's current last_epoch — the scheduler's unlocked
	// snapshot; Claim remains the CAS authority.
	GetCurrentServingReplica(ctx context.Context, instanceID string) (*ServingReplica, error)
	// InsertServingAuditEvents is the report path: identical
	// validation to InsertAuditEvents but ON CONFLICT(id) DO NOTHING
	// per event so deterministic ids dedup report retries.
	InsertServingAuditEvents(ctx context.Context, events []AuditEvent) error
	ListNodeServingReplicas(ctx context.Context, f ServingNodeFence, now time.Time) ([]*ServingReplica, error)
	RenewServingReplica(ctx context.Context, f ServingReplicaFence, now, until time.Time) (bool, error)
	ReportServingReplica(ctx context.Context, f ServingReplicaFence, state, errorCode string, now time.Time) (bool, error)
}

// SanitizeSpecSecrets enforces the boundary rule shared by releases and
// applications: spec.secrets entries are vault references only — never
// plaintext values, never inline sources. In place.
func SanitizeSpecSecrets(specMap map[string]any) error {
	raw, ok := specMap["secrets"]
	if !ok || raw == nil {
		return nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return fmt.Errorf("%w: spec.secrets must be a map", ErrInvalidServingRecord)
	}
	for name, item := range m {
		e, ok := item.(map[string]any)
		if !ok {
			return fmt.Errorf("%w: malformed secret entry", ErrInvalidServingRecord)
		}
		if !secrets.ValidName(name) {
			return fmt.Errorf("%w: invalid secret name", ErrInvalidServingRecord)
		}
		if v, has := e["value"]; has && v != nil {
			if sv, _ := v.(string); sv != "" {
				return fmt.Errorf("%w: plaintext secret value", ErrInvalidServingRecord)
			}
			if _, isStr := v.(string); !isStr {
				return fmt.Errorf("%w: malformed secret value", ErrInvalidServingRecord)
			}
			delete(e, "value")
		}
		src, _ := e["source"].(string)
		if src != "" && src != string(types.SecretVault) {
			return fmt.Errorf("%w: inline secret source", ErrInvalidServingRecord)
		}
		ref, _ := e["ref"].(string)
		if ref == "" {
			return fmt.Errorf("%w: secret ref required", ErrInvalidServingRecord)
		}
		if _, isStr := e["ref"].(string); !isStr {
			return fmt.Errorf("%w: malformed secret ref", ErrInvalidServingRecord)
		}
		if raw, has := e["allowed_domains"]; has && raw != nil {
			if _, ok := raw.([]any); !ok {
				return fmt.Errorf("%w: malformed secret domains", ErrInvalidServingRecord)
			}
		}
		e["source"] = string(types.SecretVault)
	}
	return nil
}
