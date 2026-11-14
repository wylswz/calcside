package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// NodeControl is the serving control plane: the API's scheduler owns
// it and workers consume it over the node-control transport. Reads are
// inherently idempotent and never run user code — `PrepareReplica`
// returns a full self-contained PrepareRequest (including in-memory
// secrets) for exactly one fenced replica assignment.
//
// Clock contract: ServerNow is stamped at END of response. The worker
// converts lease expiry to a LOCAL monotonic deadline as
// localReceiveTime + (ExpiresAt - ServerNow) - fullRoundTripElapsed —
// subtracting the whole RTT is conservative and assumes no
// synchronized clocks. A remaining lifetime <= 0 means do not prepare.
//
// Fencing: every report/renew/ack matches the exact stored tuple —
// replica id, logical instance id, release, node id, boot id, node
// incarnation, generation and epoch. There are no transparent retries
// and no invoke dedup.
type NodeControl interface {
	RegisterNode(ctx context.Context, req *NodeRegisterRequest) (*NodeRegisterResponse, error)
	SyncNode(ctx context.Context, req *NodeSyncRequest) (*NodeSyncResponse, error)
	PrepareReplica(ctx context.Context, req *NodePrepareRequest) (*NodePrepareResponse, error)
}

// Replica lifecycle states on the control wire; they mirror the
// store's durable set without importing it.
const (
	NodeReplicaPreparing = "preparing"
	NodeReplicaReady     = "ready"
	NodeReplicaFailed    = "failed"
	NodeReplicaStopped   = "stopped"
	// NodeReplicaAuditOnly delivers report audit without a terminal
	// state transition — used when cleanup can't be verified so the
	// DB lease must expire on its own.
	NodeReplicaAuditOnly = "audit_only"
)

// NodeIdentity is the node's stable id plus the boot incarnation the
// API issued at registration.
type NodeIdentity struct {
	NodeID      string `json:"node_id"`
	BootID      string `json:"boot_id"`
	Incarnation int64  `json:"incarnation"`
}

// NodeRegisterRequest announces a fresh worker boot.
type NodeRegisterRequest struct {
	NodeID   string `json:"node_id"`
	BootID   string `json:"boot_id"`
	Capacity int    `json:"capacity"`
}

// NodeRegisterResponse returns the assigned incarnation and server
// clock for the lease-conversion formula above.
type NodeRegisterResponse struct {
	Node      NodeIdentity `json:"node"`
	ServerNow time.Time    `json:"server_now"`
}

// ReplicaAssignment is one desired replica on this node. InstanceID is
// the logical persistent instance; ReplicaID is the ephemeral row.
type ReplicaAssignment struct {
	ReplicaID  string    `json:"replica_id"`
	InstanceID string    `json:"instance_id"`
	ReleaseID  string    `json:"release_id"`
	Generation int64     `json:"generation"`
	Epoch      int64     `json:"epoch"`
	ExpiresAt  time.Time `json:"expires_at"`
	Owner      Owner     `json:"owner"`
}

// ReplicaReport is a worker's truthful terminal-or-ready
// acknowledgement for one assignment; audit always rides along,
// including on failures.
type ReplicaReport struct {
	ReportID   string            `json:"report_id"`
	Assignment ReplicaAssignment `json:"assignment"`
	State      string            `json:"state"`
	ErrorCode  string            `json:"error_code,omitempty"`
	Audit      AuditBatch        `json:"audit"`
}

// NodeSyncRequest is the heartbeat + inventory exchange. Inventory
// contains ONLY locally ready contexts — never wishful or preparing
// entries.
type NodeSyncRequest struct {
	Node      NodeIdentity        `json:"node"`
	Capacity  int                 `json:"capacity"`
	Inventory []ReplicaAssignment `json:"inventory"`
	Reports   []ReplicaReport     `json:"reports"`
}

// NodeSyncResponse carries the full authoritative assignment set —
// metadata only, never program bytes or secrets.
type NodeSyncResponse struct {
	Assignments         []ReplicaAssignment `json:"assignments"`
	AcknowledgedReports []string            `json:"acknowledged_reports"`
	ServerNow           time.Time           `json:"server_now"`
}

// NodePrepareRequest asks for the full self-contained prepare payload
// of one assigned replica (secrets resolved in memory, never stored).
type NodePrepareRequest struct {
	Node       NodeIdentity      `json:"node"`
	Assignment ReplicaAssignment `json:"assignment"`
}

// NodePrepareResponse embeds the complete request the worker forwards
// verbatim to Manager.Prepare after lease conversion.
type NodePrepareResponse struct {
	Request   PrepareRequest `json:"request"`
	ServerNow time.Time      `json:"server_now"`
}

func (n NodeIdentity) String() string {
	return fmt.Sprintf("node=%s boot=%s inc=%d", n.NodeID, n.BootID, n.Incarnation)
}

// --- log-safe metadata ---

func (r NodeSyncRequest) String() string {
	return fmt.Sprintf("NodeSync{node=%s capacity=%d inventory=%d reports=%d}",
		r.Node, r.Capacity, len(r.Inventory), len(r.Reports))
}

func (r NodeSyncRequest) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("node_id", r.Node.NodeID),
		slog.String("boot_id", r.Node.BootID),
		slog.Int64("incarnation", r.Node.Incarnation),
		slog.Int("capacity", r.Capacity),
		slog.Int("inventory", len(r.Inventory)),
		slog.Int("reports", len(r.Reports)))
}

func (r ReplicaReport) String() string {
	return fmt.Sprintf("ReplicaReport{id=%s replica=%s instance=%s state=%s events=%d}",
		r.ReportID, r.Assignment.ReplicaID, r.Assignment.InstanceID, r.State, len(r.Audit.Events))
}

func (r ReplicaReport) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("report_id", r.ReportID),
		slog.String("replica_id", r.Assignment.ReplicaID),
		slog.String("instance_id", r.Assignment.InstanceID),
		slog.String("release_id", r.Assignment.ReleaseID),
		slog.Int64("generation", r.Assignment.Generation),
		slog.Int64("epoch", r.Assignment.Epoch),
		slog.String("state", r.State),
		slog.Int("audit_events", len(r.Audit.Events)))
}

func (r NodePrepareResponse) String() string {
	return fmt.Sprintf("NodePrepare{request=%s}", r.Request.String())
}

func (r NodePrepareResponse) LogValue() slog.Value {
	return r.Request.LogValue()
}

func (r NodeRegisterRequest) String() string {
	return fmt.Sprintf("NodeRegister{node=%s boot=%s capacity=%d}", r.NodeID, r.BootID, r.Capacity)
}

func (r NodeRegisterRequest) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("node_id", r.NodeID),
		slog.String("boot_id", r.BootID),
		slog.Int("capacity", r.Capacity))
}
