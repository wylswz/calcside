package instance

import (
	"encoding/json"
	"sync"
	"time"

	"calcside/internal/capability"
	"calcside/internal/runtime"
)

const (
	// argsMaxBytes truncates the JSON-encoded args recorded per call.
	argsMaxBytes = 4 << 10
	// maxAuditEvents caps one request's audit batch. A script can make
	// unboundedly many capability calls, and the batch has to fit in a
	// response, so overflow is counted rather than silently dropped.
	maxAuditEvents = 4096
)

// auditSink collects the gated calls made during one request so the
// response can carry them back to the API tier, which owns persistence.
//
// There is one sink per instance rather than one per request: a gate is
// built once at create time and cannot be re-pointed. Requests against
// an instance are serialized by the engine's per-session lock, so a
// sink only ever accumulates for the request in flight, and drain
// happens on that same goroutine once the request completes.
type auditSink struct {
	mu      sync.Mutex
	events  []runtime.AuditEvent
	dropped int
}

// Observe implements capability.Observer.
func (s *auditSink) Observe(rec capability.Record) {
	ev := runtime.AuditEvent{
		Ts:         time.Now().UTC(),
		UserID:     rec.Call.UserID,
		InstanceID: rec.Call.InstanceID,
		ExecID:     rec.Call.ExecID,
		Capability: rec.Call.Capability,
		Op:         rec.Call.Op,
		Args:       marshalArgs(rec.Call.Args),
		Phase:      rec.Phase,
		Decision:   rec.Decision,
		Reason:     rec.Reason,
		DurationMs: rec.Duration.Milliseconds(),
	}
	if rec.Err != nil {
		ev.Error = rec.Err.Error()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.events) >= maxAuditEvents {
		s.dropped++
		return
	}
	s.events = append(s.events, ev)
}

// drain returns and clears the batch accumulated so far.
func (s *auditSink) drain() runtime.AuditBatch {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := runtime.AuditBatch{Events: s.events, Dropped: s.dropped}
	s.events, s.dropped = nil, 0
	return b
}

// marshalArgs encodes call args as truncated JSON. Args are already
// normalized and secret-free by the time a record reaches here.
func marshalArgs(args map[string]any) string {
	if args == nil {
		return "{}"
	}
	b, err := json.Marshal(args)
	if err != nil {
		return "{}"
	}
	if len(b) > argsMaxBytes {
		b = b[:argsMaxBytes]
	}
	return string(b)
}
