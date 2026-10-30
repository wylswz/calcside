package storetest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"calcside/internal/store"
)

var rNow = time.Now().UTC().Truncate(time.Second)

// mkRun builds an in-flight run with one running step and nSlots
// reserved interrupts; caller assigns distinct CreatedAt.
func mkRun(id, ws string, createdAt time.Time, nSlots int) (*store.Run, *store.RunStep, []*store.Interrupt) {
	run := &store.Run{
		ID: id, WorkspaceID: ws, ApplicationID: "app_1", ReleaseID: "rel_1",
		UserID: "usr_1", Trigger: store.RunTriggerInvoke, Status: store.RunRunning,
		Steps: 1, CreatedAt: createdAt, UpdatedAt: createdAt,
	}
	step := &store.RunStep{
		RunID: id, Seq: 0, Attempt: 1, Entry: "main",
		Status: store.StepRunning, StartedAt: createdAt,
	}
	var slots []*store.Interrupt
	for i := 0; i < nSlots; i++ {
		slots = append(slots, &store.Interrupt{
			ID: fmt.Sprintf("int_%s_%d", id[len(id)-4:], i), WorkspaceID: ws,
			ApplicationID: "app_1", RunID: id, StepSeq: 0,
			Status: store.InterruptSlotReserved, CreatedAt: createdAt, UpdatedAt: createdAt,
		})
	}
	return run, step, slots
}

func start(t *testing.T, s store.Store, run *store.Run, step *store.RunStep, slots []*store.Interrupt) {
	t.Helper()
	if err := s.StartRun(context.Background(), run, step, slots); err != nil {
		t.Fatal(err)
	}
}

// boundKey registers a callback-style key bound to an interrupt id and
// reports whether it is currently revoked.
func boundKey(t *testing.T, s store.Store, id, boundID string) {
	t.Helper()
	k := &store.APIKey{
		ID: id, UserID: "usr_1", Name: id, Prefix: "p_" + id[len(id)-4:],
		Hash: "h_" + id + boundID[len(boundID)-4:], WorkspaceID: "ws_1", BoundID: boundID,
	}
	if err := s.CreateAPIKey(context.Background(), k); err != nil {
		t.Fatal(err)
	}
}

