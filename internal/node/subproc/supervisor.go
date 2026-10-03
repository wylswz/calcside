package subproc

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"calcside/internal/runtime"
	"calcside/internal/runtime/remote"
	"calcside/internal/types"
)

var _ runtime.Runtime = (*Supervisor)(nil)

// Options configure a Supervisor.
type Options struct {
	// Path and Args launch a child in instance mode. Path defaults to
	// the running executable and Args to [ChildArg].
	Path string
	Args []string
	// Env is appended to the supervisor's environment for each child.
	Env []string
	// Child is the template config; Socket, Key and Node.MaxInstances
	// are filled per child.
	Child ChildConfig
	// MaxInstances caps live instance processes; 0 is unlimited.
	MaxInstances int
	// MaxConcurrentExecs bounds execs across all children; 0 means 64.
	MaxConcurrentExecs int
	// StartTimeout bounds how long a child may take to start serving.
	StartTimeout time.Duration
	// HardExecTimeout is how long an exec call may take before the
	// supervisor kills the child outright — the child enforces the real
	// exec timeout, this only catches a child that stopped responding.
	// Defaults to Child.Node.Limits.MaxExecTimeout + 30s.
	HardExecTimeout time.Duration
	// ReapInterval and ReapGrace mirror instance.Options: instances the
	// API tier stopped renewing are killed after their deadline + grace.
	ReapInterval time.Duration
	ReapGrace    time.Duration
	Now          func() time.Time
}

// Supervisor is the worker-side runtime.Runtime that gives every
// instance its own process. It holds no Starlark state of its own; it
// only routes by instance id. Owner and epoch checks happen in the
// child, which re-checks them exactly as an in-process node would.
type Supervisor struct {
	o   Options
	sem chan struct{}

	mu       sync.Mutex
	procs    map[string]*proc
	starting map[string]bool

	stop     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

// proc is one live instance process.
type proc struct {
	id    string
	cmd   *exec.Cmd
	stdin io.WriteCloser
	rt    runtime.Runtime
	done  chan struct{} // closed once the process has exited

	mu        sync.Mutex
	expiresAt time.Time
}

const stopGrace = 5 * time.Second

// errExited is returned for an instance whose process is gone: its
// state went with it, so to the API tier it no longer exists.
func errExited(id string) error {
	return runtime.Errf(runtime.ErrNotFound, "instance %s: process exited", id)
}

// New builds a supervisor.
func New(o Options) (*Supervisor, error) {
	if o.Path == "" {
		exe, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("subproc: resolve executable: %w", err)
		}
		o.Path = exe
	}
	if o.Args == nil {
		o.Args = []string{ChildArg}
	}
	if o.MaxConcurrentExecs <= 0 {
		o.MaxConcurrentExecs = 64
	}
	if o.StartTimeout <= 0 {
		o.StartTimeout = 10 * time.Second
	}
	if o.HardExecTimeout <= 0 && o.Child.Node.Limits.MaxExecTimeout > 0 {
		o.HardExecTimeout = o.Child.Node.Limits.MaxExecTimeout + 30*time.Second
	}
	if o.ReapInterval <= 0 {
		o.ReapInterval = 10 * time.Second
	}
	if o.ReapGrace <= 0 {
		o.ReapGrace = 5 * time.Minute
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	// A child hosts exactly one instance and never reaps on its own:
	// lifecycle belongs to the supervisor.
	o.Child.Node.MaxInstances = 1
	return &Supervisor{
		o:        o,
		sem:      make(chan struct{}, o.MaxConcurrentExecs),
		procs:    map[string]*proc{},
		starting: map[string]bool{},
		stop:     make(chan struct{}),
	}, nil
}

// StartReaper launches the background reclaim sweep.
func (s *Supervisor) StartReaper() {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		t := time.NewTicker(s.o.ReapInterval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				s.Reap()
			case <-s.stop:
				return
			}
		}
	}()
}

// Close stops the reaper and ends every child.
func (s *Supervisor) Close() {
	s.stopOnce.Do(func() { close(s.stop) })
	s.wg.Wait()
	s.mu.Lock()
	all := make([]*proc, 0, len(s.procs))
	for _, p := range s.procs {
		all = append(all, p)
	}
	s.mu.Unlock()
	var wg sync.WaitGroup
	for _, p := range all {
		wg.Add(1)
		go func() { defer wg.Done(); s.terminate(p) }()
	}
	wg.Wait()
}

