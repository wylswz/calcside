package main

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// startServing launches the persistent-instance reconcile loop and, in
// local topology, the in-process node agent. The returned function
// blocks until both stop — call it during shutdown BEFORE the Wire
// cleanup closes the store so no reconcile runs under a closing DB.
func (a *application) startServing(ctx context.Context) func() {
	var wg sync.WaitGroup
	if a.Serving != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			t := time.NewTicker(5 * time.Second)
			defer t.Stop()
			for {
				if err := a.Serving.Reconcile(ctx); err != nil && ctx.Err() == nil {
					slog.Warn("serving reconcile failed")
				}
				select {
				case <-ctx.Done():
					return
				case <-t.C:
				}
			}
		}()
	}
	return wg.Wait
}