func keyRevoked(t *testing.T, s store.Store, id string) bool {
	t.Helper()
	k, err := s.GetAPIKey(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return k.RevokedAt != nil
}

func waitOutcome(run *store.Run, seq int, w *store.WaitCommit) store.StepOutcome {
	return store.StepOutcome{
		RunID: run.ID, Seq: seq, Attempt: 1, Status: store.StepWaiting,
		DurationMs: 5, Wait: w, Now: rNow,
	}
}

func RunRuns(t *testing.T, s store.Store) {
	t.Helper()
	ctx := context.Background()

	t.Run("start_get_list", func(t *testing.T) {
		r1, st1, sl1 := mkRun("run_l1", "ws_1", rNow, 1)
		r2, st2, sl2 := mkRun("run_l2", "ws_1", rNow.Add(time.Second), 0)
		r3, st3, sl3 := mkRun("run_l3", "ws_2", rNow.Add(2*time.Second), 0)
		start(t, s, r1, st1, sl1)
		start(t, s, r2, st2, sl2)
		start(t, s, r3, st3, sl3)

		got, err := s.GetRun(ctx, "run_l1")
		if err != nil || got.Status != store.RunRunning || got.Steps != 1 || got.Trigger != store.RunTriggerInvoke {
			t.Fatalf("get: %v %+v", err, got)
		}
		if _, err := s.GetRun(ctx, "run_missing"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("missing: %v", err)
		}
		list, err := s.ListRuns(ctx, store.RunFilter{WorkspaceID: "ws_1"})
		if err != nil || len(list) != 2 || list[0].ID != "run_l2" || list[1].ID != "run_l1" {
			t.Fatalf("list: %v %+v", err, list)
		}
		page, err := s.ListRuns(ctx, store.RunFilter{WorkspaceID: "ws_1", Limit: 1})
		if err != nil || len(page) != 1 || page[0].ID != "run_l2" {
			t.Fatalf("page1: %v %+v", err, page)
		}
		page2, err := s.ListRuns(ctx, store.RunFilter{WorkspaceID: "ws_1", Limit: 1, Before: "run_l2"})
		if err != nil || len(page2) != 1 || page2[0].ID != "run_l1" {
			t.Fatalf("page2: %v %+v", err, page2)
		}
		steps, err := s.ListRunSteps(ctx, "run_l1")
		if err != nil || len(steps) != 1 || steps[0].Seq != 0 || steps[0].Status != store.StepRunning {
			t.Fatalf("steps: %v %+v", err, steps)
		}
		intrs, err := s.ListInterrupts(ctx, store.InterruptFilter{WorkspaceID: "ws_1", RunID: "run_l1"})
		if err != nil || len(intrs) != 1 || intrs[0].Status != store.InterruptSlotReserved {
			t.Fatalf("interrupts: %v %+v", err, intrs)
		}
		if _, err := s.ListRuns(ctx, store.RunFilter{}); err == nil {
			t.Fatal("workspace-less list accepted")
		}
	})

	t.Run("finish_waiting", func(t *testing.T) {
		r, st, slots := mkRun("run_w", "ws_1", rNow, 2)
		start(t, s, r, st, slots)
		boundKey(t, s, "key_w0", slots[0].ID)
		boundKey(t, s, "key_w1", slots[1].ID)

		exp := rNow.Add(time.Hour)
		out := waitOutcome(r, 0, &store.WaitCommit{
			InterruptID: slots[0].ID,
			Handler:     "on_ok", OnExpire: "on_exp", Context: []byte("ctx"),
			ExpiresAt: &exp,
		})
		if err := s.FinishStep(ctx, out, nil); err != nil {
			t.Fatal(err)
		}
		run, _ := s.GetRun(ctx, r.ID)
		if run.Status != store.RunWaiting || run.InterruptID != slots[0].ID {
			t.Fatalf("run: %+v", run)
		}
		i0, _ := s.GetInterrupt(ctx, slots[0].ID)
		if i0.Status != store.InterruptPending ||
			i0.Handler != "on_ok" || i0.OnExpire != "on_exp" ||
			i0.ExpiresAt == nil || string(i0.Context) != "ctx" {
			t.Fatalf("interrupt: %+v", i0)
		}
		i1, _ := s.GetInterrupt(ctx, slots[1].ID)
		if i1.Status != store.InterruptCancelled || !keyRevoked(t, s, "key_w1") {
			t.Fatalf("sibling slot: %+v revoked=%v", i1, keyRevoked(t, s, "key_w1"))
		}
		if keyRevoked(t, s, "key_w0") {
			t.Fatal("committed slot's key revoked")
		}
		// A second finish on the same step conflicts.
		if err := s.FinishStep(ctx, out, nil); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("double finish: %v", err)
		}
	})

	t.Run("finish_early_resolution", func(t *testing.T) {
		r, st, slots := mkRun("run_e", "ws_1", rNow, 1)
		start(t, s, r, st, slots)
		early, err := s.ResolveInterrupt(ctx, slots[0].ID, []byte(`{"ok":true}`), "usr_decider", rNow)
		if err != nil || !early {
			t.Fatalf("early resolve: %v %v", early, err)
		}
		out := waitOutcome(r, 0, &store.WaitCommit{
			InterruptID: slots[0].ID,
			Handler:     "on_ok", Context: []byte("c"),
		})
		if err := s.FinishStep(ctx, out, func(event []byte) bool {
			return string(event) == `{"ok":true}`
		}); err != nil {
			t.Fatal(err)
		}
		i, _ := s.GetInterrupt(ctx, slots[0].ID)
		if i.Status != store.InterruptResolved || i.ResolvedBy != "usr_decider" ||
			i.DispatchState != store.DispatchQueued || i.Attempt != 1 || string(i.Event) != `{"ok":true}` {
			t.Fatalf("early accepted: %+v", i)
		}
	})

	t.Run("finish_early_rejected", func(t *testing.T) {
		r, st, slots := mkRun("run_er", "ws_1", rNow, 1)
		start(t, s, r, st, slots)
		if _, err := s.ResolveInterrupt(ctx, slots[0].ID, []byte(`bad`), "usr_x", rNow); err != nil {
			t.Fatal(err)
		}
		out := waitOutcome(r, 0, &store.WaitCommit{
			InterruptID: slots[0].ID,
			Handler:     "on_ok", Context: []byte("c"),
		})
		if err := s.FinishStep(ctx, out, func([]byte) bool { return false }); err != nil {
			t.Fatal(err)
		}
		i, _ := s.GetInterrupt(ctx, slots[0].ID)
		if i.Status != store.InterruptPending || i.Event != nil || i.ResolvedBy != "" || i.ResolvedAt != nil {
			t.Fatalf("early rejected: %+v", i)
		}
	})

	t.Run("resolve", func(t *testing.T) {
		r, st, slots := mkRun("run_r", "ws_1", rNow, 1)
		start(t, s, r, st, slots)
		out := waitOutcome(r, 0, &store.WaitCommit{
			InterruptID: slots[0].ID, Handler: "on_ok", Context: []byte("c"),
		})
		if err := s.FinishStep(ctx, out, nil); err != nil {
			t.Fatal(err)
		}
		early, err := s.ResolveInterrupt(ctx, slots[0].ID, []byte(`{"a":1}`), "usr_ok", rNow)
		if err != nil || early {
			t.Fatalf("resolve: %v %v", early, err)
		}
		i, _ := s.GetInterrupt(ctx, slots[0].ID)
		if i.Status != store.InterruptResolved || i.DispatchState != store.DispatchQueued || i.Attempt != 1 {
			t.Fatalf("resolved: %+v", i)
		}
		if _, err := s.ResolveInterrupt(ctx, slots[0].ID, []byte(`x`), "usr_2", rNow); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("second resolve: %v", err)
		}
		if _, err := s.ResolveInterrupt(ctx, "int_missing", []byte(`x`), "usr_2", rNow); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("missing: %v", err)
		}
	})

	t.Run("resolve_concurrent", func(t *testing.T) {
		r, st, slots := mkRun("run_rc", "ws_1", rNow, 1)
		start(t, s, r, st, slots)
		out := waitOutcome(r, 0, &store.WaitCommit{
			InterruptID: slots[0].ID, Handler: "on_ok", Context: []byte("c"),
		})
		if err := s.FinishStep(ctx, out, nil); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		var wins, conflicts int64
		var mu sync.Mutex
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				_, err := s.ResolveInterrupt(ctx, slots[0].ID, []byte(fmt.Sprintf("%d", i)), "usr_c", rNow)
				mu.Lock()
				defer mu.Unlock()
				if err == nil {
					wins++
				} else if errors.Is(err, store.ErrConflict) {
					conflicts++
				} else {
					t.Errorf("unexpected error: %v", err)
				}
			}(i)
		}
		wg.Wait()
		if wins != 1 || conflicts != 3 {
			t.Fatalf("wins=%d conflicts=%d", wins, conflicts)
		}
	})

	t.Run("finish_succeeded_failed", func(t *testing.T) {
		for _, status := range []string{store.StepSucceeded, store.StepFailed} {
			r, st, slots := mkRun("run_f"+status[:1]+fmt.Sprint(time.Now().UnixNano()%1000), "ws_1", rNow, 1)
			start(t, s, r, st, slots)
			boundKey(t, s, "key_"+r.ID, slots[0].ID)
			out := waitOutcome(r, 0, &store.WaitCommit{
				InterruptID: slots[0].ID, Handler: "on_ok", Context: []byte("c"),
			})
			if err := s.FinishStep(ctx, out, nil); err != nil {
				t.Fatal(err)
			}
			if _, err := s.ResolveInterrupt(ctx, slots[0].ID, []byte(`{}`), "usr_x", rNow); err != nil {
				t.Fatal(err)
			}
			cl, err := s.ClaimDispatch(ctx, rNow, rNow.Add(time.Minute))
			if err != nil || cl == nil || cl.ID != slots[0].ID {
				t.Fatalf("claim: %v %+v", err, cl)
			}
			step1 := &store.RunStep{RunID: r.ID, Seq: 1, Attempt: 1, Entry: "on_ok",
				InterruptID: slots[0].ID, Status: store.StepRunning, StartedAt: rNow}
			if err := s.StartHandlerStep(ctx, slots[0].ID, step1, nil, rNow); err != nil {
				t.Fatal(err)
			}
			fin := store.StepOutcome{
				RunID: r.ID, Seq: 1, Attempt: 1, Status: status,
				DurationMs: 9, Output: []byte("sealed"),
				DispatchedInterruptID: slots[0].ID, Now: rNow,
			}
			if err := s.FinishStep(ctx, fin, nil); err != nil {
				t.Fatal(err)
			}
			run, _ := s.GetRun(ctx, r.ID)
			want := map[string]string{store.StepSucceeded: store.RunSucceeded, store.StepFailed: store.RunFailed}[status]
			if run.Status != want || run.EndedAt == nil || run.InterruptID != "" {
				t.Fatalf("%s run: %+v", status, run)
			}
			if status == store.StepSucceeded && string(run.Output) != "sealed" {
				t.Fatalf("output: %q", run.Output)
			}
			intr, _ := s.GetInterrupt(ctx, slots[0].ID)
			if intr.DispatchState != store.DispatchDone {
				t.Fatalf("dispatch not done: %+v", intr)
			}
			if !keyRevoked(t, s, "key_"+r.ID) {
				t.Fatalf("%s keys not revoked", status)
			}
		}
	})

	t.Run("finish_unknown", func(t *testing.T) {
		r, st, slots := mkRun("run_u", "ws_1", rNow, 1)
		start(t, s, r, st, slots)
		boundKey(t, s, "key_u", slots[0].ID)
		out := store.StepOutcome{RunID: r.ID, Seq: 0, Attempt: 1, Status: store.StepUnknown, Now: rNow}
		if err := s.FinishStep(ctx, out, nil); err != nil {
			t.Fatal(err)
		}
		run, _ := s.GetRun(ctx, r.ID)
		if run.Status != store.RunUnknown || run.EndedAt != nil {
			t.Fatalf("unknown run: %+v", run)
		}
		// The still-reserved slot is cancelled by the finish and its
		// bound key dies with it; keys of committed interrupts stay.
		i, _ := s.GetInterrupt(ctx, slots[0].ID)
		if i.Status != store.InterruptCancelled || !keyRevoked(t, s, "key_u") {
			t.Fatalf("unused slot not cancelled: %+v", i)
		}
	})

	t.Run("expire", func(t *testing.T) {
		exp := rNow.Add(-time.Second) // already due
		// With on_expire: interrupt expires and queues dispatch.
		r, st, slots := mkRun("run_x1", "ws_1", rNow, 1)
		start(t, s, r, st, slots)
		if err := s.FinishStep(ctx, waitOutcome(r, 0, &store.WaitCommit{
			InterruptID: slots[0].ID,
			Handler:     "on_ok", OnExpire: "on_exp", Context: []byte("c"), ExpiresAt: &exp,
		}), nil); err != nil {
			t.Fatal(err)
		}
		due, err := s.ListDueInterrupts(ctx, rNow, 10)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, d := range due {
			if d.ID == slots[0].ID {
				found = true
			}
		}
		if !found {
			t.Fatal("due interrupt not listed")
		}
		ok, err := s.ExpireInterrupt(ctx, slots[0].ID, rNow)
		if err != nil || !ok {
			t.Fatalf("expire: %v %v", ok, err)
		}
		i, _ := s.GetInterrupt(ctx, slots[0].ID)
		if i.Status != store.InterruptExpired || i.ResolvedBy != store.ResolvedByExpiry ||
			i.DispatchState != store.DispatchQueued || i.Attempt != 1 {
			t.Fatalf("expired: %+v", i)
		}
		run, _ := s.GetRun(ctx, r.ID)
		if run.Status != store.RunWaiting {
			t.Fatalf("run should stay waiting for on_expire: %+v", run)
		}
		if ok, _ := s.ExpireInterrupt(ctx, slots[0].ID, rNow); ok {
			t.Fatal("second expire committed")
		}

		// Without on_expire: the run ends expired and keys die.
		r2, st2, slots2 := mkRun("run_x2", "ws_1", rNow, 1)
		start(t, s, r2, st2, slots2)
		boundKey(t, s, "key_x2", slots2[0].ID)
		if err := s.FinishStep(ctx, waitOutcome(r2, 0, &store.WaitCommit{
			InterruptID: slots2[0].ID,
			Handler:     "on_ok", Context: []byte("c"), ExpiresAt: &exp,
		}), nil); err != nil {
			t.Fatal(err)
		}
		ok, err = s.ExpireInterrupt(ctx, slots2[0].ID, rNow)
		if err != nil || !ok {
			t.Fatalf("expire2: %v %v", ok, err)
		}
		run2, _ := s.GetRun(ctx, r2.ID)
		if run2.Status != store.RunExpired || run2.EndedAt == nil || run2.InterruptID != "" {
			t.Fatalf("expired run: %+v", run2)
		}
		if !keyRevoked(t, s, "key_x2") {
			t.Fatal("expired run kept its keys")
		}
	})

	t.Run("claim_defer_start_recover", func(t *testing.T) {
		// Three resolved interrupts → three queued dispatches.
		var ids []string
		for i := 0; i < 3; i++ {
			r, st, slots := mkRun(fmt.Sprintf("run_d%d", i), "ws_1", rNow.Add(time.Duration(i)*time.Second), 1)
			start(t, s, r, st, slots)
			if err := s.FinishStep(ctx, waitOutcome(r, 0, &store.WaitCommit{
				InterruptID: slots[0].ID, Handler: "on_ok", Context: []byte("c"),
			}), nil); err != nil {
				t.Fatal(err)
			}
			if _, err := s.ResolveInterrupt(ctx, slots[0].ID, []byte(`{}`), "usr_x", rNow); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, slots[0].ID)
		}
		// Concurrent claimers: each interrupt claimed at most once.
		var wg sync.WaitGroup
		claimed := make(chan string, 32)
		for i := 0; i < 6; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					c, err := s.ClaimDispatch(ctx, rNow, rNow.Add(time.Minute))
					if errors.Is(err, store.ErrNotFound) {
						return
					}
					if err != nil {
						t.Errorf("claim: %v", err)
						return
					}
					claimed <- c.ID
				}
			}()
		}
		wg.Wait()
		close(claimed)
		seen := map[string]int{}
		for id := range claimed {
			seen[id]++
		}
		for id, n := range seen {
			if n != 1 {
				t.Fatalf("interrupt %s claimed %d times", id, n)
			}
		}
		for _, id := range ids {
			if seen[id] != 1 {
				t.Fatalf("interrupt %s never claimed", id)
			}
		}

		// Defer the first: running → queued, attempts++.
		if err := s.DeferDispatch(ctx, ids[0], rNow); err != nil {
			t.Fatal(err)
		}
		i0, _ := s.GetInterrupt(ctx, ids[0])
		if i0.DispatchState != store.DispatchQueued || i0.DispatchAttempts != 1 {
			t.Fatalf("deferred: %+v", i0)
		}
		if err := s.DeferDispatch(ctx, ids[0], rNow); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("double defer: %v", err)
		}
		cl, err := s.ClaimDispatch(ctx, rNow, rNow.Add(time.Minute))
		if err != nil || cl.ID != ids[0] {
			t.Fatalf("reclaim: %v %+v", err, cl)
		}

		// StartHandlerStep on ids[0]: run back to running, steps++.
		run0, _ := s.GetRun(ctx, "run_d0")
		step1 := &store.RunStep{RunID: run0.ID, Seq: 1, Attempt: 1, Entry: "on_ok",
			InterruptID: ids[0], Status: store.StepRunning, StartedAt: rNow}
		newSlot := &store.Interrupt{ID: "int_d0s", WorkspaceID: "ws_1", ApplicationID: "app_1",
			RunID: run0.ID, StepSeq: 1, Status: store.InterruptSlotReserved, CreatedAt: rNow, UpdatedAt: rNow}
		if err := s.StartHandlerStep(ctx, ids[0], step1, []*store.Interrupt{newSlot}, rNow); err != nil {
			t.Fatal(err)
		}
		run0, _ = s.GetRun(ctx, run0.ID)
		if run0.Status != store.RunRunning || run0.Steps != 2 || run0.InterruptID != "" {
			t.Fatalf("resumed run: %+v", run0)
		}
		// A retry attempt does not bump steps.
		step1b := &store.RunStep{RunID: run0.ID, Seq: 1, Attempt: 2, Entry: "on_ok",
			InterruptID: ids[0], Status: store.StepRunning, StartedAt: rNow}
		// (Cannot insert attempt 2 while attempt 1 running — StartHandlerStep
		// requires waiting run; verify conflict instead.)
		if err := s.StartHandlerStep(ctx, ids[0], step1b, nil, rNow); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("handler step on running run: %v", err)
		}

		// RecoverStaleDispatch — never-started branch: ids[1] claimed,
		// claim lapsed, no step → requeued.
		stale, err := s.ListStaleDispatches(ctx, rNow.Add(2*time.Minute), 10)
		if err != nil {
			t.Fatal(err)
		}
		var staleIDs = map[string]bool{}
		for _, d := range stale {
			staleIDs[d.ID] = true
		}
		if !staleIDs[ids[1]] || !staleIDs[ids[2]] {
			t.Fatalf("stale list: %v", staleIDs)
		}
		started, err := s.RecoverStaleDispatch(ctx, ids[1], rNow.Add(2*time.Minute))
		if err != nil || started {
			t.Fatalf("recover unstarted: %v %v", started, err)
		}
		i1, _ := s.GetInterrupt(ctx, ids[1])
		if i1.DispatchState != store.DispatchQueued {
			t.Fatalf("requeued: %+v", i1)
		}

		// Started branch: start a handler step on ids[2] first.
		run2, _ := s.GetRun(ctx, "run_d2")
		step2 := &store.RunStep{RunID: run2.ID, Seq: 1, Attempt: 1, Entry: "on_ok",
			InterruptID: ids[2], Status: store.StepRunning, StartedAt: rNow}
		if err := s.StartHandlerStep(ctx, ids[2], step2, nil, rNow); err != nil {
			t.Fatal(err)
		}
		started, err = s.RecoverStaleDispatch(ctx, ids[2], rNow.Add(2*time.Minute))
		if err != nil || !started {
			t.Fatalf("recover started: %v %v", started, err)
		}
		run2, _ = s.GetRun(ctx, run2.ID)
		i2, _ := s.GetInterrupt(ctx, ids[2])
		steps2, _ := s.ListRunSteps(ctx, run2.ID)
		if run2.Status != store.RunUnknown || i2.DispatchState != "" ||
			steps2[len(steps2)-1].Status != store.StepUnknown {
			t.Fatalf("recovered: %+v %+v %+v", run2, i2, steps2)
		}
	})

	t.Run("cancel", func(t *testing.T) {
		r, st, slots := mkRun("run_c", "ws_1", rNow, 1)
		start(t, s, r, st, slots)
		boundKey(t, s, "key_c", slots[0].ID)
		if err := s.FinishStep(ctx, waitOutcome(r, 0, &store.WaitCommit{
			InterruptID: slots[0].ID, Handler: "on_ok", Context: []byte("c"),
		}), nil); err != nil {
			t.Fatal(err)
		}
		// A running run is not cancellable.
		r2, st2, _ := mkRun("run_c2", "ws_1", rNow, 0)
		start(t, s, r2, st2, nil)
		if ok, err := s.CancelRun(ctx, r2.ID, rNow); err != nil || ok {
			t.Fatalf("cancel running: %v %v", ok, err)
		}
		ok, err := s.CancelRun(ctx, r.ID, rNow)
		if err != nil || !ok {
			t.Fatalf("cancel: %v %v", ok, err)
		}
		run, _ := s.GetRun(ctx, r.ID)
		i, _ := s.GetInterrupt(ctx, slots[0].ID)
		if run.Status != store.RunCancelled || run.EndedAt == nil ||
			i.Status != store.InterruptCancelled || !keyRevoked(t, s, "key_c") {
			t.Fatalf("cancelled: %+v %+v", run, i)
		}
	})

	t.Run("retry", func(t *testing.T) {
		// Handler-step unknown → retryable.
		r, st, slots := mkRun("run_rt", "ws_1", rNow, 1)
		start(t, s, r, st, slots)
		if err := s.FinishStep(ctx, waitOutcome(r, 0, &store.WaitCommit{
			InterruptID: slots[0].ID, Handler: "on_ok", Context: []byte("c"),
		}), nil); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ResolveInterrupt(ctx, slots[0].ID, []byte(`{}`), "usr_x", rNow); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ClaimDispatch(ctx, rNow, rNow.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		step1 := &store.RunStep{RunID: r.ID, Seq: 1, Attempt: 1, Entry: "on_ok",
			InterruptID: slots[0].ID, Status: store.StepRunning, StartedAt: rNow}
		if err := s.StartHandlerStep(ctx, slots[0].ID, step1, nil, rNow); err != nil {
			t.Fatal(err)
		}
		if err := s.FinishStep(ctx, store.StepOutcome{
			RunID: r.ID, Seq: 1, Attempt: 1, Status: store.StepUnknown, Now: rNow,
		}, nil); err != nil {
			t.Fatal(err)
		}
		ok, err := s.RetryRun(ctx, r.ID, rNow)
		if err != nil || !ok {
			t.Fatalf("retry: %v %v", ok, err)
		}
		run, _ := s.GetRun(ctx, r.ID)
		i, _ := s.GetInterrupt(ctx, slots[0].ID)
		if run.Status != store.RunWaiting || run.InterruptID != slots[0].ID ||
			i.DispatchState != store.DispatchQueued || i.Attempt != 2 {
			t.Fatalf("retried: %+v %+v", run, i)
		}

		// Main-step unknown → not retryable.
		r2, st2, _ := mkRun("run_rt2", "ws_1", rNow, 0)
		start(t, s, r2, st2, nil)
		if err := s.FinishStep(ctx, store.StepOutcome{
			RunID: r2.ID, Seq: 0, Attempt: 1, Status: store.StepUnknown, Now: rNow,
		}, nil); err != nil {
			t.Fatal(err)
		}
		if ok, err := s.RetryRun(ctx, r2.ID, rNow); err != nil || ok {
			t.Fatalf("main retry: %v %v", ok, err)
		}
	})
}
