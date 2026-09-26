package runtime

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"calcside/internal/types"
)

// Owner is the already-resolved principal an instance belongs to. The
// execution tier does not authenticate — the API tier does that and
// passes the result — but it does re-check this against the owner
// recorded at create time, so an API-tier bug cannot become cross-tenant
// execution.
type Owner struct {
	UserID string `json:"user_id"`
	Email  string `json:"email"`
}

// Secret is a resolved secret handed to the execution tier. Values are
// plaintext because the node must inject them into outbound requests at
// send time; vault decryption and allowlist narrowing already happened
// on the API tier.
//
// A transport carrying these must be mutually authenticated and
// encrypted, and must never log the request body. String and LogValue
// are defined so accidental formatting cannot leak the value.
type Secret struct {
	Name           string             `json:"name"`
	Value          string             `json:"value"`
	AllowedDomains []string           `json:"allowed_domains,omitempty"`
	Source         types.SecretSource `json:"source"`
}

func (s Secret) String() string {
	return fmt.Sprintf("Secret{Name:%s Domains:%v Value:[REDACTED]}", s.Name, s.AllowedDomains)
}

func (s Secret) LogValue() slog.Value {
	return slog.GroupValue(slog.String("name", s.Name), slog.String("value", "[REDACTED]"))
}

// PolicyBundle is the policy snapshot taken when the instance is
// created. Global modules travel with the request instead of being read
// from each node's --policy-dir, so every node evaluates the identical
// snapshot and adding a node cannot silently change enforcement.
type PolicyBundle struct {
	// Global maps module name to rego source, compiled with full builtins.
	Global map[string]string `json:"global,omitempty"`
	// User maps policy ID to rego source, compiled with the restricted
	// builtin set.
	User map[string]string `json:"user,omitempty"`
}

// CreateRequest is self-contained: everything the node needs to stand up
// the instance, with no lookups of its own.
type CreateRequest struct {
	// InstanceID is minted by the API tier, which has already written
	// the row this request corresponds to.
	InstanceID string            `json:"instance_id"`
	Owner      Owner             `json:"owner"`
	Labels     map[string]string `json:"labels,omitempty"`
	// Spec is the normalized, validated spec — identical to the JSON
	// persisted for the instance, so inline secret values are already
	// stripped from it and travel in Secrets instead.
	Spec     json.RawMessage `json:"spec"`
	Policies PolicyBundle    `json:"policies"`
	Secrets  []Secret        `json:"secrets,omitempty"`
	// ExpiresAt is computed by the API tier, which owns the clock. The
	// node keeps it only to drive a local memory-reclaim safety net.
	ExpiresAt time.Time `json:"expires_at"`
}

func (r *CreateRequest) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("instance_id", r.InstanceID),
		slog.String("user_id", r.Owner.UserID),
		slog.Int("secrets", len(r.Secrets)),
	)
}

type CreateResponse struct {
	// Capabilities granted to the instance, in registry order, including
	// implicitly granted ones such as io.
	Capabilities []types.CapabilityName `json:"capabilities,omitempty"`
}

// ExecRequest runs one chunk of Starlark.
type ExecRequest struct {
	InstanceID string `json:"instance_id"`
	Owner      Owner  `json:"owner"`
	// ExecID is minted by the API tier and doubles as the idempotency
	// key: a node that has already run this ExecID must return the
	// recorded result rather than run the code again. Exec has external
	// side effects, so a transport-level retry without this would let a
	// single request hit a third-party API twice.
	ExecID string `json:"exec_id"`
	Code   string `json:"code"`
	// TimeoutMs overrides the instance default when > 0.
	TimeoutMs int64 `json:"timeout_ms,omitempty"`
	// RenewedExpiresAt is the sliding TTL deadline the API tier has
	// computed for after this exec.
	RenewedExpiresAt time.Time `json:"renewed_expires_at"`
}

func (r *ExecRequest) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("instance_id", r.InstanceID),
		slog.String("exec_id", r.ExecID),
		slog.Int("code_bytes", len(r.Code)),
	)
}

