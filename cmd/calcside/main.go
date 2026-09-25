// calcside is a multi-tenant Starlark execution sandbox server.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"calcside/internal/api"
	"calcside/internal/audit"
	"calcside/internal/auth"
	"calcside/internal/capability"
	capfs "calcside/internal/capability/fs"
	capio "calcside/internal/capability/io"
	capnet "calcside/internal/capability/net"
	"calcside/internal/config"
	"calcside/internal/engine"
	"calcside/internal/instance"
	"calcside/internal/store"
	_ "calcside/internal/store/sqlite"
)

func main() {
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

	st, err := store.Open(ctx, cfg.Store, cfg.DSN)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	reg := capability.NewRegistry()
	reg.Register(capfs.Factory())
	reg.Register(capnet.Factory())
	reg.Register(capio.Factory())

	rec := audit.NewRecorder(st)
	defer rec.Close()

	eng := engine.New(cfg.MaxConcurrentExecs, engine.WithMemoryLimit(cfg.ExecMemoryLimit))
	defer eng.Close()
	limits := instance.ServerLimits{
		MaxInstancesPerUser: cfg.MaxInstancesPerUser,
		DefaultTTL:          cfg.DefaultTTL,
		MaxTTL:              cfg.MaxTTL,
		MaxExecTimeout:      cfg.MaxExecTimeout,
		MaxSteps:            cfg.MaxSteps,
		MaxFSQuotaBytes:     256 << 20,
		MaxOutputBytes:      cfg.MaxOutputBytes,
		NetAllowPrivate:     cfg.NetAllowPrivate,
		MaxNetResponseBytes: cfg.MaxNetResponseBytes,
	}
	mgr := instance.New(st, eng, reg, rec, cfg.PolicyDir, cfg.PolicyEvalTimeout,
		limits, nil, nil, cfg.ReaperInterval)
	if n, err := mgr.Recover(ctx); err != nil {
		return fmt.Errorf("recover: %w", err)
	} else if n > 0 {
		slog.Warn("marked lost instances", "count", n)
	}
	mgr.StartReaper()

	svc := auth.NewService(st, cfg.CookieSecure, cfg.DevLogin)
	if cfg.DevLogin {
		slog.Warn("dev login ENABLED — do not use in production")
	}
	flow, err := auth.InitGoogle(ctx, auth.GoogleConfig{
		ClientID:       cfg.GoogleClientID,
		ClientSecret:   cfg.GoogleClientSecret,
		BaseURL:        cfg.BaseURL,
		AllowedDomains: cfg.GoogleAllowedDomains,
	})
	if err != nil {
		return fmt.Errorf("google oidc: %w", err)
	}

	mux := api.Handler(api.Deps{
		Store: st, Manager: mgr, Registry: reg, Auth: svc,
	})
	if flow != nil {
		flow.Bind(svc)
		mux2 := http.NewServeMux()
		mux2.Handle("/auth/google/login", flow.LoginHandler())
		mux2.Handle("/auth/google/callback", flow.CallbackHandler())
		mux2.Handle("/", mux)
		mux = mux2
	} else {
		slog.Info("google login disabled (no --google-client-id)")
	}

	srv := &http.Server{Addr: cfg.Addr, Handler: mux}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	slog.Info("listening", "addr", cfg.Addr)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	mgr.StopReaper()
	rec.Close()
	return nil
}
