package main

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"calcside/cmd/calcside/internal/api"
	"calcside/cmd/calcside/internal/artifact"
	"calcside/cmd/calcside/internal/audit"
	"calcside/cmd/calcside/internal/auth"
	"calcside/cmd/calcside/internal/config"
	"calcside/cmd/calcside/internal/extensions"
	_ "calcside/cmd/calcside/internal/gormstore"
	"calcside/cmd/calcside/internal/service/access"
	appsvc "calcside/cmd/calcside/internal/service/application"
	policysvc "calcside/cmd/calcside/internal/service/policy"
	runsvc "calcside/cmd/calcside/internal/service/run"
	"calcside/cmd/calcside/internal/service/sandbox"
	"calcside/cmd/calcside/internal/service/serving"
	"calcside/cmd/calcside/internal/vaultcipher"
	"calcside/internal/capability"
	capext "calcside/internal/capability/ext"
	capfs "calcside/internal/capability/fs"
	capio "calcside/internal/capability/io"
	capnet "calcside/internal/capability/net"
	"calcside/internal/placement"
	"calcside/internal/runtime"
	"calcside/internal/runtime/worker"
	"calcside/internal/store"
	webpkg "calcside/web"
)

type application struct {
	Server  *http.Server
	Sandbox *sandbox.Service
	// Serving is the persistent-instance scheduler.
	Serving *serving.Service
	// Runs is the EP-0016 durable-run dispatcher.
	Runs *runsvc.Service
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

// provideExtRepo builds the API extension artifact repository: source
// policy + captured immutable trees, resolved from cache.
func provideExtRepo(cfg config.Config, st store.Store) (*extensions.Repository, error) {
	return extensions.NewRepository(st, capext.Options{
		AllowSources: cfg.ExtAllowSources,
		LocalRoots:   cfg.ExtLocalRoots,
		CacheDir:     cfg.ExtCacheDir,
		FetchTimeout: cfg.ExtFetchTimeout,
	})
}

// provideRegistry builds the capability registry directly from the
// factories — spec validation uses the immutable artifact resolver.
func provideRegistry(repo *extensions.Repository) *capability.Registry {
	reg := capability.NewRegistry()
	reg.Register(capfs.Factory())
	reg.Register(capnet.Factory())
	reg.Register(capio.Factory())
	reg.Register(capext.Factory(repo.Options()))
	return reg
}

func provideRuntime(cfg config.Config, st store.Store) (runtime.Runtime, func(), error) {
	// The API tier never executes code — every call forwards to a
	// remote worker node; --workers/--worker-key are required.
	if cfg.WorkerKey == "" {
		return nil, nil, fmt.Errorf("--worker-key is required")
	}
	nodes, err := config.ParseWorkers(cfg.Workers)
	if err != nil {
		return nil, nil, err
	}
	slog.Info("remote execution tier", "workers", len(nodes))
	client := worker.NewClient(
		[]byte(cfg.WorkerKey),
		"api",
		func(context.Context) ([]placement.NodeRef, error) { return nodes, nil },
		func(ctx context.Context, id string) (*placement.Binding, error) {
			if strings.HasPrefix(id, string(store.PrefixServingReplica)) {
				// Persistent replica: route only to the node that
				// holds the live, current-generation ready lease.
				rep, err := st.GetServingReplica(ctx, id)
				if err != nil {
					return nil, err
				}
				inst, err := st.GetPersistentInstance(ctx, rep.WorkspaceID, rep.InstanceID)
				if err != nil {
					return nil, err
				}
				live, err := st.ListServingNodes(ctx, time.Now())
				if err != nil {
					return nil, err
				}
				current := rep.State == store.ServingReplicaReady &&
					rep.LeaseUntil.After(time.Now()) &&
					inst.Enabled && inst.Generation == rep.Generation &&
					inst.ReleaseID == rep.ReleaseID &&
					inst.ActiveReplicaID == rep.ID
				for _, n := range live {
					if n.NodeID == rep.NodeID && n.BootID == rep.BootID &&
						n.Incarnation == rep.NodeIncarnation && current {
						return &placement.Binding{
							InstanceID: rep.ID, NodeID: rep.NodeID, Epoch: rep.Epoch,
						}, nil
					}
				}
				return nil, placement.ErrNotBound
			}
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

func provideSandboxOptions(ctx context.Context, cfg config.Config, st store.Store, rt runtime.Runtime, vaultSvc sandbox.SecretResolver, rec sandbox.AuditSink, reg *capability.Registry, limits capability.ServerLimits, globalPolicies map[string]string, ac *access.Checker, repo *extensions.Repository) (sandbox.Options, error) {
	extOpts := repo.Options()
	return sandbox.Options{
		Store:               st,
		Runtime:             rt,
		Secrets:             vaultSvc,
		Audit:               rec,
		Registry:            reg,
		Ext:                 &extOpts,
		SpecPinner:          repo,
		Limits:              limits,
		GlobalPolicies:      globalPolicies,
		Access:              ac,
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

// provideServingService is the API-side persistent scheduler. It owns
// SQL; workers never touch the database.
func provideServingService(cfg config.Config, st store.Store, secrets sandbox.SecretResolver,
	globalPolicies map[string]string) (*serving.Service, error) {
	refs, err := config.ParseWorkers(cfg.Workers)
	if err != nil {
		return nil, err
	}
	nodes := map[string]string{}
	for _, r := range refs {
		nodes[r.NodeID] = r.Addr
	}
	return serving.New(serving.Options{
		Store: st, Secrets: secrets, Nodes: nodes,
		GlobalPolicies: globalPolicies,
		LeaseDuration:  30 * time.Second, NodeTTL: 45 * time.Second,
	})
}

func provideServer(cfg config.Config, st store.Store, deps api.Deps, flow *auth.GoogleFlow, sctl *serving.Service) *http.Server {
	deps.Dev = cfg.Dev
	deps.GoogleEnabled = flow != nil
	svc := deps.Auth
	mux := api.Handler(deps)
	if cfg.Workers != "" {
		// The api-callback surface (ext tree + node control) is signed
		// with the same shared key as the runtime protocol.
		mux2 := http.NewServeMux()
		mux2.Handle("/internal/v1/", extensions.WithControl(st, []byte(cfg.WorkerKey), sctl))
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

func provideExtOptions(cfg config.Config) *capext.Options {
	return &capext.Options{LocalRoots: cfg.ExtLocalRoots, AllowSources: cfg.ExtAllowSources, CacheDir: cfg.ExtCacheDir, FetchTimeout: cfg.ExtFetchTimeout}
}

func provideExtCatalog(opts *capext.Options) capext.CatalogProvider {
	return extensions.NewCatalog(*opts)
}

// provideApplicationService wires the durable named-function service:
// drafts + publish/test/invoke against the shared runtime and access
// checker. The instance quota doubles as the application quota until a
// dedicated flag exists.
func provideApplicationService(st store.Store, rt runtime.Runtime, sb *sandbox.Service,
	ac *access.Checker, rec appsvc.AuditSink, cfg config.Config, rs *runsvc.Service) (*appsvc.Service, error) {
	return appsvc.New(appsvc.Options{
		Store: st, Runtime: rt, Sandbox: sb, Access: ac, Audit: rec, Runs: rs,
		MaxPerUser: cfg.MaxInstancesPerUser,
	})
}

// provideRunService wires the durable-run lifecycle: Begin/Complete for
// Test/Invoke plus the background dispatch loop started by serve.
func provideRunService(st store.Store, rt runtime.Runtime, sb *sandbox.Service,
	ac *access.Checker, rec runsvc.AuditSink, c *vaultcipher.Cipher, cfg config.Config) (*runsvc.Service, error) {
	return runsvc.New(runsvc.Options{
		Store: st, Runtime: rt, Sandbox: sb, Access: ac, Audit: rec,
		Cipher: c, ConsoleOrigin: cfg.ConsoleOrigin,
	})
}
