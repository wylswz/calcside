// Package subproc runs each instance in its own OS process. A
// Supervisor implements runtime.Runtime on the worker: Create spawns a
// child (the current binary re-executed in instance mode), every other
// call is forwarded to that child over a unix socket using the same
// signed worker protocol the API tier speaks, and Delete/reap ends the
// child.
//
// The child is a one-instance execution node: it builds the usual node
// (registry, engine, instance.Manager) and serves it. Starlark globals,
// the VFS, compiled policies and loaded extensions therefore stay in the
// child's memory across execs — REPL semantics are unchanged — while a
// crash, leak or runaway exec takes down only its own instance.
package subproc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"calcside/internal/node"
	"calcside/internal/runtime/remote"
)

// ChildArg is the hidden subcommand that puts a binary into instance
// mode: `<binary> __instance`, with a ChildConfig on stdin.
const ChildArg = "__instance"

// readyLine is what a child prints on stdout once it is serving.
const readyLine = "calcside-instance-ready"

// ChildConfig is everything an instance process needs. It travels as
// JSON on the child's stdin — never argv or env, since it carries keys.
type ChildConfig struct {
	Node node.Config `json:"node"`
	// Socket is the unix socket the child serves on.
	Socket string `json:"socket"`
	// Key authenticates supervisor→child calls; minted per child.
	Key []byte `json:"key"`
	// APIAddr, NodeID and APIKey let the child resolve local ext sources
	// through the API tier, as an in-process worker node would.
	APIAddr string `json:"api_addr,omitempty"`
	NodeID  string `json:"node_id,omitempty"`
	APIKey  []byte `json:"api_key,omitempty"`
}

// RunChild is the instance-process entrypoint. It reads a ChildConfig
// from stdin, serves the runtime protocol on the configured socket, and
// returns once stdin reaches EOF: the supervisor holds the pipe open for
// as long as it wants the instance, so EOF means it is done with us or
// has died — either way the instance must not outlive it.
func RunChild(stdin io.Reader, stdout io.Writer) error {
	// Terminal signals go to the whole process group; lifecycle is the
	// supervisor's call, made through stdin.
	signal.Ignore(os.Interrupt, syscall.SIGTERM)
	// stdout carries the ready handshake; keep gin's debug output off it.
	gin.SetMode(gin.ReleaseMode)

	dec := json.NewDecoder(stdin)
	var cfg ChildConfig
	if err := dec.Decode(&cfg); err != nil {
		return fmt.Errorf("subproc: read config: %w", err)
	}
	nc := cfg.Node
	if cfg.APIAddr != "" {
		nc.ExtLocalResolver = remote.LocalExtResolver(cfg.APIAddr, cfg.NodeID, cfg.APIKey, nc.ExtCacheDir)
	}
	nd := node.Build(nc)
	defer nd.Close()

	ln, err := net.Listen("unix", cfg.Socket)
	if err != nil {
		return fmt.Errorf("subproc: listen: %w", err)
	}
	srv := &http.Server{Handler: remote.NewHandler(nd.Manager, cfg.Key), ReadHeaderTimeout: 10 * time.Second}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	if _, err := fmt.Fprintln(stdout, readyLine); err != nil {
		return err
	}

	eof := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, io.MultiReader(dec.Buffered(), stdin))
		close(eof)
	}()
	select {
	case <-eof:
	case err := <-serveErr:
		return fmt.Errorf("subproc: serve: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(ctx)
}
