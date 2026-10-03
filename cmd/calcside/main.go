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
	"strings"
	"syscall"
	"time"

	"calcside/internal/api"
	"calcside/internal/audit"
	"calcside/internal/auth"
	"calcside/internal/config"
	"calcside/internal/node"
	"calcside/internal/node/subproc"
	"calcside/internal/placement"
	"calcside/internal/policy"
	"calcside/internal/runtime"
	"calcside/internal/runtime/remote"
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
	if len(os.Args) == 2 && os.Args[1] == subproc.ChildArg {
		if err := subproc.RunChild(os.Stdin, os.Stdout); err != nil {
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

	st, err := store.Open(ctx, cfg.Store, cfg.DSN)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	rec := audit.NewRecorder(st)
	defer rec.Close()

	limits := cfg.ExecLimits()
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

	// Execution tier. In-process by default; --workers switches the API
	// tier to forward to remote worker nodes instead.
	ncfg := node.Config{
		Limits:             limits,
		ExtAllowSources:    cfg.ExtAllowSources,
		ExtLocalRoots:      cfg.ExtLocalRoots,
		ExtCacheDir:        cfg.ExtCacheDir,
		ExtFetchTimeout:    cfg.ExtFetchTimeout,
		MaxConcurrentExecs: cfg.MaxConcurrentExecs,
		ExecMemoryLimit:    cfg.ExecMemoryLimit,
		EvalTimeout:        cfg.PolicyEvalTimeout,
		MaxInstances:       cfg.MaxInstancesPerNode,
		ReapInterval:       cfg.ReaperInterval,
	}
	nd := node.Build(ncfg)
	defer nd.Close()

	var rt runtime.Runtime = nd.Manager
	if cfg.Workers != "" {
		if cfg.WorkerKey == "" {
			return fmt.Errorf("--workers requires --worker-key")
		}
		nodes, err := parseWorkers(cfg.Workers)
		if err != nil {
			return err
		}
		slog.Info("remote execution tier", "workers", len(nodes))
		rt = remote.NewClient(
			[]byte(cfg.WorkerKey),
			cfg.NodeID,
			func(context.Context) ([]placement.NodeRef, error) { return nodes, nil },
			func(ctx context.Context, id string) (*placement.Binding, error) {
				in, err := st.GetInstance(ctx, id)
				if err != nil {
					return nil, err
				}
				if in.NodeID == "" {
					return nil, placement.ErrNotBound
				}
				return &placement.Binding{InstanceID: id, NodeID: in.NodeID, Epoch: in.LeaseEpoch}, nil
			})
	} else if cfg.InstanceIsolation == config.IsolationProcess {
		sup, err := subproc.New(subproc.Options{
			Child:              subproc.ChildConfig{Node: ncfg},
			MaxInstances:       cfg.MaxInstancesPerNode,
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
		slog.Info("instance isolation", "mode", cfg.InstanceIsolation)
		rt = sup
	} else {
		nd.Manager.StartReaper()
	}

	// Instances a previous incarnation of this node owned cannot be
	// recovered: their Starlark globals lived in that process's memory.
	if n, err := st.MarkRunningAsLostForNode(ctx, cfg.NodeID); err != nil {
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
		Runtime:             rt,
		Secrets:             vaultSvc,
		Audit:               rec,
		Registry:            nd.Registry,
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
		Catalog: catalog.New(nd.Registry), Sandbox: sandboxSvc,
		Auth: svc, Web: webFS, GoogleEnabled: flow != nil,
		Dev: cfg.Dev, Anonymous: anon,
	})
	if cfg.Workers != "" {
		// Workers resolve local extension sources through the API —
		// they have no filesystem roots of their own. Signed with the
		// same shared key as the runtime protocol.
		mux2 := http.NewServeMux()
		mux2.Handle(remote.ExtTreePath, remote.ExtTreeHandler(cfg.ExtLocalRoots, []byte(cfg.WorkerKey)))
		mux2.Handle("/", mux)
		mux = mux2
	}
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