// ExecError is the structured script failure. It mirrors the engine's
// error shape without making the API tier depend on the engine.
type ExecError struct {
	Type      types.ExecErrorType `json:"type"`
	Message   string              `json:"message"`
	Backtrace string              `json:"backtrace,omitempty"`
}

// ExecResult is the outcome of one exec. Output and error text have
// already been scrubbed of secret values by the node.
type ExecResult struct {
	ExecID     string     `json:"exec_id"`
	Output     string     `json:"output"`
	Error      *ExecError `json:"error"`
	DurationMs int64      `json:"duration_ms"`
	Steps      uint64     `json:"steps"`
}

type ExecResponse struct {
	Result ExecResult `json:"result"`
	Audit  AuditBatch `json:"audit"`
}

// AuditEvent is one gated capability call, produced by the node and
// persisted by the API tier. Args is already JSON, truncated, and
// scrubbed; it never holds file contents or response bodies.
type AuditEvent struct {
	Ts         time.Time            `json:"ts"`
	UserID     string               `json:"user_id"`
	InstanceID string               `json:"instance_id"`
	ExecID     string               `json:"exec_id"`
	Capability types.CapabilityName `json:"capability"`
	Op         types.Op             `json:"op"`
	Args       string               `json:"args"`
	Phase      types.Phase          `json:"phase"`
	Decision   types.Decision       `json:"decision"`
	Reason     string               `json:"reason"`
	Error      string               `json:"error"`
	DurationMs int64                `json:"duration_ms"`
}

// AuditBatch rides back on the response that produced it. Audit is a
// security log, so an overflow is reported rather than silently lost.
type AuditBatch struct {
	Events []AuditEvent `json:"events,omitempty"`
	// Dropped counts events discarded because the batch hit its cap.
	Dropped int `json:"dropped,omitempty"`
}

type KeepaliveRequest struct {
	InstanceID       string    `json:"instance_id"`
	Owner            Owner     `json:"owner"`
	RenewedExpiresAt time.Time `json:"renewed_expires_at"`
}

type KeepaliveResponse struct{}

type DeleteRequest struct {
	InstanceID string `json:"instance_id"`
	Owner      Owner  `json:"owner"`
}

type DeleteResponse struct{}

type BrowseRequest struct {
	InstanceID string `json:"instance_id"`
	Owner      Owner  `json:"owner"`
	Path       string `json:"path"`
}

// FileEntry mirrors one VFS directory entry.
type FileEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
	Mtime int64  `json:"mtime"`
}

// BrowseResponse is either a directory listing or a single file whose
// content the node has already redacted.
type BrowseResponse struct {
	IsDir   bool        `json:"is_dir"`
	Entries []FileEntry `json:"entries,omitempty"`
	Path    string      `json:"path,omitempty"`
	Content string      `json:"content,omitempty"`
	Audit   AuditBatch  `json:"audit"`
}

type PromptRequest struct {
	InstanceID string `json:"instance_id"`
	Owner      Owner  `json:"owner"`
}

// PromptSecret names a secret and the hosts it may be sent to. Values
// never leave the node.
type PromptSecret struct {
	Name    string   `json:"name"`
	Domains []string `json:"domains,omitempty"`
}

// PromptFragment is one granted capability's rendered prompt text.
type PromptFragment struct {
	Capability types.CapabilityName `json:"capability"`
	Text       string               `json:"text"`
}

// PromptResponse carries the per-capability prompt fragments the node
// rendered from the instance's effective config, plus the facts the API
// tier needs to assemble the final prompt. Capability factories are not
// serializable, so the node renders and the API tier composes.
type PromptResponse struct {
	Fragments      []PromptFragment  `json:"fragments,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	Secrets        []PromptSecret    `json:"secrets,omitempty"`
	ExecTimeoutMs  int64             `json:"exec_timeout_ms"`
	MaxSteps       uint64            `json:"max_steps"`
	MaxOutputBytes int64             `json:"max_output_bytes"`
	TTLSeconds     int64             `json:"ttl_seconds"`
	// NetHosts is the raw allow_hosts list, used for the worked example.
	NetHosts []string `json:"net_hosts,omitempty"`
}
