// calcside-worker is a standalone execution node. It serves the
// runtime contract over authenticated HTTP to the API tier and holds no
// store handle: every request arrives self-contained.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"calcside/cmd/calcside-worker/internal/config"
	"calcside/internal/node/subproc"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == subproc.ChildArg {
		if err := subproc.RunChild(os.Stdin, os.Stdout, configureChild); err != nil {
			fmt.Fprintln(os.Stderr, "instance:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) < 2 || os.Args[1] != "serve" {
		fmt.Fprintln(os.Stderr, "usage: calcside-worker serve [flags]")
		os.Exit(2)
	}
	cfg, err := config.ParseWorker(os.Args[2:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(2)
	}
	if err := serve(cfg); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

// nodeID resolves the stable node identity: explicit flag/env wins,
// then a persisted file (created on first boot), then an ephemeral id
// with a loud warning — an ephemeral id orphans every instance this
// node held.
func nodeID(cfg config.WorkerConfig) (string, error) {
	if cfg.NodeID != "" {
		return cfg.NodeID, nil
	}
	if cfg.NodeIDFile != "" {
		if b, err := os.ReadFile(cfg.NodeIDFile); err == nil {
			if id := strings.TrimSpace(string(b)); id != "" {
				return id, nil
			}
		}
		id := "node-" + randHex(8)
		if err := os.WriteFile(cfg.NodeIDFile, []byte(id+"\n"), 0o600); err != nil {
			return "", fmt.Errorf("--node-id-file: %w", err)
		}
		return id, nil
	}
	id := "node-" + randHex(8)
	slog.Warn("no --node-id or --node-id-file: using ephemeral node id; instances owned on restart will be orphaned", "node_id", id)
	return id, nil
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func serve(cfg config.WorkerConfig) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	app, cleanup, err := initializeWorker(cfg)
	if err != nil {
		return err
	}
	defer cleanup()

	srv := app.Server
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		slog.Info("draining", "node_id", app.NodeID)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	defer func() {
		stop()
		<-shutdownDone
	}()
	slog.Info("listening", "addr", srv.Addr, "node_id", app.NodeID)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	return nil
}
