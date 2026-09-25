package instance

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"calcside/internal/capability"
	capfs "calcside/internal/capability/fs"
	capio "calcside/internal/capability/io"
	"calcside/internal/engine"
	"calcside/internal/store"
	_ "calcside/internal/store/sqlite"
)

func testMgr(t *testing.T, now *time.Time, limits ServerLimits) (*Manager, store.Store, *store.User) {
	t.Helper()
	st, err := store.Open(context.Background(), "sqlite", filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	reg := capability.NewRegistry()
	reg.Register(capfs.Factory())
	reg.Register(capio.Factory())
	m := New(st, engine.New(8), reg, nil, "", time.Second, limits, nil,
		func() time.Time { return *now }, time.Hour)
	u, err := st.UpsertUserByEmail(context.Background(), "u@x.com", "", "")
	if err != nil {
		t.Fatal(err)
	}
	return m, st, u
}

func defaultLimits() ServerLimits {
	return ServerLimits{
		MaxInstancesPerUser: 10,
		DefaultTTL:          15 * time.Minute,
		MaxTTL:              24 * time.Hour,
		MaxExecTimeout:      5 * time.Minute,
		MaxFSQuotaBytes:     256 << 20,
	}
}

func TestTTLExpiryViaFakeClock(t *testing.T) {
	now := time.Now()
	m, st, u := testMgr(t, &now, defaultLimits())
	ctx := context.Background()
	meta, err := m.Create(ctx, u, []byte(`{"ttl_seconds":60,"capabilities":{"fs":{}}}`))
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	m.Reap(ctx)
	got, err := st.GetInstance(ctx, meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.StatusExpired || got.EndedAt == nil {
		t.Fatalf("expected expired, got %+v", got)
	}
	// exec on expired instance fails
	if _, err := m.Exec(ctx, meta.ID, "print(1)", 0, nil); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound after expiry, got %v", err)
	}
}

func TestKeepaliveExtends(t *testing.T) {
	now := time.Now()
	m, _, u := testMgr(t, &now, defaultLimits())
	ctx := context.Background()
	meta, err := m.Create(ctx, u, []byte(`{"ttl_seconds":60}`))
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(50 * time.Second)
	if _, err := m.Keepalive(ctx, meta.ID); err != nil {
		t.Fatal(err)
	}
	now = now.Add(50 * time.Second) // 100s > 60s since creation
	m.Reap(ctx)
	if _, err := m.Exec(ctx, meta.ID, "print('alive')", 0, nil); err != nil {
		t.Fatalf("instance should still be alive: %v", err)
	}
}

func TestPerUserLimit(t *testing.T) {
	now := time.Now()
	limits := defaultLimits()
	limits.MaxInstancesPerUser = 2
	m, _, u := testMgr(t, &now, limits)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := m.Create(ctx, u, []byte(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.Create(ctx, u, []byte(`{}`)); err != ErrTooMany {
		t.Fatalf("expected ErrTooMany, got %v", err)
	}
}

func TestConcurrentExecsSerialized(t *testing.T) {
	now := time.Now()
	m, _, u := testMgr(t, &now, defaultLimits())
	ctx := context.Background()
	meta, err := m.Create(ctx, u, []byte(`{"capabilities":{"fs":{}}}`))
	if err != nil {
		t.Fatal(err)
	}
	// Hammer the same instance from many goroutines; -race catches
	// unsynchronized globals/buffer access.
	if _, err := m.Exec(ctx, meta.ID, "counter = 0", 0, nil); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := m.Exec(ctx, meta.ID,
				"counter = counter + 1\nfs.write('n.txt', str(counter))", 0, nil)
			if err != nil || res.Error != nil {
				t.Errorf("exec %d: %v %+v", i, err, res)
			}
		}(i)
	}
	wg.Wait()
}

func TestStartupMarksRunningAsLost(t *testing.T) {
	now := time.Now()
	m, st, u := testMgr(t, &now, defaultLimits())
	ctx := context.Background()
	meta, err := m.Create(ctx, u, []byte(`{"ttl_seconds":3600}`))
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a fresh process: new manager on same store.
	m2, _, _ := testMgr2(t, &now, defaultLimits(), st)
	n, err := m2.Recover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 lost, got %d", n)
	}
	got, _ := st.GetInstance(ctx, meta.ID)
	if got.Status != store.StatusLost {
		t.Fatalf("expected lost, got %s", got.Status)
	}
}

func testMgr2(t *testing.T, now *time.Time, limits ServerLimits, st store.Store) (*Manager, store.Store, *store.User) {
	t.Helper()
	reg := capability.NewRegistry()
	reg.Register(capfs.Factory())
	reg.Register(capio.Factory())
	m := New(st, engine.New(8), reg, nil, "", time.Second, limits, nil,
		func() time.Time { return *now }, time.Hour)
	u, _ := st.UpsertUserByEmail(context.Background(), "u@x.com", "", "")
	return m, st, u
}
