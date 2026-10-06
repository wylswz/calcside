package dto

import (
	"time"

	"calcside/internal/store"
	"calcside/internal/types"
)

// AuditEvent mirrors the wire shape of a store.AuditEvent.
type AuditEvent struct {
	ID         string               `json:"id"`
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

func NewAuditEvent(e *store.AuditEvent) AuditEvent {
	return AuditEvent{
		ID: e.ID, Ts: e.Ts, UserID: e.UserID, InstanceID: e.InstanceID,
		ExecID: e.ExecID, Capability: e.Capability, Op: e.Op, Args: e.Args,
		Phase: e.Phase, Decision: e.Decision, Reason: e.Reason,
		Error: e.Error, DurationMs: e.DurationMs,
	}
}

func NewAuditEvents(lst []*store.AuditEvent) []AuditEvent {
	out := make([]AuditEvent, len(lst))
	for i, e := range lst {
		out[i] = NewAuditEvent(e)
	}
	return out
}

type AuditEnvelope struct {
	Events []AuditEvent `json:"events"`
}
