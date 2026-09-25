// calcside is a multi-tenant Starlark execution sandbox server.
package main

import (
	"context"
	"fmt"
	"io/fs"
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
	"calcside/internal/secrets"
	"calcside/internal/store"
	_ "calcside/internal/store/gormstore"
	webpkg "calcside/web"
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
		SecretsAllowHTTP:    cfg.SecretsAllowHTTP,
	}
	var cipher *secrets.Cipher
	if cfg.SecretKey != "" {
		cipher, err = secrets.NewCipher(cfg.SecretKey)
		if err != nil {
			return fmt.Errorf("--secret-key: %w", err)
		}
	} else {
		slog.Warn("secrets vault disabled (no --secret-key)")
	}
	mgr := instance.New(st, eng, reg, rec, cfg.PolicyDir, cfg.PolicyEvalTimeout,
		limits, nil, cipher, nil, cfg.ReaperInterval)
	if n, err := mgr.Recover(ctx); err != nil {
		return fmt.Errorf("recover: %w", err)
	} else if n > 0 {
		slog.Warn("marked lost instances", "count", n)
	}
	mgr.StartReaper()

	svc := auth.NewService(st, cfg.CookieSecure)
	var anon *store.User
	if cfg.Dev {
		if !cfg.AddrExplicit {
			cfg.Addr = "127.0.0.1:8080"
		}
		if err := config.CheckDevAddr(cfg.Addr, cfg.DevAllowRemote); err != nil {
			return err
		}
		slog.Warn("DEV MODE ENABLED — no login required; all API requests run as anonymous — do not use in production")
		anon, err = st.UpsertUserByEmail(ctx, "anonymous@localhost", "anonymous", "")
		if err != nil {
			return fmt.Errorf("dev anonymous user: %w", err)
		}
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

	var webFS fs.FS
	if sub, err := fs.Sub(webpkg.Dist, "dist"); err == nil {
		webFS = sub
	}
	mux := api.Handler(api.Deps{
		Store: st, Manager: mgr, Registry: reg, Auth: svc,
		Web: webFS, GoogleEnabled: flow != nil, Cipher: cipher,
		Dev: cfg.Dev, Anonymous: anon,
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
