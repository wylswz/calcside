// Package store defines the persistence layer: models, the Store
// interface, ID generation, and the Open factory.
package store

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ErrNotFound is returned when a requested row does not exist.
var ErrNotFound = errors.New("store: not found")

// ErrConflict is returned when a uniqueness constraint is violated.
var ErrConflict = errors.New("store: conflict")

// ID prefixes.
const (
	PrefixUser      = "usr_"
	PrefixAPIKey    = "key_"
	PrefixInstance  = "ins_"
	PrefixExecution = "exe_"
	PrefixPolicy    = "pol_"
	PrefixAudit     = "aud_"
	PrefixSecret    = "sec_"
)

const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// NewID returns a prefixed random ID (prefix + 24 base62 chars).
func NewID(prefix string) string {
	b := make([]byte, 24)
	raw := make([]byte, 24)
	_, _ = rand.Read(raw)
	for i := range b {
		b[i] = alphabet[int(raw[i])%len(alphabet)]
	}
	return prefix + string(b)
}

type User struct {
	ID          string    `json:"id"`
	Email       string    `json:"email"`
	Name        string    `json:"name"`
	GoogleSub   string    `json:"google_sub,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	LastLoginAt time.Time `json:"last_login_at"`
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
}

type Session struct {
	Hash      string    `json:"-"`
	UserID    string    `json:"user_id"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Instance status values.
const (
	StatusRunning = "running"
	StatusDeleted = "deleted"
	StatusExpired = "expired"
	StatusLost    = "lost"
)

type Instance struct {
	ID           string            `json:"id"`
	UserID       string            `json:"user_id"`
	Spec         json.RawMessage   `json:"spec"` // serialized as a JSON object
	Labels       map[string]string `json:"labels"`
	Status       string            `json:"status"`
	CreatedAt    time.Time         `json:"created_at"`
	LastActiveAt time.Time         `json:"last_active_at"`
	ExpiresAt    time.Time         `json:"expires_at"`
	EndedAt      *time.Time        `json:"ended_at,omitempty"`
}

// Execution status values.
const (
	ExecOK    = "ok"
	ExecError = "error"
)

type Execution struct {
	ID          string    `json:"id"`
	InstanceID  string    `json:"instance_id"`
	UserID      string    `json:"user_id"`
	CodeSHA256  string    `json:"code_sha256"`
	CodeSnippet string    `json:"code_snippet"`
	Status      string    `json:"status"`
	ErrorType   string    `json:"error_type,omitempty"`
	DurationMs  int64     `json:"duration_ms"`
	Steps       uint64    `json:"steps"`
	OutputBytes int64     `json:"output_bytes"`
	CreatedAt   time.Time `json:"created_at"`
}

// Audit decisions.
const (
	DecisionAllow = "allow"
	DecisionDeny  = "deny"
)

type AuditEvent struct {
	ID         string    `json:"id"`
	Ts         time.Time `json:"ts"`
	UserID     string    `json:"user_id"`
	InstanceID string    `json:"instance_id"`
	ExecID     string    `json:"exec_id"`
	Capability string    `json:"capability"`
	Op         string    `json:"op"`
	Args       string    `json:"args"` // JSON, truncated to 4KB
	Phase      string    `json:"phase"`
	Decision   string    `json:"decision"`
	Reason     string    `json:"reason"`
	Error      string    `json:"error"`
	DurationMs int64     `json:"duration_ms"`
}

type AuditFilter struct {
	UserID     string
	InstanceID string
	ExecID     string
	Limit      int
	Before     *time.Time
}

type Policy struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	Name      string    `json:"name"`
	Rego      string    `json:"rego"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Secret is a vault secret. Ciphertext is never serialized by the API.
type Secret struct {
	ID             string    `json:"id"`
	UserID         string    `json:"user_id"`
	Name           string    `json:"name"`
	Ciphertext     []byte    `json:"-"`
	AllowedDomains []string  `json:"allowed_domains"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type Store interface {
	UpsertUserByEmail(ctx context.Context, email, name, googleSub string) (*User, error)
	GetUser(ctx context.Context, id string) (*User, error)

	CreateAPIKey(ctx context.Context, k *APIKey) error
	ListAPIKeys(ctx context.Context, userID string) ([]*APIKey, error)
	GetAPIKeyByHash(ctx context.Context, hash string) (*APIKey, error)
	RevokeAPIKey(ctx context.Context, userID, id string) error
	TouchAPIKey(ctx context.Context, id string, at time.Time) error

	CreateSession(ctx context.Context, s *Session) error
	GetSession(ctx context.Context, hash string) (*Session, error)
	DeleteSession(ctx context.Context, hash string) error
	DeleteExpiredSessions(ctx context.Context) (int, error)

	CreateInstance(ctx context.Context, in *Instance) error
	GetInstance(ctx context.Context, id string) (*Instance, error)
	ListInstances(ctx context.Context, userID, status string) ([]*Instance, error)
	UpdateInstance(ctx context.Context, in *Instance) error
	MarkRunningAsLost(ctx context.Context) (int, error)

	CreateExecution(ctx context.Context, e *Execution) error
	ListExecutions(ctx context.Context, instanceID string, limit int) ([]*Execution, error)

	InsertAuditEvents(ctx context.Context, evs []AuditEvent) error
	ListAuditEvents(ctx context.Context, f AuditFilter) ([]*AuditEvent, error)

	CreatePolicy(ctx context.Context, p *Policy) error
	GetPolicy(ctx context.Context, id string) (*Policy, error)
	ListPolicies(ctx context.Context, userID string) ([]*Policy, error)
	UpdatePolicy(ctx context.Context, p *Policy) error
	DeletePolicy(ctx context.Context, id string) error

	CreateSecret(ctx context.Context, s *Secret) error
	GetSecret(ctx context.Context, id string) (*Secret, error)
	GetSecretByName(ctx context.Context, userID, name string) (*Secret, error)
	ListSecrets(ctx context.Context, userID string) ([]*Secret, error)
	UpdateSecret(ctx context.Context, s *Secret) error
	DeleteSecret(ctx context.Context, id string) error

	Close() error
}

var drivers = map[string]func(ctx context.Context, dsn string) (Store, error){}

// RegisterDriver is called by driver packages (e.g. store/sqlite) in init.
func RegisterDriver(name string, fn func(ctx context.Context, dsn string) (Store, error)) {
	drivers[name] = fn
}

// Open creates a Store for the given registered driver.
func Open(ctx context.Context, driver, dsn string) (Store, error) {
	fn, ok := drivers[driver]
	if !ok {
		return nil, fmt.Errorf("store: unknown driver %q", driver)
	}
	return fn(ctx, dsn)
}
