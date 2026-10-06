package main

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"

	"calcside/cmd/calcside/internal/api"
	"calcside/cmd/calcside/internal/artifact"
	"calcside/cmd/calcside/internal/audit"
	"calcside/cmd/calcside/internal/auth"
	"calcside/cmd/calcside/internal/config"
	"calcside/cmd/calcside/internal/extensions"
	_ "calcside/cmd/calcside/internal/gormstore"
	policysvc "calcside/cmd/calcside/internal/service/policy"
	"calcside/cmd/calcside/internal/service/sandbox"
	"calcside/cmd/calcside/internal/vaultcipher"
	"calcside/cmd/calcside/internal/worker"
	"calcside/internal/capability"
	capext "calcside/internal/capability/ext"
	common "calcside/internal/config"
	"calcside/internal/node"
	"calcside/internal/node/subproc"
	"calcside/internal/placement"
	"calcside/internal/runtime"
	"calcside/internal/runtime/remote"
	"calcside/internal/store"
	webpkg "calcside/web"
)

type application struct {
	Server  *http.Server
	Sandbox *sandbox.Service
}

func provideStore(ctx context.Context, cfg config.Config) (store.Store, func(), error) {
	st, err := store.Open(ctx, cfg.Store, cfg.DSN)
	if err != nil {
		return nil, nil, fmt.Errorf("open store: %w", err)
	}
	return st, func() { _ = st.Close() }, nil
}

func provideRecorder(st store.Store) (*audit.Recorder, func()) {
	rec := audit.NewRecorder(st)
	return rec, rec.Close
}

func provideCipher(cfg config.Config) (*vaultcipher.Cipher, error) {
	if cfg.SecretKey == "" {
		slog.Warn("secrets vault disabled (no --secret-key)")
		return nil, nil
	}
	cipher, err := vaultcipher.NewCipher(cfg.SecretKey)
	if err != nil {
		return nil, fmt.Errorf("--secret-key: %w", err)
	}
	return cipher, nil
}

func provideLimits(cfg config.Config) capability.ServerLimits {
	if len(cfg.NetAllowCIDRs) > 0 {
		slog.Warn("net: private/reserved address blocking relaxed", "cidrs", cfg.NetAllowCIDRs)
	}
	return cfg.ExecLimits()
}

