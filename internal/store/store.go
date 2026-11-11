// Package store defines the persistence layer: models, the Store
// interface, ID generation, and the Open factory.
package store

import (
	"calcside/internal/types"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrNotFound is returned when a requested row does not exist.
var ErrNotFound = errors.New("store: not found")

// ErrConflict is returned when a uniqueness constraint is violated.
var ErrConflict = errors.New("store: conflict")

// ErrNotClaimable is returned by ClaimInstanceOp when the row exists
// and no live lease blocks it, but its status is not claimable or its
// TTL has already elapsed — the caller re-reads the row to classify.
var ErrNotClaimable = errors.New("store: instance not claimable")

// ErrApplicationDraftConflict is a stale draft CAS on the reviewed
// application publish path.
var ErrApplicationDraftConflict = errors.New("store: application draft changed")

// ErrApplicationBaseConflict is the reviewed-publish base sentinel:
// the app's current published release differs from the release the
// reviewer compared against.
var ErrApplicationBaseConflict = errors.New("store: application published base changed")

// IDPrefix is the fixed string identifying the entity kind in an ID.
type IDPrefix string

// ID prefixes.
const (
	PrefixUser        IDPrefix = "usr_"
	PrefixWorkspace   IDPrefix = "ws_"
	PrefixAPIKey      IDPrefix = "key_"
	PrefixInstance    IDPrefix = "ins_"
	PrefixExecution   IDPrefix = "exe_"
	PrefixPolicy      IDPrefix = "pol_"
	PrefixLLMProvider IDPrefix = "llmp_"
	PrefixAudit       IDPrefix = "aud_"
	PrefixSecret      IDPrefix = "sec_"
)

const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// NewID returns a prefixed random ID (prefix + 24 base62 chars).
func NewID(prefix IDPrefix) string {
	b := make([]byte, 24)
	raw := make([]byte, 24)
	_, _ = rand.Read(raw)
	for i := range b {
		b[i] = alphabet[int(raw[i])%len(alphabet)]
	}
	return string(prefix) + string(b)
}

// PersonalWorkspaceID is the deterministic personal workspace for a
// user: ws_ + the user id's unique tail.
func PersonalWorkspaceID(userID string) string {
	return string(PrefixWorkspace) + strings.TrimPrefix(userID, string(PrefixUser))
}

type Workspace struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Personal  bool      `json:"personal"`
	CreatedAt time.Time `json:"created_at"`
}

// Member roles, weakest to strongest (typed enum in internal/types).
const (
	RoleViewer = types.RoleViewer
	RoleEditor = types.RoleEditor
	RoleOwner  = types.RoleOwner
)

type Member struct {
	WorkspaceID string     `json:"workspace_id"`
	UserID      string     `json:"user_id"`
	Role        types.Role `json:"role"`
}

// Membership is one workspace a user belongs to, with their role.
type Membership struct {
	Workspace *Workspace `json:"workspace"`
	Role      types.Role `json:"role"`
}

// MemberInfo is a member row joined with the user's identity.
type MemberInfo struct {
	UserID string     `json:"user_id"`
	Email  string     `json:"email"`
	Name   string     `json:"name"`
	Role   types.Role `json:"role"`
}

// Share links one instance to one user.
type Share struct {
	InstanceID string     `json:"instance_id"`
	UserID     string     `json:"user_id"`
	Role       types.Role `json:"role"`
	CreatedBy  string     `json:"created_by"`
	CreatedAt  time.Time  `json:"created_at"`
}

// ShareInfo is a share row joined with the user's identity.
type ShareInfo struct {
	Share
	Email string `json:"email"`
	Name  string `json:"name"`
}

// Workspace change kinds written to workspace_changes (typed enum in
// internal/types).
const (
	ChangeMemberAdded   = types.ChangeMemberAdded
	ChangeRoleChanged   = types.ChangeRoleChanged
	ChangeMemberRemoved = types.ChangeMemberRemoved
	ChangeShareAdded    = types.ChangeShareAdded
	ChangeShareRemoved  = types.ChangeShareRemoved
	ChangeRenamed       = types.ChangeRenamed
)