// Reap ends instances whose deadline passed more than ReapGrace ago.
func (s *Supervisor) Reap() {
	cutoff := s.o.Now().Add(-s.o.ReapGrace)
	var doomed []*proc
	s.mu.Lock()
	for _, p := range s.procs {
		p.mu.Lock()
		exp := p.expiresAt
		p.mu.Unlock()
		if !exp.IsZero() && !exp.After(cutoff) {
			doomed = append(doomed, p)
		}
	}
	s.mu.Unlock()
	for _, p := range doomed {
		s.terminate(p)
	}
}

// Count reports live instance processes (tests/metrics).
func (s *Supervisor) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.procs)
}

func (s *Supervisor) Create(ctx context.Context, req *runtime.CreateRequest) (*runtime.CreateResponse, error) {
	s.mu.Lock()
	if _, ok := s.procs[req.InstanceID]; ok || s.starting[req.InstanceID] {
		s.mu.Unlock()
		return nil, runtime.Errf(runtime.ErrBadSpec, "instance %s already exists", req.InstanceID)
	}
	if s.o.MaxInstances > 0 && len(s.procs)+len(s.starting) >= s.o.MaxInstances {
		s.mu.Unlock()
		return nil, runtime.ErrTooMany
	}
	s.starting[req.InstanceID] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.starting, req.InstanceID)
		s.mu.Unlock()
	}()

	p, err := s.spawn(ctx, req.InstanceID)
	if err != nil {
		return nil, err
	}
	resp, err := p.rt.Create(ctx, req)
	if err != nil {
		s.terminate(p)
		return nil, err
	}
	p.renew(req.ExpiresAt)
	s.mu.Lock()
	s.procs[req.InstanceID] = p
	s.mu.Unlock()
	// The child may have died between Create and registration.
	if p.exited() {
		s.forget(p)
		return nil, errExited(req.InstanceID)
	}
	return resp, nil
}

// spawn starts a child and waits until it serves.
func (s *Supervisor) spawn(ctx context.Context, id string) (*proc, error) {
	dir, err := os.MkdirTemp("", "cs-inst-")
	if err != nil {
		return nil, fmt.Errorf("subproc: socket dir: %w", err)
	}
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	cfg := s.o.Child
	cfg.Socket = filepath.Join(dir, "s")
	cfg.Key = key

	cmd := exec.Command(s.o.Path, s.o.Args...)
	cmd.Env = append(os.Environ(), s.o.Env...)
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("subproc: start instance process: %w", err)
	}

	sock := cfg.Socket
	hc := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
	}}
	rt, err := remote.NewDirect("http://instance", hc, key, "supervisor")
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = os.RemoveAll(dir)
		return nil, err
	}
	p := &proc{id: id, cmd: cmd, stdin: stdin, rt: rt, done: make(chan struct{})}

	ready := make(chan struct{})
	go func() {
		// Wait must follow the last read of stdout, so one goroutine
		// owns both: handshake, then forward stray output to stderr.
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if sc.Text() == readyLine {
				close(ready)
				break
			}
			fmt.Fprintln(os.Stderr, sc.Text())
		}
		_, _ = io.Copy(os.Stderr, stdout)
		err := cmd.Wait()
		// Unroute before signalling done, so anyone woken by done
		// already sees the instance gone.
		if s.forget(p) {
			slog.Warn("instance process exited unexpectedly", "instance_id", id, "err", err)
		}
		_ = os.RemoveAll(dir)
		close(p.done)
	}()

	if err := json.NewEncoder(stdin).Encode(&cfg); err != nil {
		s.kill(p)
		return nil, fmt.Errorf("subproc: send config: %w", err)
	}
	t := time.NewTimer(s.o.StartTimeout)
	defer t.Stop()
	select {
	case <-ready:
		return p, nil
	case <-p.done:
		return nil, fmt.Errorf("subproc: instance process exited during startup")
	case <-t.C:
		s.kill(p)
		return nil, fmt.Errorf("subproc: instance process not ready after %s", s.o.StartTimeout)
	case <-ctx.Done():
		s.kill(p)
		return nil, ctx.Err()
	}
}

// forget drops p from the routing table; it reports whether p was
// still registered.
func (s *Supervisor) forget(p *proc) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.procs[p.id] != p {
		return false
	}
	delete(s.procs, p.id)
	return true
}

