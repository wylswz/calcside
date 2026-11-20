package auth

import (
	"context"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"calcside/internal/store"
)

type touchCall struct {
	id string
	at time.Time
}

// touchStore records TouchAPIKey calls; the embedded interface covers
// the rest of the surface.
type touchStore struct {
	store.Store
	mu    sync.Mutex
	calls []touchCall
	key   *store.APIKey
	user  *store.User
}

func (s *touchStore) TouchAPIKey(_ context.Context, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, touchCall{id, at})
	return nil
}

func (s *touchStore) GetAPIKeyByHash(context.Context, string) (*store.APIKey, error) {
	return s.key, nil
}

func (s *touchStore) GetUser(context.Context, string) (*store.User, error) {
	return s.user, nil
}

func (s *touchStore) written() []touchCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]touchCall(nil), s.calls...)
}

func TestKeyToucherCoalesces(t *testing.T) {
	st := &touchStore{}
	tk := newKeyToucher(st, time.Hour) // long interval: only explicit flush writes
	defer tk.Close()
	base := time.Now().UTC()
	for i := 0; i < 5; i++ {
		tk.Touch("k1", base.Add(time.Duration(i)*time.Second))
	}
	// Out-of-order older observations never win the buffer.
	tk.Touch("k1", base.Add(-time.Minute))
	tk.Touch("k2", base)
	if got := st.written(); len(got) != 0 {
		t.Fatalf("wrote before flush: %+v", got)
	}
	tk.flush()
	got := st.written()
	if len(got) != 2 {
		t.Fatalf("writes=%d, want 2 (one per key): %+v", len(got), got)
	}
	byID := map[string]time.Time{}
	for _, c := range got {
		byID[c.id] = c.at
	}
	if !byID["k1"].Equal(base.Add(4*time.Second)) || !byID["k2"].Equal(base) {
		t.Fatalf("newest timestamps not kept: %+v", got)
	}
}

func TestKeyToucherCloseFlushes(t *testing.T) {
	st := &touchStore{}
	tk := newKeyToucher(st, time.Hour)
	tk.Touch("k1", time.Now().UTC())
	tk.Close()
	if got := st.written(); len(got) != 1 || got[0].id != "k1" {
		t.Fatalf("close did not flush: %+v", got)
	}
	// Touches after close drop silently; Close is idempotent.
	tk.Touch("k1", time.Now().UTC())
	tk.Close()
	if got := st.written(); len(got) != 1 {
		t.Fatalf("touch after close written: %+v", got)
	}
}

// TestResolveBuffersAPIKeyTouch is the issue-43 regression: Resolve
// must not issue a store write on the request path — the touch lands
// only when the buffered flusher runs (or the service closes).
func TestResolveBuffersAPIKeyTouch(t *testing.T) {
	st := &touchStore{
		key:  &store.APIKey{ID: "key_1", UserID: "usr_1", Hash: "h"},
		user: &store.User{ID: "usr_1"},
	}
	svc := NewService(st, false)
	r := httptest.NewRequest("GET", "/api/v1/me", nil)
	r.Header.Set("Authorization", "Bearer cs_token")
	p, err := svc.Resolve(r.Context(), r)
	if err != nil || p == nil || p.KeyID != "key_1" {
		t.Fatalf("resolve: p=%+v err=%v", p, err)
	}
	if got := st.written(); len(got) != 0 {
		t.Fatalf("per-request write on resolve: %+v", got)
	}
	svc.Close()
	got := st.written()
	if len(got) != 1 || got[0].id != "key_1" {
		t.Fatalf("close flush: %+v", got)
	}
}
