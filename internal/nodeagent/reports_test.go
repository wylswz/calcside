package nodeagent

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"calcside/internal/runtime"
)

// Report reservations: a lifecycle reserves two slots; enqueues
// consume them and cannot fail silently at the cap.
func TestReportReservationSlots(t *testing.T) {
	a, err := New(Options{
		Control: &fakeCtl{}, Runtime: &fakeRT{}, NodeID: "n1", BootID: "b1", Capacity: 2})
	if err != nil {
		t.Fatal(err)
	}
	a.ident = runtime.NodeIdentity{NodeID: "n1", BootID: "b1", Incarnation: 1}
	m := &managed{
		assignment: assign("rep_1", "pin_1", 1, time.Now().Add(time.Minute)),
		reserved:   2,
	}
	a.slots = 2

	a.enqueueReport(m, runtime.NodeReplicaReady, "", runtime.AuditBatch{})
	a.enqueueReport(m, runtime.NodeReplicaStopped, "", runtime.AuditBatch{})
	if len(a.reports) != 2 || m.reserved != 0 || a.slots != 2 {
		t.Fatalf("reservation accounting: reports=%d reserved=%d slots=%d",
			len(a.reports), m.reserved, a.slots)
	}
	// Slots at cap: two pending fill them; a third enqueue panics
	// (unreachable by design — reservation is exhausted).
	defer func() {
		if recover() == nil {
			t.Fatal("over-reservation silently accepted")
		}
	}()
	a.enqueueReport(m, runtime.NodeReplicaStopped, "", runtime.AuditBatch{})
}

// Acks free slots only for ids we actually sent; unknown ack ids free
// nothing.
func TestReportAckAccounting(t *testing.T) {
	a, err := New(Options{
		Control: &fakeCtl{}, Runtime: &fakeRT{}, NodeID: "n1", BootID: "b1", Capacity: 2})
	if err != nil {
		t.Fatal(err)
	}
	a.ident = runtime.NodeIdentity{NodeID: "n1", BootID: "b1", Incarnation: 1}
	m := &managed{
		assignment: assign("rep_1", "pin_1", 1, time.Now().Add(time.Minute)),
		reserved:   2,
	}
	a.slots = 2
	a.enqueueReport(m, runtime.NodeReplicaReady, "", runtime.AuditBatch{})
	a.enqueueReport(m, runtime.NodeReplicaStopped, "", runtime.AuditBatch{})

	a.mu.Lock()
	batch := a.reportBatch(nil)
	// An ack for an id we never sent frees nothing.
	a.ackReports(batch, []string{"rpt_forged"})
	if len(a.reports) != 2 || a.slots != 2 {
		t.Fatalf("forged ack freed slots: %d/%d", len(a.reports), a.slots)
	}
	// A real ack frees the slot and drops the report.
	a.ackReports(batch, []string{batch[0].ReportID})
	if len(a.reports) != 1 || a.slots != 1 {
		t.Fatalf("ack: reports=%d slots=%d", len(a.reports), a.slots)
	}
	a.mu.Unlock()
}

// Bounded reports: oversized Args are clipped, events beyond the byte
// and count budgets become Dropped, and the sync body stays under the
// wire limit while report ids stay stable across retries.
func TestReportBatchingBounds(t *testing.T) {
	a, err := New(Options{
		Control: &fakeCtl{}, Runtime: &fakeRT{}, NodeID: "n1", BootID: "b1", Capacity: 2})
	if err != nil {
		t.Fatal(err)
	}
	a.ident = runtime.NodeIdentity{NodeID: "n1", BootID: "b1", Incarnation: 1}

	// 40 reports, each with 512 oversized-arg events.
	for i := 0; i < 40; i++ {
		m := &managed{
			assignment: assign("rep_"+strings.Repeat("x", 2)+string(rune('a'+i%26)),
				"pin_1", int64(i+1), time.Now().Add(time.Minute)),
			reserved: 2,
		}
		evs := make([]runtime.AuditEvent, 0, 512)
		for j := 0; j < 512; j++ {
			evs = append(evs, runtime.AuditEvent{
				Ts: time.Now(), Args: strings.Repeat("\x00", 4096)})
		}
		a.enqueueReport(m, runtime.NodeReplicaFailed, "runtime",
			runtime.AuditBatch{Events: evs})
		m.reserved = 0
		a.slots += 1 // simulate the slot the queue already holds
	}
	a.mu.Lock()
	if len(a.reports) != 40 {
		t.Fatalf("queued %d", len(a.reports))
	}
	for _, r := range a.reports {
		if r.Audit.Dropped == 0 {
			t.Fatal("overflow events not counted as dropped")
		}
		enc, _ := json.Marshal(r)
		if len(enc) > maxReportBytes {
			t.Fatalf("report %s over bound: %d", r.ReportID, len(enc))
		}
	}
	// The batch must fit the wire bound and send oldest first.
	batch := a.reportBatch(nil)
	if len(batch) == 0 || len(batch) > maxReportSend {
		t.Fatalf("batch %d", len(batch))
	}
	body, _ := json.Marshal(runtime.NodeSyncRequest{
		Node: a.ident, Capacity: a.o.Capacity, Reports: batch})
	if len(body) > maxSyncBody {
		t.Fatalf("sync body %d", len(body))
	}
	a.ackReports(batch, []string{batch[0].ReportID, batch[1].ReportID})
	a.mu.Unlock()
	// Next batch leads with the first unacked report — stable ids.
	a.mu.Lock()
	batch2 := a.reportBatch(nil)
	a.mu.Unlock()
	if batch2[0].ReportID != batch[2].ReportID {
		t.Fatal("report order not stable across acks")
	}
}
