// Package node assembles a live execution node — capability registry,
// engine, and instance manager — the pieces both `calcside serve` (for
// its in-process runtime) and `calcside-worker` need, identical.
package node

import (
	"time"

	"calcside/internal/capability"
	capext "calcside/internal/capability/ext"
	capfs "calcside/internal/capability/fs"
	capio "calcside/internal/capability/io"
	capnet "calcside/internal/capability/net"
	"calcside/internal/engine"
	"calcside/internal/instance"
)

// Config mirrors the execution-relevant part of the API/worker flags.
// The node must see the same limits and extension policy on every
// process, or identical specs would behave differently by placement.
type Config struct {
	Limits          capability.ServerLimits
	ExtAllowSources []string
	ExtLocalRoots   []string
	// ExtLocalResolver replaces filesystem local-source resolution when
	// set — a remote worker resolves local identifiers through the API.
	// It is not serializable; an instance process rebuilds it from its
	// own config (see cmd/calcside-worker).
	ExtLocalResolver   capext.LocalResolver `json:"-"`
	ExtCacheDir        string
	ExtFetchTimeout    time.Duration
	MaxConcurrentExecs int
	ExecMemoryLimit    uint64
	EvalTimeout        time.Duration
	MaxInstances       int
	ReapInterval       time.Duration
	ReapGrace          time.Duration
}

// Node is a running execution node.
type Node struct {
	Registry *capability.Registry
	Engine   *engine.Engine
	Manager  *instance.Manager
}

func newRegistry(c Config) *capability.Registry {
	reg := capability.NewRegistry()
	reg.Register(capfs.Factory())
	reg.Register(capnet.Factory())
	reg.Register(capio.Factory())
	reg.Register(capext.Factory(capext.Options{
		AllowSources:  c.ExtAllowSources,
		LocalRoots:    c.ExtLocalRoots,
		LocalResolver: c.ExtLocalResolver,
		CacheDir:      c.ExtCacheDir,
		FetchTimeout:  c.ExtFetchTimeout,
	}))
	return reg
}

func newEngine(c Config) *engine.Engine {
	return engine.New(c.MaxConcurrentExecs)
}

func managerOptions(c Config, reg *capability.Registry, eng *engine.Engine) instance.Options {
	return instance.Options{
		Engine:       eng,
		Registry:     reg,
		Limits:       c.Limits,
		EvalTimeout:  c.EvalTimeout,
		MaxInstances: c.MaxInstances,
		ReapInterval: c.ReapInterval,
		ReapGrace:    c.ReapGrace,
	}
}

func (n *Node) Close() {
	n.Manager.StopReaper()
	n.Engine.Close()
}