// terminate ends p gracefully: closing stdin tells the child to shut
// down; a child that does not exit within stopGrace is killed.
func (s *Supervisor) terminate(p *proc) {
	s.forget(p)
	_ = p.stdin.Close()
	select {
	case <-p.done:
	case <-time.After(stopGrace):
		s.kill(p)
	}
}

func (s *Supervisor) kill(p *proc) {
	s.forget(p)
	_ = p.cmd.Process.Kill()
	<-p.done
}

func (p *proc) exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

func (p *proc) renew(deadline time.Time) {
	if deadline.IsZero() {
		return
	}
	p.mu.Lock()
	p.expiresAt = deadline
	p.mu.Unlock()
}

func (s *Supervisor) get(id string) (*proc, error) {
	s.mu.Lock()
	p, ok := s.procs[id]
	s.mu.Unlock()
	if !ok {
		return nil, runtime.ErrNotFound
	}
	return p, nil
}

// call forwards one request to the instance's child. A transport
// failure from a child that has since exited is reported as the
// instance being gone rather than as an opaque connection error.
func call[Resp any](s *Supervisor, id string, fn func(p *proc) (*Resp, error)) (*Resp, error) {
	p, err := s.get(id)
	if err != nil {
		return nil, err
	}
	resp, err := fn(p)
	if err == nil {
		return resp, nil
	}
	var rerr *runtime.Error
	if !errors.As(err, &rerr) {
		select {
		case <-p.done:
			return nil, errExited(id)
		case <-time.After(200 * time.Millisecond):
		}
	}
	return nil, err
}

func (s *Supervisor) Exec(ctx context.Context, req *runtime.ExecRequest) (*runtime.ExecResponse, error) {
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		return &runtime.ExecResponse{Result: runtime.ExecResult{
			ExecID: req.ExecID,
			Error:  &runtime.ExecError{Type: types.ErrTimeout, Message: "waiting for exec slot: " + ctx.Err().Error()},
		}}, nil
	}
	return call(s, req.InstanceID, func(p *proc) (*runtime.ExecResponse, error) {
		hctx, cancel := ctx, context.CancelFunc(func() {})
		if s.o.HardExecTimeout > 0 {
			hctx, cancel = context.WithTimeout(ctx, s.o.HardExecTimeout)
		}
		defer cancel()
		resp, err := p.rt.Exec(hctx, req)
		if err != nil && ctx.Err() == nil && errors.Is(hctx.Err(), context.DeadlineExceeded) {
			slog.Warn("instance process unresponsive; killing", "instance_id", req.InstanceID, "exec_id", req.ExecID)
			s.kill(p)
			return nil, errExited(req.InstanceID)
		}
		if err == nil {
			p.renew(req.RenewedExpiresAt)
		}
		return resp, err
	})
}

func (s *Supervisor) Keepalive(ctx context.Context, req *runtime.KeepaliveRequest) (*runtime.KeepaliveResponse, error) {
	return call(s, req.InstanceID, func(p *proc) (*runtime.KeepaliveResponse, error) {
		resp, err := p.rt.Keepalive(ctx, req)
		if err == nil {
			p.renew(req.RenewedExpiresAt)
		}
		return resp, err
	})
}

func (s *Supervisor) Delete(ctx context.Context, req *runtime.DeleteRequest) (*runtime.DeleteResponse, error) {
	return call(s, req.InstanceID, func(p *proc) (*runtime.DeleteResponse, error) {
		resp, err := p.rt.Delete(ctx, req)
		// Owner/epoch rejections leave the instance alive; anything the
		// child accepted (or no longer knows) ends the process.
		if err == nil || errors.Is(err, runtime.ErrNotFound) {
			s.terminate(p)
		}
		return resp, err
	})
}

func (s *Supervisor) Browse(ctx context.Context, req *runtime.BrowseRequest) (*runtime.BrowseResponse, error) {
	return call(s, req.InstanceID, func(p *proc) (*runtime.BrowseResponse, error) { return p.rt.Browse(ctx, req) })
}

func (s *Supervisor) Prompt(ctx context.Context, req *runtime.PromptRequest) (*runtime.PromptResponse, error) {
	return call(s, req.InstanceID, func(p *proc) (*runtime.PromptResponse, error) { return p.rt.Prompt(ctx, req) })
}

func (s *Supervisor) Inspect(ctx context.Context, req *runtime.InspectRequest) (*runtime.InspectResponse, error) {
	return call(s, req.InstanceID, func(p *proc) (*runtime.InspectResponse, error) { return p.rt.Inspect(ctx, req) })
}
