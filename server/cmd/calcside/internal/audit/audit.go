// Package audit persists the gated capability calls an execution node
// reports. Nodes do not write to the database: they return audit
// batches on the response that produced them, and the API tier hands
// those batches here for async batched insertion.
//
// It never records file contents or response bodies — that is enforced
// upstream, where a Call's args are built.
package audit

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"calcside/internal/runtime"
	"calcside/internal/store"
)

const (
	batchSize  = 100
	flushEvery = time.Second
	queueSize  = 4096
)

// Recorder buffers audit events and writes them to the store in
// batches.
type Recorder struct {
	st   store.Store
	ch   chan store.AuditEvent
	done chan struct{}
	wg   sync.WaitGroup
}

func NewRecorder(st store.Store) *Recorder {
	r := &Recorder{st: st, ch: make(chan store.AuditEvent, queueSize), done: make(chan struct{})}
	r.wg.Add(1)
	go r.writer()
	return r
}

// Record enqueues a batch reported by an execution node. Events are
// assigned IDs here: identity belongs to the tier that persists.
//
// Callers must call this even when the request that produced the batch
// failed — a denied or erroring capability call is exactly the kind of
// thing the audit log exists for.
func (r *Recorder) Record(b runtime.AuditBatch) {
	logDropped(b)
	ws := map[string]string{}
	for _, ev := range b.Events {
		w := ws[ev.InstanceID]
		if w == "" && ev.InstanceID != "" {
			if _, ok := ws[ev.InstanceID]; !ok {
				if in, err := r.st.GetInstance(context.Background(), ev.InstanceID); err == nil {
					ws[ev.InstanceID] = in.WorkspaceID
				}
				w = ws[ev.InstanceID]
			}
		}
		if w == "" {
			// An unresolvable event must never poison the whole insert
			// batch — replicas without an instance row belong to
			// RecordWorkspace callers.
			slog.Warn("audit event without workspace dropped", "instance_id", ev.InstanceID, "op", string(ev.Capability)+"."+string(ev.Op))
			continue
		}
		r.enqueue(toStoreEvent(ev, w))
	}
}

// RecordWorkspace enqueues a batch for a caller-known workspace. Use it
// for replicas whose ids are not instance rows — application replicas
// (apv_…) have no GetInstance answer, so the workspace arrives with the
// call instead of a lookup.
func (r *Recorder) RecordWorkspace(workspaceID string, b runtime.AuditBatch) {
	logDropped(b)
	for _, ev := range b.Events {
		r.enqueue(toStoreEvent(ev, workspaceID))
	}
}

func logDropped(b runtime.AuditBatch) {
	if b.Dropped > 0 {
		slog.Warn("audit batch overflowed on the execution node", "dropped", b.Dropped)
	}
}

func toStoreEvent(ev runtime.AuditEvent, workspaceID string) store.AuditEvent {
	return store.AuditEvent{
		ID:          store.NewID(store.PrefixAudit),
		WorkspaceID: workspaceID,
		Ts:          ev.Ts,
		UserID:      ev.UserID,
		InstanceID:  ev.InstanceID,
		ExecID:      ev.ExecID,
		Capability:  ev.Capability,
		Op:          ev.Op,
		Args:        ev.Args,
		Phase:       ev.Phase,
		Decision:    ev.Decision,
		Reason:      ev.Reason,
		Error:       ev.Error,
		DurationMs:  ev.DurationMs,
	}
}

func (r *Recorder) enqueue(ev store.AuditEvent) {
	select {
	case r.ch <- ev:
	default:
		slog.Warn("audit queue full, dropping event", "op", string(ev.Capability)+"."+string(ev.Op))
	}
}

func (r *Recorder) writer() {
	defer r.wg.Done()
	ticker := time.NewTicker(flushEvery)
	defer ticker.Stop()
	var batch []store.AuditEvent
	flush := func() {
		if len(batch) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := r.st.InsertAuditEvents(ctx, batch); err != nil {
			slog.Error("audit insert failed", "err", err, "count", len(batch))
		}
		cancel()
		batch = batch[:0]
	}
	for {
		select {
		case ev := <-r.ch:
			batch = append(batch, ev)
			if len(batch) >= batchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-r.done:
			// drain remaining events
			for {
				select {
				case ev := <-r.ch:
					batch = append(batch, ev)
				default:
					flush()
					return
				}
			}
		}
	}
}

// Close flushes pending events and stops the writer.
func (r *Recorder) Close() {
	close(r.done)
	r.wg.Wait()
}
