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
	capext "calcside/internal/capability/ext"
	capfs "calcside/internal/capability/fs"
	capio "calcside/internal/capability/io"
	capnet "calcside/internal/capability/net"
	"calcside/internal/config"
	"calcside/internal/engine"
	"calcside/internal/instance"
	"calcside/internal/policy"
	"calcside/internal/secrets"
	auditsvc "calcside/internal/service/audit"
	"calcside/internal/service/catalog"
	"calcside/internal/service/iam"
	policysvc "calcside/internal/service/policy"
	"calcside/internal/service/sandbox"
	"calcside/internal/service/vault"
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
	reg.Register(capext.Factory(capext.Options{
		AllowSources: cfg.ExtAllowSources,
		LocalRoots:   cfg.ExtLocalRoots,
		CacheDir:     cfg.ExtCacheDir,
		FetchTimeout: cfg.ExtFetchTimeout,
	}))

	rec := audit.NewRecorder(st)
	defer rec.Close()

	eng := engine.New(cfg.MaxConcurrentExecs, engine.WithMemoryLimit(cfg.ExecMemoryLimit))
	defer eng.Close()
	limits := capability.ServerLimits{
		DefaultTTL:          cfg.DefaultTTL,
		MaxTTL:              cfg.MaxTTL,
		MaxExecTimeout:      cfg.MaxExecTimeout,
		MaxSteps:            cfg.MaxSteps,
		MaxFSQuotaBytes:     256 << 20,
		MaxOutputBytes:      cfg.MaxOutputBytes,
		NetAllowPrivate:     cfg.NetAllowPrivate,
		NetAllowCIDRs:       cfg.NetAllowCIDRs,
		MaxNetResponseBytes: cfg.MaxNetResponseBytes,
		SecretsAllowHTTP:    cfg.SecretsAllowHTTP,
	}
	if len(cfg.NetAllowCIDRs) > 0 {
		slog.Warn("net: private/reserved address blocking relaxed", "cidrs", cfg.NetAllowCIDRs)
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

	// Execution tier. It is in-process in this binary, but the API tier
	// below reaches it only through runtime.Runtime and it holds no
	// store handle, so the same code serves a separate worker.
	mgr := instance.New(instance.Options{
		Engine:       eng,
		Registry:     reg,
		Limits:       limits,
		EvalTimeout:  cfg.PolicyEvalTimeout,
		MaxInstances: cfg.MaxInstancesPerNode,
		ReapInterval: cfg.ReaperInterval,
	})
	mgr.StartReaper()
	defer mgr.StopReaper()

	// Instances a previous process owned cannot be recovered: their
	// Starlark globals lived in that process's memory.
	if n, err := st.MarkRunningAsLost(ctx); err != nil {
		return fmt.Errorf("recover: %w", err)
	} else if n > 0 {
		slog.Warn("marked lost instances", "count", n)
	}

	// Global policies are read once here and travel with every create
	// request, so a node never depends on its own copy of --policy-dir.
	globalPolicies, err := policy.LoadDir(cfg.PolicyDir)
	if err != nil {
		return fmt.Errorf("--policy-dir: %w", err)
	}

	vaultSvc := vault.New(st, cipher)
	sandboxSvc := sandbox.New(sandbox.Options{
		Store:               st,
		Runtime:             mgr,
		Secrets:             vaultSvc,
		Audit:               rec,
		Registry:            reg,
		Limits:              limits,
		GlobalPolicies:      globalPolicies,
		MaxInstancesPerUser: cfg.MaxInstancesPerUser,
	})
	// Expiry is a status transition, so it is the API tier's to make.
	go expireLoop(ctx, sandboxSvc, cfg.ReaperInterval)

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
		IAM: iam.New(st, nil), Vault: vaultSvc,
		Policy: policysvc.New(st), Audit: auditsvc.New(st),
		Catalog: catalog.New(reg), Sandbox: sandboxSvc,
		Auth: svc, Web: webFS, GoogleEnabled: flow != nil,
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
	rec.Close()
	return nil
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
