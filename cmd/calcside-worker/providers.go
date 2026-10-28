package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"calcside/cmd/calcside-worker/internal/apiclient"
	"calcside/cmd/calcside-worker/internal/config"
	"calcside/cmd/calcside-worker/internal/controlclient"
	"calcside/cmd/calcside-worker/internal/extcache"
	"calcside/internal/node"
	"calcside/internal/node/subproc"
	"calcside/internal/nodeagent"
	"calcside/internal/runtime"
	"calcside/internal/runtime/remote"
)

type workerApp struct {
	Agent  *nodeagent.Agent
	Server *http.Server
	NodeID string
}

func provideNodeID(cfg config.WorkerConfig) (string, error) {
	if cfg.SharedKey == "" {
		return "", fmt.Errorf("--shared-key is required: an unauthenticated worker is an open execution endpoint")
	}
	if cfg.APIAddr == "" {
		return "", fmt.Errorf("--api-addr is required: the worker only reports back over node control")
	}
	id, err := nodeID(cfg)
	if err != nil {
		return "", err
	}
	slog.Info("worker identity", "node_id", id)
	return id, nil
}

func provideNodeConfig(cfg config.WorkerConfig) node.Config {
	ncfg := node.Config{
		Limits:             cfg.ExecLimits(),
		ExtCacheDir:        extArtifactDir(cfg),
		ExtFetchTimeout:    cfg.ExtFetchTimeout,
		MaxConcurrentExecs: cfg.MaxConcurrentExecs,
		ExecMemoryLimit:    cfg.ExecMemoryLimit,
		EvalTimeout:        cfg.PolicyEvalTimeout,
		MaxInstances:       cfg.MaxInstances,
		ReapInterval:       cfg.ReaperInterval,
	}
	ncfg.ExtSourceResolver = extcache.CacheResolver(extArtifactDir(cfg))
	return ncfg
}

// boundedClient clamps every API callback response body so the
// generated parser never sees an unbounded payload.
type boundedClient struct{ inner *http.Client }

func (b *boundedClient) Do(req *http.Request) (*http.Response, error) {
	inner := b.inner
	if inner == nil {
		inner = http.DefaultClient
	}
	resp, err := inner.Do(req)
	if err != nil {
		return nil, err
	}
	resp.Body = http.MaxBytesReader(nil, resp.Body, 2<<20)
	return resp, nil
}

// extArtifactDir is shared by the parent fetch cache and child resolvers.
func extArtifactDir(cfg config.WorkerConfig) string {
	return filepath.Join(cfg.ExtCacheDir, "artifacts")
}

func provideRuntime(cfg config.WorkerConfig, ncfg node.Config, ext *extcache.Cache) (runtime.Runtime, func(), error) {
	sup, err := subproc.New(subproc.Options{
		Child:              subproc.ChildConfig{Node: ncfg},
		MaxInstances:       cfg.MaxInstances,
		MaxConcurrentExecs: cfg.MaxConcurrentExecs,
		ReapInterval:       cfg.ReaperInterval,
		MemoryMax:          cfg.InstanceMemoryMax,
		CgroupParent:       cfg.InstanceCgroupParent,
	})
	if err != nil {
		return nil, nil, err
	}
	sup.StartReaper()
	return extcache.WithRuntime(sup, ext), sup.Close, nil
}

// provideExtCache builds the synchronous extension artifact cache.
// Fetch runs inline on a cache miss, never during worker startup.
func provideExtCache(cfg config.WorkerConfig, nodeID string) (*extcache.Cache, error) {
	fetch := extcache.Fetch(func(ctx context.Context, source, sum string) (map[string][]byte, string, error) {
		if cfg.APIAddr == "" {
			return nil, "", fmt.Errorf("ext: api unavailable")
		}
		client, err := apiclient.NewClientWithResponses(strings.TrimSuffix(cfg.APIAddr, "/"),
			apiclient.WithHTTPClient(&boundedClient{}),
			apiclient.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
				remote.SignRequest([]byte(cfg.SharedKey), nodeID, req, nil, time.Now())
				return nil
			}))
		if err != nil {
			return nil, "", fmt.Errorf("ext: bad api addr: %w", err)
		}
		resp, err := client.ExtTreeWithResponse(ctx, &apiclient.ExtTreeParams{Path: source, Sum: sum})
		if err != nil {
			return nil, "", fmt.Errorf("ext: fetch failed")
		}
		if resp.JSON200 == nil {
			// fixed message only — the API error envelope body is
			// not trusted content for worker logs.
			return nil, "", fmt.Errorf("ext: fetch rejected")
		}
		return resp.JSON200.Files, resp.JSON200.Sum, nil
	})
	return extcache.New(extArtifactDir(cfg), fetch, cfg.ExtFetchTimeout)
}

// provideServingAgent runs the node agent against the API's control
// plane; without --api-addr the worker keeps legacy execution only.
func provideServingAgent(cfg config.WorkerConfig, nodeID string, rt runtime.Runtime) (*nodeagent.Agent, error) {
	ctl, err := controlclient.New(cfg.APIAddr, nodeID, []byte(cfg.SharedKey), nil)
	if err != nil {
		return nil, err
	}
	boot := "boot_" + randHex(16)
	cap := cfg.MaxInstances
	if cap <= 0 || cap > 128 {
		cap = 128
	}
	slog.Info("serving node boot", "node_id", nodeID, "boot_id", boot)
	return nodeagent.New(nodeagent.Options{
		Control: ctl, Runtime: rt, NodeID: nodeID, BootID: boot,
		Capacity: cap,
	})
}

func provideServer(cfg config.WorkerConfig, rt runtime.Runtime) *http.Server {
	// /healthz is operational, not protocol: it lives outside the
	// authenticated handler so orchestrators can probe without a key.
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle("/", remote.NewHandler(rt, []byte(cfg.SharedKey)))

	return &http.Server{
		Addr:        cfg.Addr,
		Handler:     mux,
		ReadTimeout: 30 * time.Second,
		// WriteTimeout must exceed the longest exec, or the server
		// truncates responses mid-flight.
		WriteTimeout: cfg.MaxExecTimeout + 30*time.Second,
	}
}
