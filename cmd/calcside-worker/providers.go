package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"calcside/cmd/calcside-worker/internal/config"
	common "calcside/internal/config"
	"calcside/internal/node"
	"calcside/internal/node/subproc"
	"calcside/internal/runtime"
	"calcside/internal/runtime/remote"
)

type workerApp struct {
	Server *http.Server
	NodeID string
}

func provideNodeID(cfg config.WorkerConfig) (string, error) {
	if cfg.SharedKey == "" {
		return "", fmt.Errorf("--shared-key is required: an unauthenticated worker is an open execution endpoint")
	}
	id, err := nodeID(cfg)
	if err != nil {
		return "", err
	}
	slog.Info("worker identity", "node_id", id, "boot_id", randHex(8))
	return id, nil
}

func provideNodeConfig(cfg config.WorkerConfig, nodeID string) node.Config {
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
		ncfg.ExtLocalResolver = LocalExtResolver(cfg.APIAddr, nodeID, []byte(cfg.SharedKey), cfg.ExtCacheDir)
	}
	return ncfg
}

func provideRuntime(cfg config.WorkerConfig, nodeID string, ncfg node.Config) (runtime.Runtime, func(), error) {
	slog.Info("instance isolation", "mode", cfg.InstanceIsolation)
	if cfg.InstanceIsolation == common.IsolationProcess {
		extra, err := json.Marshal(childExtensions{APIAddr: cfg.APIAddr, NodeID: nodeID, APIKey: []byte(cfg.SharedKey)})
		if err != nil {
			return nil, nil, err
		}
		sup, err := subproc.New(subproc.Options{
			Child: subproc.ChildConfig{
				Node:  ncfg,
				Extra: extra,
			},
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
		return sup, sup.Close, nil
	}
	nd := node.Build(ncfg)
	nd.Manager.StartReaper()
	return nd.Manager, nd.Close, nil
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
