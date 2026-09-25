// Package audit records every gated capability call into the store via an
// async batched writer. It never records file contents or response bodies.
package audit

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"calcside/internal/capability"
	"calcside/internal/store"
)

const (
	argsMaxBytes = 4 << 10
	batchSize    = 100
	flushEvery   = time.Second
	queueSize    = 4096
)

// Recorder is a capability.Observer that enqueues audit events.
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

// Observe converts a gate record into an AuditEvent and enqueues it.
func (r *Recorder) Observe(rec capability.Record) {
	args := "{}"
	if rec.Call.Args != nil {
		if b, err := json.Marshal(rec.Call.Args); err == nil {
			if len(b) > argsMaxBytes {
				b = b[:argsMaxBytes]
			}
			args = string(b)
		}
	}
	errStr := ""
	if rec.Err != nil {
		errStr = rec.Err.Error()
	}
	ev := store.AuditEvent{
		ID:         store.NewID(store.PrefixAudit),
		Ts:         time.Now().UTC(),
		UserID:     rec.Call.UserID,
		InstanceID: rec.Call.InstanceID,
		ExecID:     rec.Call.ExecID,
		Capability: rec.Call.Capability,
		Op:         rec.Call.Op,
		Args:       args,
		Phase:      rec.Phase,
		Decision:   rec.Decision,
		Reason:     rec.Reason,
		Error:      errStr,
		DurationMs: rec.Duration.Milliseconds(),
	}
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
