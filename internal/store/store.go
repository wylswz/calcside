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

// IDPrefix is the fixed string identifying the entity kind in an ID.
type IDPrefix string

// ID prefixes.
const (
	PrefixUser      IDPrefix = "usr_"
	PrefixWorkspace IDPrefix = "ws_"
	PrefixAPIKey    IDPrefix = "key_"
	PrefixInstance  IDPrefix = "ins_"
	PrefixExecution IDPrefix = "exe_"
	PrefixPolicy    IDPrefix = "pol_"
	PrefixAudit     IDPrefix = "aud_"
	PrefixSecret    IDPrefix = "sec_"
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
	// InstanceID binds the key to a single instance (nullable).
	InstanceID string `json:"instance_id,omitempty"`
	ViewOnly   bool   `json:"-"`
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
	RevokeKeysByInstance(ctx context.Context, instanceID string) error
	GetUserByEmail(ctx context.Context, email string) (*User, error)

	// Tx runs fn against one store connection in a single transaction.
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
