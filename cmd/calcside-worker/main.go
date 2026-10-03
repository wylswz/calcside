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

	"calcside/internal/config"
	"calcside/internal/node"
	"calcside/internal/node/subproc"
	"calcside/internal/runtime"
	"calcside/internal/runtime/remote"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == subproc.ChildArg {
		if err := subproc.RunChild(os.Stdin, os.Stdout); err != nil {
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
	if cfg.SharedKey == "" {
		return fmt.Errorf("--shared-key is required: an unauthenticated worker is an open execution endpoint")
	}
	nodeID, err := nodeID(cfg)
	if err != nil {
		return err
	}
	bootID := randHex(8)
	slog.Info("worker identity", "node_id", nodeID, "boot_id", bootID)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	ncfg := node.Config{
		Limits:             cfg.ExecLimits(),
		ExtAllowSources:    cfg.ExtAllowSources,
		ExtLocalRoots:      cfg.ExtLocalRoots,
		ExtCacheDir:        cfg.ExtCacheDir,
		ExtFetchTimeout:    cfg.ExtFetchTimeout,
		MaxConcurrentExecs: cfg.MaxConcurrentExecs,
		ExecMemoryLimit:    cfg.ExecMemoryLimit,
		EvalTimeout:        cfg.PolicyEvalTimeout,
		MaxInstances:       cfg.MaxInstances,
		ReapInterval:       cfg.ReaperInterval,
	}
	if cfg.APIAddr != "" {
		// Local ext sources live on the API tier's filesystem; resolve
		// them over the wire into our own cache volume.
		ncfg.ExtLocalResolver = remote.LocalExtResolver(cfg.APIAddr, nodeID, []byte(cfg.SharedKey), cfg.ExtCacheDir)
	}
	var rt runtime.Runtime
	if cfg.InstanceIsolation == config.IsolationProcess {
		sup, err := subproc.New(subproc.Options{
			Child: subproc.ChildConfig{
				Node:    ncfg,
				APIAddr: cfg.APIAddr,
				NodeID:  nodeID,
				APIKey:  []byte(cfg.SharedKey),
			},
			MaxInstances:       cfg.MaxInstances,
			MaxConcurrentExecs: cfg.MaxConcurrentExecs,
			ReapInterval:       cfg.ReaperInterval,
			MemoryMax:          cfg.InstanceMemoryMax,
			CgroupParent:       cfg.InstanceCgroupParent,
		})
		if err != nil {
			return err
		}
		defer sup.Close()
		sup.StartReaper()
		rt = sup
	} else {
		nd := node.Build(ncfg)
		defer nd.Close()
		nd.Manager.StartReaper()
		rt = nd.Manager
	}
	slog.Info("instance isolation", "mode", cfg.InstanceIsolation)

	// /healthz is operational, not protocol: it lives outside the
	// authenticated handler so orchestrators can probe without a key.
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle("/", remote.NewHandler(rt, []byte(cfg.SharedKey)))

	srv := &http.Server{
		Addr:        cfg.Addr,
		Handler:     mux,
		ReadTimeout: 30 * time.Second,
		// WriteTimeout must exceed the longest exec, or the server
		// truncates responses mid-flight.
		WriteTimeout: cfg.MaxExecTimeout + 30*time.Second,
	}
	go func() {
		<-ctx.Done()
		slog.Info("draining", "node_id", nodeID)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	slog.Info("listening", "addr", cfg.Addr, "node_id", nodeID)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	return nil
}