// WorkspaceChange records one membership/share/rename event.
type WorkspaceChange struct {
	ID          string           `json:"id"`
	WorkspaceID string           `json:"workspace_id"`
	ByUserID    string           `json:"by_user_id"`
	Change      types.ChangeKind `json:"change"`
	UserID      string           `json:"user_id,omitempty"`
	InstanceID  string           `json:"instance_id,omitempty"`
	Role        types.Role       `json:"role,omitempty"`
	CreatedAt   time.Time        `json:"created_at"`
}

type User struct {
	ID           string    `json:"id"`
	Email        string    `json:"email"`
	Name         string    `json:"name"`
	GoogleSub    string    `json:"google_sub,omitempty"`
	IsAdmin      bool      `json:"is_admin"`
	Username     string    `json:"username,omitempty"`
	PasswordHash string    `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
	LastLoginAt  time.Time `json:"last_login_at"`
}

type APIKey struct {
	ID         string     `json:"id"`
	UserID     string     `json:"user_id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Hash       string     `json:"-"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`

	// Spec is the normalized InstanceSpec this key may create; nil = full
	// access. MaxInstances bounds live instances for a restricted key.
	Spec         json.RawMessage `json:"-"`
	MaxInstances int             `json:"-"`
	WorkspaceID  string          `json:"-"`
	// BoundID binds the key to a single resource — an instance or an
	// application — identified by its prefixed id (nullable).
	BoundID  string `json:"bound_id,omitempty"`
	ViewOnly bool   `json:"-"`
}

