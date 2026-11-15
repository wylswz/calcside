package auth

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"calcside/internal/store"
)

// touchFlushEvery bounds last_used_at staleness: buffered touches
// flush at most this often per API replica, so request rate no longer
// drives the write rate.
const touchFlushEvery = time.Minute

// keyToucher batches api_keys.last_used_at updates. Resolve used to
// issue one UPDATE per bearer-authenticated request, which serialized
// every request sharing a key on the same row under load. Buffering
// keeps only the newest timestamp per key and flushes periodically —
// the column is informational, so flush-interval staleness is the
// intended trade. Shape mirrors audit.Recorder.
type keyToucher struct {
	st    store.Store
	every time.Duration

	mu     sync.Mutex
	pend   map[string]time.Time
	closed bool
	done   chan struct{}
	wg     sync.WaitGroup
}

func newKeyToucher(st store.Store, every time.Duration) *keyToucher {
	t := &keyToucher{st: st, every: every, pend: map[string]time.Time{}, done: make(chan struct{})}
	t.wg.Add(1)
	go t.loop()
	return t
}

// Touch records a use of the key; only the newest timestamp per key
// is ever written. Touches after Close drop silently.
func (t *keyToucher) Touch(id string, at time.Time) {
	t.mu.Lock()
	if !t.closed {
		if cur, ok := t.pend[id]; !ok || at.After(cur) {
			t.pend[id] = at.UTC()
		}
	}
	t.mu.Unlock()
}

func (t *keyToucher) loop() {
	defer t.wg.Done()
	ticker := time.NewTicker(t.every)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			t.flush()
		case <-t.done:
			t.flush()
			return
		}
	}
}

// flush writes every buffered timestamp. Failures are logged and
// dropped — last_used_at is best-effort, matching the fire-and-forget
// semantics it replaces.
func (t *keyToucher) flush() {
	t.mu.Lock()
	pend := t.pend
	t.pend = map[string]time.Time{}
	t.mu.Unlock()
	if len(pend) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for id, at := range pend {
		if err := t.st.TouchAPIKey(ctx, id, at); err != nil {
			slog.Warn("api key touch flush failed", "key_id", id, "err", err)
		}
	}
}

// Close flushes pending touches and stops the writer. Safe to call
// more than once.
func (t *keyToucher) Close() {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.closed = true
	close(t.done)
	t.mu.Unlock()
	t.wg.Wait()
}
