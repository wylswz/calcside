// calcside is a multi-tenant Starlark execution sandbox server.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"calcside/cmd/calcside/internal/config"
	"calcside/cmd/calcside/internal/service/sandbox"
	"calcside/internal/node/subproc"
	"calcside/internal/placement"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == subproc.ChildArg {
		if err := subproc.RunChild(os.Stdin, os.Stdout, nil); err != nil {
			fmt.Fprintln(os.Stderr, "instance:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) < 2 || os.Args[1] != "serve" {
		fmt.Fprintln(os.Stderr, "usage: calcside serve [flags]")
		os.Exit(2)
	}
	cfg, err := config.Parse(os.Args[2:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(2)
	}
	if err := serve(cfg); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func serve(cfg config.Config) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	app, cleanup, err := initializeApp(ctx, cfg)
	if err != nil {
		return err
	}
	defer cleanup()

	// Expiry is a status transition, so it is the API tier's to make.
	expired := make(chan struct{})
	go func() {
		defer close(expired)
		expireLoop(ctx, app.Sandbox, cfg.ReaperInterval)
	}()
	defer func() {
		stop()
		<-expired
	}()

	srv := app.Server
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	defer func() {
		stop()
		<-shutdownDone
	}()
	slog.Info("listening", "addr", srv.Addr)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	return nil
}

// parseWorkers parses --workers entries of the form "nodeID=host:port".
func parseWorkers(spec string) ([]placement.NodeRef, error) {
	var out []placement.NodeRef
	for _, ent := range strings.Split(spec, ",") {
		id, addr, ok := strings.Cut(strings.TrimSpace(ent), "=")
		if !ok || id == "" || addr == "" {
			return nil, fmt.Errorf("--workers: bad entry %q, want nodeID=host:port", ent)
		}
		out = append(out, placement.NodeRef{NodeID: id, Addr: addr})
	}
	return out, nil
}

// expireLoop sweeps instances whose sliding TTL elapsed. Nodes reclaim
// their own memory after a grace period, but only the API tier can
// record that an instance expired.
func expireLoop(ctx context.Context, svc *sandbox.Service, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			n, err := svc.ExpireDue(ctx, 200)
			if err != nil {
				slog.Error("expire sweep failed", "err", err)
			} else if n > 0 {
				slog.Info("expired instances", "count", n)
			}
		case <-ctx.Done():
			return
		}
	}
}
