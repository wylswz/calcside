package nodeagent

import (
	"context"
	"encoding/json"
	"unicode/utf8"

	"calcside/internal/runtime"
	"calcside/internal/types"
)

const maxReportBytes = 256 << 10

func clipped(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for !utf8.ValidString(s[:n]) {
		n--
	}
	return s[:n]
}

func boundedReport(r runtime.ReplicaReport) runtime.ReplicaReport {
	events := r.Audit.Events
	r.Assignment.Owner.Email = ""
	r.Audit.Events = nil
	if r.Audit.Dropped < 0 {
		r.Audit.Dropped = 0
	}
	base, _ := json.Marshal(r)
	size := len(base) + 64
	for i, e := range events {
		e.UserID = clipped(e.UserID, 128)
		e.InstanceID = clipped(e.InstanceID, 128)
		e.ExecID = clipped(e.ExecID, 128)
		e.Op = types.Op(clipped(string(e.Op), 128))
		e.Args = clipped(e.Args, 4<<10)
		e.Reason = clipped(e.Reason, 1<<10)
		e.Error = clipped(e.Error, 1<<10)
		encoded, err := json.Marshal(e)
		if err != nil || len(r.Audit.Events) >= maxReportEvents ||
			size+len(encoded)+1 > maxReportBytes {
			r.Audit.Dropped += len(events) - i
			break
		}
		size += len(encoded) + 1
		r.Audit.Events = append(r.Audit.Events, e)
	}
	return r
}

// Each lifecycle reserves two slots before it starts. Moving a reservation
// into this immutable queue cannot fail or discard failure audit.
func (a *Agent) enqueueReport(m *managed, state, code string, audit runtime.AuditBatch) {
	r := boundedReport(runtime.ReplicaReport{
		ReportID: newReportID(), Assignment: m.assignment,
		State: state, ErrorCode: code, Audit: audit,
	})
	a.mu.Lock()
	defer a.mu.Unlock()
	if m.reserved <= 0 {
		panic("nodeagent: report reservation exhausted")
	}
	m.reserved--
	a.reports = append(a.reports, &r)
	// Nudge the poll loop so a ready/stopped report ships immediately
	// instead of waiting a full PollInterval.
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

// reportBatch and ackReports require a.mu.
func (a *Agent) reportBatch(inventory []runtime.ReplicaAssignment) []runtime.ReplicaReport {
	base, err := json.Marshal(runtime.NodeSyncRequest{
		Node: a.ident, Capacity: a.o.Capacity, Inventory: inventory,
	})
	if err != nil {
		return nil
	}
	size, events := len(base)+16, 0
	var batch []runtime.ReplicaReport
	for _, r := range a.reports {
		encoded, err := json.Marshal(r)
		if err != nil || len(batch) >= maxReportSend ||
			events+len(r.Audit.Events) > maxReportEvents ||
			size+len(encoded)+1 > maxSyncBody {
			break
		}
		size += len(encoded) + 1
		events += len(r.Audit.Events)
		batch = append(batch, *r)
	}
	return batch
}

func (a *Agent) ackReports(batch []runtime.ReplicaReport, acknowledgements []string) {
	sent := make(map[string]bool, len(batch))
	for _, r := range batch {
		sent[r.ReportID] = true
	}
	acked := make(map[string]bool, len(acknowledgements))
	for _, id := range acknowledgements {
		if sent[id] {
			acked[id] = true
		}
	}
	old := a.reports
	kept := old[:0]
	for _, r := range old {
		if acked[r.ReportID] {
			a.slots--
		} else {
			kept = append(kept, r)
		}
	}
	for i := len(kept); i < len(old); i++ {
		old[i] = nil
	}
	a.reports = kept
}

// Shutdown may deliver pending audit, but must never apply the returned
// desired set or start a replacement after local teardown has drained.
func (a *Agent) flushReports(ctx context.Context) {
	for ctx.Err() == nil {
		a.mu.Lock()
		batch := a.reportBatch(nil)
		before := len(a.reports)
		a.mu.Unlock()
		if len(batch) == 0 {
			return
		}
		resp, err := a.o.Control.SyncNode(ctx, &runtime.NodeSyncRequest{
			Node: a.ident, Capacity: a.o.Capacity, Reports: batch,
		})
		if err != nil || resp == nil {
			return
		}
		a.mu.Lock()
		a.ackReports(batch, resp.AcknowledgedReports)
		progress := len(a.reports) < before
		a.mu.Unlock()
		if !progress {
			return
		}
	}
}