type Session struct {
	Hash      string    `json:"-"`
	UserID    string    `json:"user_id"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

type Instance struct {
	ID           string               `json:"id"`
	UserID       string               `json:"user_id"`
	Spec         json.RawMessage      `json:"spec"` // serialized as a JSON object
	Labels       map[string]string    `json:"labels"`
	Status       types.InstanceStatus `json:"status"`
	CreatedAt    time.Time            `json:"created_at"`
	LastActiveAt time.Time            `json:"last_active_at"`
	ExpiresAt    time.Time            `json:"expires_at"`
	EndedAt      *time.Time           `json:"ended_at,omitempty"`

	// Placement: the node holding this instance's live state and the
	// fencing epoch of that binding. Internal only — never serialized
	// to API clients (dto.NewInstance maps fields explicitly).
	NodeID     string `json:"-"`
	LeaseEpoch int64  `json:"-"`

	// KeyID is the restricted API key that created the instance; empty
	// for console sessions and full-access keys.
	KeyID string `json:"-"`
	// WorkspaceID owns the instance; user_id stays "created by".
	WorkspaceID string `json:"-"`

	// Notebook state (EP-0022). Incarnation counts runtime processes
	// under this instance id: 1 at creation, +1 per restoration.
	// RestoredAt marks the last lost→running restoration. NotebookRev
	// is the CAS revision clients save sections against. RunSeq numbers
	// section runs within an incarnation. Drift counts scratch execs
	// that changed globals outside the notebook. OpToken/OpExpiresAt
	// are the per-instance operation lease serializing execs, runs,
	// saves and restoration.
	Incarnation int64      `json:"-"`
	RestoredAt  *time.Time `json:"-"`
	NotebookRev int64      `json:"-"`
	RunSeq      int64      `json:"-"`
	Drift       int64      `json:"-"`
	OpToken     string     `json:"-"`
	OpExpiresAt *time.Time `json:"-"`
}

// Rerun policies for notebook sections.
const (
	RerunSafe = "safe" // re-run whenever stale in bulk runs
	RerunOnce = "once" // skipped by bulk runs after a success
)

// NotebookSection is one durable named section of an instance's
// notebook: ordered code plus its static analysis and last run facts.
type NotebookSection struct {
	InstanceID string `json:"-"`
	Name       string `json:"name"`
	Position   int64  `json:"position"`

	Description string `json:"description"`
	Code        string `json:"code"`
	CodeSHA256  string `json:"-"`
	Rerun       string `json:"rerun"`

	// Static analysis of Code: top-level names it defines and reads,
	// plus observed capability op names from audit.
	Defines []string `json:"defines"`
	Uses    []string `json:"uses"`
	Effects []string `json:"effects"`

	// DependsOn records which earlier sections provide the names in
	// Uses (section names, never define names). It is provenance for
	// staleness: when a provider is edited, moved or deleted, its
	// dependents go stale even if the define name itself changed.
	DependsOn []string `json:"depends_on"`

	// Last run facts within the current or a previous incarnation.
	// LastCodeSHA256 is the code hash that last ran — comparing it to
	// CodeSHA256 detects edits that were never executed (stale).
	LastExecID      string     `json:"last_exec_id,omitempty"`
	LastIncarnation int64      `json:"last_incarnation"`
	LastRunSeq      int64      `json:"last_run_seq"`
	LastStatus      string     `json:"last_status,omitempty"`
	LastCodeSHA256  string     `json:"-"`
	LastRunAt       *time.Time `json:"last_run_at,omitempty"`
	// SucceededBefore records a past success so `once` sections can be
	// skipped by bulk runs even after edits.
	SucceededBefore bool `json:"succeeded_before"`

	CreatedBy string    `json:"created_by"`
	UpdatedBy string    `json:"updated_by"`
	EditedAt  time.Time `json:"edited_at"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// InstanceFilter selects instances by workspace, creating user
// (per-user quota only), status and creating key.
type InstanceFilter struct {
	WorkspaceID string
	UserID      string
	Status      types.InstanceStatus
	KeyID       string
	// KeyInstanceID filters to one instance for instance-bound keys.
	KeyInstanceID string
}

// ExecActivity is the per-instance run summary for the list view.
type ExecActivity struct {
	InstanceID string     `json:"instance_id"`
	Runs       int        `json:"runs"`
	DeniedRuns int        `json:"denied_runs"`
	FailedRuns int        `json:"failed_runs"`
	LastRunAt  *time.Time `json:"last_run_at,omitempty"`
	LastStatus string     `json:"last_status,omitempty"`
}

type Execution struct {
	ID          string              `json:"id"`
	InstanceID  string              `json:"instance_id"`
	UserID      string              `json:"user_id"`
	CodeSHA256  string              `json:"code_sha256"`
	CodeSnippet string              `json:"code_snippet"`
	Code        string              `json:"-"` // full code; served only by the execution detail endpoint
	Status      types.ExecStatus    `json:"status"`
	ErrorType   types.ExecErrorType `json:"error_type,omitempty"` // "" = no error
	DurationMs  int64               `json:"duration_ms"`
	Steps       uint64              `json:"steps"`
	OutputBytes int64               `json:"output_bytes"`
	Via         types.AuthKind      `json:"via,omitempty"` // "" = recorded before this field existed
	// Detail-only fields; served only by the execution detail endpoint.
	Output          string    `json:"-"`
	OutputTruncated bool      `json:"-"`
	ErrorMessage    string    `json:"-"`
	ErrorBacktrace  string    `json:"-"`
	CreatedAt       time.Time `json:"created_at"`

	// Notebook provenance (EP-0022): empty SectionName marks a scratch
	// exec; Incarnation records which runtime process ran it.
	SectionName string `json:"section_name,omitempty"`
	Incarnation int64  `json:"incarnation,omitempty"`
}

type AuditEvent struct {
	ID          string               `json:"id"`
	WorkspaceID string               `json:"workspace_id"`
	Ts          time.Time            `json:"ts"`
	UserID      string               `json:"user_id"`
	InstanceID  string               `json:"instance_id"`
	ExecID      string               `json:"exec_id"`
	Capability  types.CapabilityName `json:"capability"`
	Op          types.Op             `json:"op"`
	Args        string               `json:"args"` // JSON, truncated to 4KB
	Phase       types.Phase          `json:"phase"`
	Decision    types.Decision       `json:"decision"`
	Reason      string               `json:"reason"`
	Error       string               `json:"error"`
	DurationMs  int64                `json:"duration_ms"`
}

type AuditFilter struct {
	// WorkspaceID scopes via instance_id IN (instances of the workspace).
	WorkspaceID string
	InstanceID  string
	ExecID      string
	Limit       int
	Before      *time.Time
}

type Policy struct {
	ID          string    `json:"id"`
	UserID      string    `json:"user_id"`
	WorkspaceID string    `json:"-"`
	Name        string    `json:"name"`
	Rego        string    `json:"rego"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// LLMProvider is a workspace-level named provider extension: an ext
// source, its model catalogue and effective config, snapshotted into
// instances whose llm capability ext selects it by name.
type LLMProvider struct {
	ID          string          `json:"id"`
	UserID      string          `json:"user_id"`
	WorkspaceID string          `json:"-"`
	Name        string          `json:"name"`
	Source      string          `json:"source"`
	Sum         string          `json:"sum,omitempty"`
	Models      []string        `json:"models"`
	Config      json.RawMessage `json:"config"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

// Secret is a vault secret. Ciphertext is never serialized by the API.
type Secret struct {
	ID             string    `json:"id"`
	UserID         string    `json:"user_id"`
	WorkspaceID    string    `json:"-"`
	Name           string    `json:"name"`
	Ciphertext     []byte    `json:"-"`
	AllowedDomains []string  `json:"allowed_domains"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type Store interface {
	UpsertUserByEmail(ctx context.Context, email, name, googleSub string) (*User, error)
	BootstrapAdminUser(ctx context.Context, username, email, passwordHash string) (*User, error)
	GetUserByUsername(ctx context.Context, username string) (*User, error)
	HasPasswordUsers(ctx context.Context) (bool, error)
	UpdateUserPassword(ctx context.Context, userID, oldHash, newHash string) error
	GetUser(ctx context.Context, id string) (*User, error)

	CreateAPIKey(ctx context.Context, k *APIKey) error
	ListAPIKeys(ctx context.Context, workspaceID string) ([]*APIKey, error)
	GetAPIKeyByHash(ctx context.Context, hash string) (*APIKey, error)
	GetAPIKey(ctx context.Context, id string) (*APIKey, error)
	RevokeAPIKey(ctx context.Context, userID, id string) error
	TouchAPIKey(ctx context.Context, id string, at time.Time) error

	CreateSession(ctx context.Context, s *Session) error
	CreatePasswordSession(ctx context.Context, s *Session, passwordHash string) error
	GetSession(ctx context.Context, hash string) (*Session, error)
	DeleteSession(ctx context.Context, hash string) error
	DeleteExpiredSessions(ctx context.Context) (int, error)

	CreateInstance(ctx context.Context, in *Instance) error
	GetInstance(ctx context.Context, id string) (*Instance, error)
	ListInstances(ctx context.Context, f InstanceFilter) ([]*Instance, error)
	UpdateInstance(ctx context.Context, in *Instance) error
	// ListExpiredInstances returns running instances whose sliding TTL
	// elapsed before the given time. The API tier owns expiry as a
	// status transition — an execution node cannot write it — so this
	// drives the reaper.
	ListExpiredInstances(ctx context.Context, before time.Time, limit int) ([]*Instance, error)
	// MarkRunningAsLostForNode marks running instances bound to nodeID —
	// plus unbound orphans, which no node can own — as lost. Any node may
	// sweep orphans; a bound instance is never another node's to mark.
	MarkRunningAsLostForNode(ctx context.Context, nodeID string) (int, error)
	// BindInstance atomically claims a running instance for a node,
	// bumping the fencing epoch. Returns ok=false when the instance is
	// missing, not running, or already bound to another node.
	BindInstance(ctx context.Context, id, nodeID string) (epoch int64, ok bool, err error)
	// ReleaseInstance drops a binding, but only when nodeID and epoch
	// still match — a stale holder cannot release a newer binding.
	ReleaseInstance(ctx context.Context, id, nodeID string, epoch int64) (ok bool, err error)

	// --- instance operation lease + notebook (EP-0022) ---
	// ClaimInstanceOp takes the instance's operation lease for ttl when
	// the row's status is one of allowed (empty = any non-terminal) and
	// its TTL has not elapsed — an expired row can never be claimed, so
	// no op can renew it back to life. Returns ErrConflict while
	// another token holds a live lease, ErrNotClaimable when the status
	// or expiry forbids the claim, ErrNotFound when missing.
	ClaimInstanceOp(ctx context.Context, id, token string, ttl time.Duration, allowed ...types.InstanceStatus) (*Instance, error)
	// RefreshInstanceOp extends the lease while token still matches.
	RefreshInstanceOp(ctx context.Context, id, token string, ttl time.Duration) (bool, error)
	// ReleaseInstanceOp clears the lease when token still matches.
	ReleaseInstanceOp(ctx context.Context, id, token string) error
	// MarkInstanceLost CASes running→lost under the held op token.
	MarkInstanceLost(ctx context.Context, id, token string, endedAt time.Time) (bool, error)
	// TouchInstanceLive renews last_active/expires under the op token
	// and adds driftDelta to the drift counter.
	TouchInstanceLive(ctx context.Context, id, token string, lastActive, expiresAt time.Time, driftDelta int64) (bool, error)
	// BumpRunSeq increments and returns the instance's run sequence.
	BumpRunSeq(ctx context.Context, id, token string) (int64, error)
	// CommitRestore CASes lost→running for a restoration: binds
	// nodeID/epoch, sets incarnation+1, restored_at, clears drift and
	// the op lease fields the caller no longer needs. Fails when the
	// row went terminal or expired.
	CommitRestore(ctx context.Context, id, token, nodeID string, epoch, incarnation int64, restoredAt time.Time) (bool, error)
	// ClearRestoreBinding drops a just-claimed restore binding when the
	// runtime create failed, leaving the row lost.
	ClearRestoreBinding(ctx context.Context, id, token string) error
	// SettleInstance marks a terminal status and clears the op lease in
	// one write — UpdateInstance deliberately does not persist op
	// fields, so terminal transitions must not leave a stale lease
	// blocking claims until op_expires_at lapses.
	SettleInstance(ctx context.Context, id string, status types.InstanceStatus, endedAt time.Time) error

	// SaveNotebookSection upserts one section under revision CAS:
	// expectedRev must equal the instance's notebook_rev. `after`
	// positions the section directly after the named one ("" appends).
	// Returns the saved row and the new notebook revision.
	SaveNotebookSection(ctx context.Context, instanceID string, expectedRev int64, row *NotebookSection, after string) (*NotebookSection, int64, error)
	GetNotebookSection(ctx context.Context, instanceID, name string) (*NotebookSection, error)
	ListNotebookSections(ctx context.Context, instanceID string) ([]*NotebookSection, error)
	// DeleteNotebookSection removes a section under revision CAS and
	// returns the names of sections still reading its defines.
	DeleteNotebookSection(ctx context.Context, instanceID, name string, revision int64) ([]string, int64, error)
	// StampSectionRun records a section's last-run facts and observed
	// effects, bumps the notebook revision, and clears `once` skips —
	// but only while the instance is still running under the caller's
	// op token in the given incarnation. A late result after the row
	// went terminal, was restored into a new incarnation, or lost its
	// lease returns ErrConflict and writes nothing.
	// codeSHA is the hash of the code that ran (the section's
	// CodeSHA256 at run time).
	StampSectionRun(ctx context.Context, instanceID, name, execID, status string, incarnation, runSeq int64, at time.Time, effects []string, succeeded bool, codeSHA, token string) (int64, error)

	CreateExecution(ctx context.Context, e *Execution) error
	GetExecution(ctx context.Context, id string) (*Execution, error)
	ListExecutions(ctx context.Context, instanceID string, limit int) ([]*Execution, error)

	InsertAuditEvents(ctx context.Context, evs []AuditEvent) error
	ListAuditEvents(ctx context.Context, f AuditFilter) ([]*AuditEvent, error)
	// ExecutionActivity aggregates run stats per instance id in one query.
	ExecutionActivity(ctx context.Context, ids []string) (map[string]*ExecActivity, error)

	CreatePolicy(ctx context.Context, p *Policy) error
	GetPolicy(ctx context.Context, id string) (*Policy, error)
	ListPolicies(ctx context.Context, workspaceID string) ([]*Policy, error)
	UpdatePolicy(ctx context.Context, p *Policy) error
	DeletePolicy(ctx context.Context, id string) error

	CreateLLMProvider(ctx context.Context, p *LLMProvider) error
	GetLLMProvider(ctx context.Context, id string) (*LLMProvider, error)
	ListLLMProviders(ctx context.Context, workspaceID string) ([]*LLMProvider, error)
	UpdateLLMProvider(ctx context.Context, p *LLMProvider) error
	DeleteLLMProvider(ctx context.Context, id string) error

	CreateSecret(ctx context.Context, s *Secret) error
	GetSecret(ctx context.Context, id string) (*Secret, error)
	GetSecretByName(ctx context.Context, workspaceID, name string) (*Secret, error)
	ListSecrets(ctx context.Context, workspaceID string) ([]*Secret, error)
	UpdateSecret(ctx context.Context, s *Secret) error
	DeleteSecret(ctx context.Context, id string) error

	CreateWorkspace(ctx context.Context, w *Workspace) error
	GetWorkspace(ctx context.Context, id string) (*Workspace, error)
	UpdateWorkspace(ctx context.Context, w *Workspace) error
	DeleteWorkspace(ctx context.Context, id string) error
	// LockWorkspace locks the workspace row inside a Tx (SELECT FOR UPDATE).
	LockWorkspace(ctx context.Context, id string) error
	AddMember(ctx context.Context, m *Member) error
	GetMember(ctx context.Context, workspaceID, userID string) (*Member, error)
	ListMembers(ctx context.Context, workspaceID string) ([]*MemberInfo, error)
	ListMemberships(ctx context.Context, userID string) ([]*Membership, error)
	UpdateMember(ctx context.Context, m *Member) error
	DeleteMember(ctx context.Context, workspaceID, userID string) error
	CountMembers(ctx context.Context, workspaceID string, role types.Role) (int, error)

	CreateShare(ctx context.Context, sh *Share) error
	DeleteShare(ctx context.Context, instanceID, userID string) error
	GetShare(ctx context.Context, instanceID, userID string) (*Share, error)
	ListShares(ctx context.Context, instanceID string) ([]*ShareInfo, error)
	ListSharedInstances(ctx context.Context, userID string) ([]*Share, error)

	InsertChange(ctx context.Context, c *WorkspaceChange) error
	ListChanges(ctx context.Context, workspaceID string, limit int) ([]*WorkspaceChange, error)

	RevokeUserKeys(ctx context.Context, workspaceID, userID string) error
	// RevokeBoundKeys revokes every key bound to the given resource id
	// (instance or application); used when that resource dies.
	RevokeBoundKeys(ctx context.Context, boundID string) error
	GetUserByEmail(ctx context.Context, email string) (*User, error)

	// Tx runs fn against one store connection in a single transaction.
	ServingStore
	ApplicationStore
	RunStore
	ExtensionArtifactStore
	ModuleStore

	Tx(ctx context.Context, fn func(Store) error) error

	Close() error
}

var drivers = map[types.StoreDriver]func(ctx context.Context, dsn string) (Store, error){}

// RegisterDriver is called by driver packages (e.g. store/sqlite) in init.
func RegisterDriver(name types.StoreDriver, fn func(ctx context.Context, dsn string) (Store, error)) {
	drivers[name] = fn
}

// Open creates a Store for the given registered driver.
func Open(ctx context.Context, driver types.StoreDriver, dsn string) (Store, error) {
	fn, ok := drivers[driver]
	if !ok {
		return nil, fmt.Errorf("store: unknown driver %q", driver)
	}
	return fn(ctx, dsn)
}