func provideNodeConfig(cfg config.Config, limits capability.ServerLimits) node.Config {
	return node.Config{
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
}

func provideNode(cfg node.Config) (*node.Node, func()) {
	nd := node.Build(cfg)
	return nd, nd.Close
}

func provideRuntime(cfg config.Config, st store.Store, ncfg node.Config, nd *node.Node) (runtime.Runtime, func(), error) {
	// Execution tier. In-process by default; --workers switches the API
	// tier to forward to remote worker nodes instead.
	if cfg.Workers != "" {
		if cfg.WorkerKey == "" {
			return nil, nil, fmt.Errorf("--workers requires --worker-key")
		}
		nodes, err := parseWorkers(cfg.Workers)
		if err != nil {
			return nil, nil, err
		}
		slog.Info("remote execution tier", "workers", len(nodes))
		client := worker.NewClient(
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
		return client, client.HTTP.CloseIdleConnections, nil
	} else if cfg.InstanceIsolation == common.IsolationProcess {
		sup, err := subproc.New(subproc.Options{
			Child:              subproc.ChildConfig{Node: ncfg},
			MaxInstances:       cfg.MaxInstancesPerNode,
			MaxConcurrentExecs: cfg.MaxConcurrentExecs,
			ReapInterval:       cfg.ReaperInterval,
			MemoryMax:          cfg.InstanceMemoryMax,
			CgroupParent:       cfg.InstanceCgroupParent,
		})
		if err != nil {
			return nil, nil, err
		}
		sup.StartReaper()
		slog.Info("instance isolation", "mode", cfg.InstanceIsolation)
		return sup, sup.Close, nil
	}
	nd.Manager.StartReaper()
	return nd.Manager, func() {}, nil
}

func provideGlobalPolicies(cfg config.Config) (map[string]string, error) {
	// Global policies are read once here and travel with every create
	// request, so a node never depends on its own copy of --policy-dir.
	globalPolicies, err := policysvc.LoadDir(cfg.PolicyDir)
	if err != nil {
		return nil, fmt.Errorf("--policy-dir: %w", err)
	}

	return globalPolicies, nil
}

func provideSandboxOptions(ctx context.Context, cfg config.Config, st store.Store, rt runtime.Runtime, vaultSvc sandbox.SecretResolver, rec sandbox.AuditSink, reg *capability.Registry, limits capability.ServerLimits, globalPolicies map[string]string) (sandbox.Options, error) {
	// Instances a previous incarnation of this node owned cannot be
	// recovered: their Starlark globals lived in that process's memory.
	if n, err := st.MarkRunningAsLostForNode(ctx, cfg.NodeID); err != nil {
		return sandbox.Options{}, fmt.Errorf("recover: %w", err)
	} else if n > 0 {
		slog.Warn("marked lost instances", "count", n)
	}

	return sandbox.Options{
		Store:               st,
		Runtime:             rt,
		Secrets:             vaultSvc,
		Audit:               rec,
		Registry:            reg,
		Limits:              limits,
		GlobalPolicies:      globalPolicies,
		MaxInstancesPerUser: cfg.MaxInstancesPerUser,
	}, nil
}

func provideAuth(cfg config.Config, st store.Store) *auth.Service {
	return auth.NewService(st, cfg.CookieSecure)
}

func provideBasic(ctx context.Context, cfg config.Config, svc *auth.Service) (*auth.BasicLogin, error) {
	return auth.NewBasicLogin(ctx, svc, cfg.AdminUsername, cfg.AdminPassword)
}

func serverAddr(cfg config.Config) string {
	if cfg.Dev && !cfg.AddrExplicit {
		return "127.0.0.1:8080"
	}
	return cfg.Addr
}

func provideAnonymous(ctx context.Context, cfg config.Config, st store.Store) (*store.User, error) {
	if !cfg.Dev {
		return nil, nil
	}
	if err := config.CheckDevAddr(serverAddr(cfg), cfg.DevAllowRemote); err != nil {
		return nil, err
	}
	slog.Warn("DEV MODE ENABLED — no login required; all API requests run as anonymous — do not use in production")
	anon, err := st.UpsertUserByEmail(ctx, "anonymous@localhost", "anonymous", "")
	if err != nil {
		return nil, fmt.Errorf("dev anonymous user: %w", err)
	}
	return anon, nil
}

func provideGoogle(ctx context.Context, cfg config.Config) (*auth.GoogleFlow, error) {
	flow, err := auth.InitGoogle(ctx, auth.GoogleConfig{
		ClientID:       cfg.GoogleClientID,
		ClientSecret:   cfg.GoogleClientSecret,
		ConsoleOrigin:  cfg.ConsoleOrigin,
		AllowedDomains: cfg.GoogleAllowedDomains,
	})
	if err != nil {
		return nil, fmt.Errorf("google oidc: %w", err)
	}
	return flow, nil
}

func provideWeb() fs.FS {
	if sub, err := fs.Sub(webpkg.Dist, "dist"); err == nil {
		return sub
	}
	return nil
}

func provideServer(cfg config.Config, deps api.Deps, flow *auth.GoogleFlow) *http.Server {
	deps.Dev = cfg.Dev
	deps.GoogleEnabled = flow != nil
	svc := deps.Auth
	mux := api.Handler(deps)
	if cfg.Workers != "" {
		// Workers resolve local extension sources through the API —
		// they have no filesystem roots of their own. Signed with the
		// same shared key as the runtime protocol.
		mux2 := http.NewServeMux()
		mux2.Handle(remote.ExtTreePath, extensions.ExtTreeHandler(cfg.ExtLocalRoots, []byte(cfg.WorkerKey)))
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

	if deps.Previews != nil {
		mux = deps.Previews.Wrap(mux)
	}
	return &http.Server{Addr: serverAddr(cfg), Handler: mux}
}

func provideArtifacts(cfg config.Config, sbx *sandbox.Service) (*api.ArtifactPreviews, func(), error) {
	previews, err := api.NewArtifactPreviews(artifact.Config{BaseURL: cfg.ArtifactPreviewBaseURL, ConsoleOrigin: cfg.ConsoleOrigin, AllowScripts: cfg.ArtifactAllowScripts, AllowLocalHTTP: cfg.Dev}, sbx)
	if err != nil {
		return nil, nil, err
	}
	return previews, previews.Close, nil
}

func provideExtCatalog(cfg config.Config) capext.CatalogProvider {
	return extensions.NewCatalog(capext.Options{LocalRoots: cfg.ExtLocalRoots, AllowSources: cfg.ExtAllowSources, CacheDir: cfg.ExtCacheDir, FetchTimeout: cfg.ExtFetchTimeout})
}
