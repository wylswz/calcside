// Package stream implements pull-based, single-execution Starlark
// streams (EP-0021): an ordered, one-shot, single-cursor Iterable whose
// items are produced on demand by the owning execution scope. Streams
// are engine-level values, not a capability: creation grants no I/O
// authority, and every operation verifies the owning execution scope
// and thread. The package depends only on Starlark and the standard
// library so capability and engine code can build on it without
// import cycles.
package stream

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"

	"go.starlark.net/starlark"
)

var (
	_ starlark.HasAttrs = (*Stream)(nil)
	_ starlark.Iterable = (*Stream)(nil)
)

// MaxLive bounds the number of simultaneously open streams in one
// execution scope. It is an operator-independent hard cap; creation
// above it is rejected before any native resource is acquired.
const MaxLive = 128

// ErrOutOfScope is returned (and latched as a scope fault) when a
// stream is used outside its owning execution scope: a stale handle
// reached from a later exec, a foreign thread, or a closed scope.
var ErrOutOfScope = errors.New("stream used outside its execution scope")

// ThreadKey is the starlark thread local under which the engine stores
// the active scope so stream builtins can resolve it. Ext module init
// threads and program pure-init never carry this local, so stream
// creation is only possible inside an armed execution.
const ThreadKey = "calcside.stream.scope"

type ctxKey struct{}

// PullFunc produces the next item: (value, true, nil) for an item,
// (None, false, nil) at EOF, or an error. It always runs synchronously
// on the owning Starlark thread.
type PullFunc func(*starlark.Thread) (starlark.Value, bool, error)

// CloseFunc releases the native resource behind a stream (request body,
// buffers). It must be idempotent and must not run Starlark callbacks.
type CloseFunc func() error

// Scope is one execution's stream registry. Every stream registers at
// creation — including never-consumed and derived streams — and
// Scope.Close tears them down deterministically, in reverse
// registration order, while the gate is still armed. A scope is active
// only while its exec's gate is armed (see Activate); creation outside
// an active scope fails.
type Scope struct {
	ctx    context.Context
	thread *starlark.Thread
	// resolve returns the session's currently armed scope. The engine
	// binds it to the session so a stale stream iterated from a later
	// exec faults the CURRENT execution, not its dead owner. Iterable
	// exposes no caller thread, so the captured owner thread alone
	// cannot distinguish a live pull from a stale-exec access; the
	// resolver is the only correct answer. Defaults to self-while-active.
	resolve func() *Scope

	mu      sync.Mutex
	active  bool
	closed  bool
	sticky  error
	nextID  atomic.Int64
	seq     atomic.Int64
	streams map[int64]*Stream
}

// NewScope creates an inactive scope bound to ctx and thread. resolve,
// when nil, defaults to returning the scope itself while it is active.
func NewScope(ctx context.Context, thread *starlark.Thread, resolve func() *Scope) *Scope {
	if ctx == nil {
		ctx = context.Background()
	}
	s := &Scope{
		ctx:     ctx,
		thread:  thread,
		resolve: resolve,
		streams: map[int64]*Stream{},
	}
	return s
}

// Activate marks the scope live. The engine calls this immediately
// after arming the gate; Close deactivates again so scope activity
// tracks the gate's armed lifetime exactly.
func (s *Scope) Activate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.active = true
	}
}

// Active reports whether the scope is inside its armed window.
func (s *Scope) Active() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active && !s.closed
}

// Context returns the scope's context carrying the scope itself, so
// helpers holding only a context can recover it via FromContext.
func (s *Scope) Context() context.Context {
	return context.WithValue(s.ctx, ctxKey{}, s)
}

// FromContext extracts a scope stored via Scope.Context.
func FromContext(ctx context.Context) *Scope {
	if ctx == nil {
		return nil
	}
	if s, ok := ctx.Value(ctxKey{}).(*Scope); ok {
		return s
	}
	return nil
}

// FromThread extracts the scope the engine bound to the thread.
func FromThread(thread *starlark.Thread) *Scope {
	if thread == nil {
		return nil
	}
	if s, ok := thread.Local(ThreadKey).(*Scope); ok {
		return s
	}
	return nil
}

// Current resolves the session's armed scope right now. It is what a
// stale-stream access consults: if it differs from the stream's owner,
// the owner is dead and the fault belongs to the returned scope.
func (s *Scope) Current() *Scope {
	if s.resolve != nil {
		return s.resolve()
	}
	if s.Active() {
		return s
	}
	return nil
}

// Err is the first sticky typed error latched by a stream failure.
func (s *Scope) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sticky
}

// Fail latches the first typed error in the scope and cancels the
// owning thread. Iterator.Next cannot return errors, so stream failures
// reach the engine through this bridge; direct Pull failures latch here
// too so a wrapper callback cannot swallow them.
func (s *Scope) Fail(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	if s.sticky == nil {
		s.sticky = err
	}
	s.mu.Unlock()
	s.thread.Cancel(err.Error())
}

// Reserve allocates a stream id without registering a stream, for
// callers that need a correlation id before acquisition (the gate's
// open phase). Gaps are fine; ids stay monotonically increasing and
// unique within the scope.
func (s *Scope) Reserve() int64 {
	return s.nextID.Add(1)
}

// register adds a stream, enforcing the live cap. Duplicate explicit
// ids of live streams are rejected before any overwrite.
func (s *Scope) register(st *Stream, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active || s.closed {
		return ErrOutOfScope
	}
	if len(s.streams) >= MaxLive {
		return fmt.Errorf("stream: live stream limit %d exceeded", MaxLive)
	}
	if id == 0 {
		id = s.nextID.Add(1)
	} else {
		if _, dup := s.streams[id]; dup {
			return fmt.Errorf("stream: duplicate stream id %d", id)
		}
		// Reserved ids were already bumped; keep monotonicity if a
		// caller passes a raw id.
		for {
			cur := s.nextID.Load()
			if cur >= id || s.nextID.CompareAndSwap(cur, id) {
				break
			}
		}
	}
	st.id = id
	st.seq = s.seq.Add(1)
	s.streams[id] = st
	return nil
}

func (s *Scope) unregister(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.streams, id)
}

// Live returns the count of currently open streams.
func (s *Scope) Live() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.streams)
}

// Close tears down every registered stream in reverse registration
// order, while the gate is still armed, then deactivates. It is
// idempotent and runs only native cleanup — never arbitrary Starlark
// callbacks, which the cancelled thread could not run anyway.
func (s *Scope) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.active = false
	// Teardown order is reverse REGISTRATION order (tracked by seq, not
	// stream id — reserved correlation ids are not registration order).
	// Bookkeeping stays bounded by the live cap.
	ids := make([]int64, 0, len(s.streams))
	byseq := map[int64]int64{}
	for id, st := range s.streams {
		byseq[st.seq] = id
		ids = append(ids, st.seq)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] > ids[j] })
	for i := range ids {
		ids[i] = byseq[ids[i]]
	}
	s.mu.Unlock()

	var first error
	for _, id := range ids {
		s.mu.Lock()
		st := s.streams[id]
		s.mu.Unlock()
		if st == nil {
			continue
		}
		if err := st.Close(); err != nil && first == nil {
			first = err
		}
	}
	// Native teardown failures are latched (after any earlier sticky)
	// so an exec cannot report success while resources failed to close.
	if first != nil {
		s.Fail(first)
	}
	return first
}

// State is the terminal-state machine of a stream.
type State int

const (
	Open State = iota
	Exhausted
	Closed
	Failed
)

func (s State) String() string {
	switch s {
	case Open:
		return "open"
	case Exhausted:
		return "exhausted"
	case Closed:
		return "closed"
	case Failed:
		return "failed"
	}
	return "unknown"
}

// Stream is a pull-based, one-shot iterable resource. It is an opaque
// value: no len/index/hash/JSON, Truth is always true, String never
// pulls or renders payload bytes, and Freeze is a no-op that cannot
// make the resource persistable or shareable.
type Stream struct {
	scope *Scope

	id      int64
	pull    PullFunc
	closeFn CloseFunc
	source  *Stream

	mu        sync.Mutex
	state     State
	err       error
	termMeta  func() map[string]any // optional safe terminal metadata
	metaSnap  map[string]any        // snapshot taken at cleanup
	seq       int64                 // registration order within scope
	iterating bool                  // one active iterator owns the cursor
	owned     bool                  // an attached derived stream owns iteration of this cursor
	pulling   bool                  // reentrancy guard for the pull callback
	cleaned   bool                  // native cleanup ran exactly once
	pulls     int64
	bytes     int64
}

// New registers a pull-based stream in scope. pull runs on the owning
// thread; closeFn is native cleanup, idempotent; source, when non-nil,
// is an upstream stream of the same scope that is closed natively when
// this stream closes (wrappers attach their real source so for/break
// tears down the whole pipeline).
func New(scope *Scope, pull PullFunc, closeFn CloseFunc, source *Stream) (*Stream, error) {
	return NewWithID(scope, 0, pull, closeFn, source)
}

// NewWithID is New with a caller-reserved id (see Scope.Reserve).
func NewWithID(scope *Scope, id int64, pull PullFunc, closeFn CloseFunc, source *Stream) (*Stream, error) {
	if scope == nil || pull == nil {
		return nil, errors.New("stream: no active execution scope")
	}
	if source != nil {
		if source.scope != scope {
			return nil, ErrOutOfScope
		}
		source.mu.Lock()
		// Reject claiming an upstream that is already owned, being
		// iterated, or mid-pull — ownership must not change under
		// active consumption.
		if source.owned || source.iterating || source.pulling {
			source.mu.Unlock()
			return nil, errors.New("stream: source is already claimed or being consumed")
		}
		source.mu.Unlock()
	}
	st := &Stream{scope: scope, pull: pull, closeFn: closeFn, source: source}
	if err := scope.register(st, id); err != nil {
		return nil, err
	}
	// The derived stream owns iteration of the upstream cursor: direct
	// .next() pulls still share it (one cursor, one thread), but an
	// independent for/list/any over the source is rejected. Claim only
	// after successful registration so a failed create cannot poison
	// the source.
	if source != nil {
		source.mu.Lock()
		source.owned = true
		source.mu.Unlock()
	}
	return st, nil
}

// ID is the stream's unique id within its scope.
func (s *Stream) ID() int64 { return s.id }

// Scope exposes the owning scope for gate wrappers.
func (s *Stream) Scope() *Scope { return s.scope }

// State returns the stream's state.
func (s *Stream) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// Err returns the stored failure once the stream is in Failed state.
func (s *Stream) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// Pulls is the count of delivered items.
func (s *Stream) Pulls() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pulls
}

// SetTerminalMeta installs an optional getter for safe terminal
// metadata (status/counters/validated usage). Gate wrappers merge it
// into the once-per-resource close record; it must never carry payload.
func (s *Stream) SetTerminalMeta(f func() map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.termMeta = f
}

// TerminalMeta returns live counters while open, or the snapshot taken
// at cleanup — the producer closure is released with the resource so a
// saved closed handle cannot pin the body reader or redactor state.
func (s *Stream) TerminalMeta() map[string]any {
	s.mu.Lock()
	f, snap := s.termMeta, s.metaSnap
	s.mu.Unlock()
	if snap != nil {
		return snap
	}
	if f == nil {
		return nil
	}
	return f()
}

// Bytes is the count of delivered byte payload (Bytes/String items).
func (s *Stream) Bytes() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bytes
}

// scopeFor validates a direct method call's thread. When the owner is
// dead but the calling thread carries a different live scope (a stale
// handle reached into a later exec), the CURRENT scope takes the fault.
func (s *Stream) scopeFor(t *starlark.Thread) error {
	if t == s.scope.thread && s.scope.Active() {
		return nil
	}
	if cur := FromThread(t); cur != nil && cur != s.scope {
		cur.Fail(ErrOutOfScope)
	}
	return ErrOutOfScope
}

// Pull advances the cursor from Starlark (the .next() builtin path and
// derived-stream callbacks). It verifies owner scope+thread, rejects
// interleaving with an active iterator, and latches failures in the
// scope so a wrapper callback cannot swallow them.
func (s *Stream) Pull(t *starlark.Thread) (starlark.Value, bool, error) {
	if err := s.scopeFor(t); err != nil {
		return nil, false, err
	}
	s.mu.Lock()
	if s.iterating {
		s.mu.Unlock()
		err := errors.New("stream: direct read while an iterator is active")
		s.scope.Fail(err)
		return nil, false, err
	}
	switch s.state {
	case Failed:
		err := s.err
		s.mu.Unlock()
		return nil, false, err
	case Exhausted, Closed:
		s.mu.Unlock()
		return starlark.None, false, nil
	}
	s.mu.Unlock()
	return s.pullOnce(t)
}

// pullOnce runs the pull callback under the reentrancy guard and
// applies EOF/error state transitions. Both the iterator path and the
// direct Pull path funnel here.
func (s *Stream) pullOnce(t *starlark.Thread) (starlark.Value, bool, error) {
	s.mu.Lock()
	if s.pulling {
		s.mu.Unlock()
		err := errors.New("stream: recursive pull")
		s.failWith(err)
		return nil, false, err
	}
	s.pulling = true
	s.mu.Unlock()

	if err := s.scope.ctx.Err(); err != nil {
		s.mu.Lock()
		s.pulling = false
		s.mu.Unlock()
		s.failWith(err)
		return nil, false, err
	}
	v, ok, err := s.pull(t)

	// Re-check state after the callback: it may have closed the stream
	// (or cascaded a failure) through .close() on itself or an
	// upstream. Values produced after the cursor closed are never
	// yielded.
	s.mu.Lock()
	s.pulling = false
	state, stErr := s.state, s.err
	s.mu.Unlock()

	if err != nil {
		s.failWith(err)
		return nil, false, err
	}
	switch state {
	case Failed:
		return nil, false, stErr
	case Closed, Exhausted:
		return starlark.None, false, nil
	}
	if !ok {
		if cerr := s.shutdown(Exhausted); cerr != nil {
			// Native close failure at EOF cannot report success.
			s.failWith(cerr)
			return nil, false, cerr
		}
		return starlark.None, false, nil
	}
	s.mu.Lock()
	s.pulls++
	s.bytes += byteLen(v)
	s.mu.Unlock()
	return v, true, nil
}

// failWith records the stream's terminal failure and latches the
// typed error in the scope BEFORE native cleanup runs — a denied or
// failing close hook must not replace the primary read failure.
func (s *Stream) failWith(err error) {
	s.mu.Lock()
	if s.state != Failed {
		s.state = Failed
		s.err = err
	}
	s.mu.Unlock()
	s.scope.Fail(err)
	_ = s.cleanup()
}

func (s *Stream) cleanup() error {
	s.mu.Lock()
	if s.cleaned {
		s.mu.Unlock()
		return nil
	}
	s.cleaned = true
	closeFn, source, f := s.closeFn, s.source, s.termMeta
	s.pull, s.closeFn, s.source, s.termMeta = nil, nil, nil, nil
	s.mu.Unlock()
	// Snapshot the safe terminal metadata entirely outside the lock: a
	// getter may call back into stream accessors (e.g. a body reader's
	// counters). The assignment re-locks briefly.
	var snapshot map[string]any
	if f != nil {
		snapshot = f()
	}
	if snapshot != nil {
		s.mu.Lock()
		s.metaSnap = snapshot
		s.mu.Unlock()
	}

	var first error
	if closeFn != nil {
		first = closeFn()
	}
	if source != nil {
		if err := source.Close(); err != nil && first == nil {
			first = err
		}
	}
	s.scope.unregister(s.id)
	return first
}

// shutdown marks a terminal state and releases native resources.
func (s *Stream) shutdown(st State) error {
	s.mu.Lock()
	if s.state == Failed || s.state == Closed || s.state == Exhausted {
		s.mu.Unlock()
		return nil
	}
	s.state = st
	s.mu.Unlock()
	return s.cleanup()
}

// Fail marks the stream failed with the ORIGINAL error before native
// cleanup runs, so a later cleanup/close-denial error cannot replace it
// in the scope. Nil is a no-op.
func (s *Stream) Fail(err error) {
	if err != nil {
		s.failWith(err)
	}
}

// Close terminates the stream voluntarily, releasing native resources
// and cascading to the source. Idempotent; native close errors are
// latched in the scope (after any earlier read failure).
func (s *Stream) Close() error {
	if err := s.shutdown(Closed); err != nil {
		s.scope.Fail(err)
		return err
	}
	return nil
}

// ---- starlark.Value ----

func (s *Stream) String() string { return fmt.Sprintf("<stream #%d>", s.ID()) }
func (s *Stream) Type() string   { return "stream" }
func (s *Stream) Truth() starlark.Bool {
	// Never pulls or inspects payload; a stream is always truthy.
	return starlark.True
}

// Freeze is a no-op: it cannot turn the handle into a persistable or
// shareable resource and never consumes the stream.
func (s *Stream) Freeze() {}

func (s *Stream) Hash() (uint32, error) {
	return 0, errors.New("unhashable type: stream")
}

// Attr exposes .next() and .close() only.
func (s *Stream) Attr(name string) (starlark.Value, error) {
	switch name {
	case "next":
		return starlark.NewBuiltin("stream.next", s.builtinNext), nil
	case "close":
		return starlark.NewBuiltin("stream.close", s.builtinClose), nil
	case "map":
		return starMap(s), nil
	case "reduce":
		return starReduce(s), nil
	case "flatmap":
		return starFlatMap(s), nil
	}
	return nil, nil
}

func (s *Stream) AttrNames() []string { return []string{"close", "flatmap", "map", "next", "reduce"} }

func (s *Stream) builtinNext(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(args) != 0 || len(kwargs) != 0 {
		return nil, errors.New("stream.next takes no arguments")
	}
	v, ok, err := s.Pull(thread)
	if err != nil {
		return nil, err
	}
	if !ok {
		return starlark.Tuple{starlark.False, starlark.None}, nil
	}
	return starlark.Tuple{starlark.True, v}, nil
}

func (s *Stream) builtinClose(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(args) != 0 || len(kwargs) != 0 {
		return nil, errors.New("stream.close takes no arguments")
	}
	if err := s.scopeFor(thread); err != nil {
		return nil, err
	}
	return starlark.None, s.Close()
}

// MarshalJSON always fails: streams may never be JSON-encoded at any
// nesting depth. The vendored starlark json encoder checks
// json.Marshaler before Iterable, so this rejects encoding without a
// preflight traversal and without consuming the cursor.
func (s *Stream) MarshalJSON() ([]byte, error) {
	return nil, errors.New("cannot JSON-encode a stream")
}

// Iterate starts the single cursor. One iterator may be active at a
// time; nested or concurrent consumption faults the owning scope.
// Iterator carries no thread handle — see iterator.Next for how stale
// access is attributed via the scope resolver instead.
func (s *Stream) Iterate() starlark.Iterator {
	cur := s.scope.Current()
	if cur != s.scope || !s.scope.Active() {
		if cur != nil && cur != s.scope {
			cur.Fail(ErrOutOfScope)
		}
		return &iterator{s: s, dead: true}
	}
	s.mu.Lock()
	if s.iterating {
		s.mu.Unlock()
		s.scope.Fail(errors.New("stream: nested or concurrent iteration"))
		return &iterator{s: s, dead: true}
	}
	if s.owned {
		s.mu.Unlock()
		s.scope.Fail(errors.New("stream: independent iteration of an owned source"))
		return &iterator{s: s, dead: true}
	}
	if s.state != Open {
		s.mu.Unlock()
		return &iterator{s: s, dead: true}
	}
	s.iterating = true
	s.mu.Unlock()
	return &iterator{s: s}
}

func (s *Stream) Map(fn starlark.Callable) (*Stream, error) {
	if fn == nil {
		return nil, errors.New("stream.map: fn must be callable")
	}
	return New(
		s.scope,
		func(t *starlark.Thread) (starlark.Value, bool, error) {
			nxt, ok, err := s.Pull(t)
			if err != nil || !ok {
				return nxt, ok, err
			}
			result, err := starlark.Call(t, fn, starlark.Tuple{nxt}, nil)
			if err != nil {
				return nil, false, err
			}
			return result, true, nil
		},
		nil,
		s,
	)
}

func (s *Stream) Reduce(fn starlark.Callable, initial starlark.Value) (*Stream, error) {
	if fn == nil {
		return nil, errors.New("stream.reduce: fn must be callable")
	}
	done := false
	return New(
		s.scope,
		func(t *starlark.Thread) (starlark.Value, bool, error) {
			if done {
				return starlark.None, false, nil
			}
			done = true
			acc := initial
			for {
				item, ok, err := s.Pull(t)
				if err != nil {
					return nil, false, err
				}
				if !ok {
					return acc, true, nil
				}
				acc, err = starlark.Call(t, fn, starlark.Tuple{acc, item}, nil)
				if err != nil {
					return nil, false, err
				}
			}
		},
		nil,
		s,
	)
}

func (s *Stream) FlatMap(fn starlark.Callable) (*Stream, error) {
	if fn == nil {
		return nil, errors.New("stream.flatmap: fn must be callable")
	}
	var self, inner *Stream
	flat, err := New(
		s.scope,
		func(t *starlark.Thread) (starlark.Value, bool, error) {
			for {
				if inner != nil {
					v, ok, err := inner.Pull(t)
					if err != nil {
						return nil, false, err
					}
					if ok {
						return v, true, nil
					}
					inner = nil
					continue
				}
				item, ok, err := s.Pull(t)
				if err != nil || !ok {
					return item, ok, err
				}
				res, err := starlark.Call(t, fn, starlark.Tuple{item}, nil)
				if err != nil {
					return nil, false, err
				}
				in, err := s.adoptInner(res)
				if err != nil {
					return nil, false, err
				}
				if in == s || in == self {
					return nil, false, errors.New("stream.flatmap: fn returned the stream being flattened")
				}
				inner = in
			}
		},
		func() error {
			if inner == nil {
				return nil
			}
			err := inner.Close()
			inner = nil
			return err
		},
		s,
	)
	if err != nil {
		return nil, err
	}
	self = flat
	return flat, nil
}

func (s *Stream) adoptInner(v starlark.Value) (*Stream, error) {
	if x, ok := v.(*Stream); ok {
		if x.scope != s.scope {
			return nil, ErrOutOfScope
		}
		x.mu.Lock()
		defer x.mu.Unlock()
		if x.owned || x.iterating || x.pulling {
			return nil, errors.New("stream.flatmap: inner stream is already claimed or being consumed")
		}
		x.owned = true
		return x, nil
	}
	switch v.(type) {
	case *starlark.List, starlark.Tuple:
		return NewList(s.scope, v)
	}
	return nil, errors.New("stream.flatmap: fn must return a stream, list, or tuple")
}

type iterator struct {
	s        *Stream
	dead     bool
	done     bool
	released bool // Done ran; iterating flag already cleared
}

func (it *iterator) Next(p *starlark.Value) bool {
	*p = starlark.None
	if it.done || it.dead {
		it.done = true
		return false
	}
	s := it.s
	// Iterable.Next has no thread parameter: the only safe attribution
	// for a stale stream is the session's CURRENT scope via the owner
	// scope's resolver — never the captured owner thread, which cannot
	// tell a live same-thread pull from a stale-exec access.
	cur := s.scope.Current()
	if cur != s.scope || !s.scope.Active() {
		it.done = true
		if cur != nil && cur != s.scope {
			cur.Fail(ErrOutOfScope)
		}
		return false
	}
	s.mu.Lock()
	st := s.state
	s.mu.Unlock()
	switch st {
	case Failed:
		it.done = true
		if err := s.Err(); err != nil {
			s.scope.Fail(err)
		}
		return false
	case Exhausted, Closed:
		it.done = true
		return false
	}
	v, ok, err := s.pullOnce(s.scope.thread)
	if err != nil || !ok {
		it.done = true
		return false
	}
	*p = v
	return true
}

// Done ends iteration: break/return/unwind discard the remainder and
// the stream can resume on the shared cursor; Done only releases it.
// no-op.
func (it *iterator) Done() {
	if it.released {
		return
	}
	it.released = true
	it.done = true
	if it.dead {
		return
	}
	s := it.s
	s.mu.Lock()
	if s.iterating {
		s.iterating = false
	}
	s.mu.Unlock()
	// Done releases the cursor only — it is NOT an early close: the
	// stream stays open, a later .next or for may resume the same
	// cursor, and unclosed streams are reclaimed by exec teardown.
}

// NewCallback builds the stream.new callback path: next is a Starlark
// callable invoked synchronously on the owner thread per pull and must
// return exactly (has_value, value). Malformed returns are errors.
// There is deliberately no source/upstream lifecycle binding — callers
// control resource lifetime with explicit .close; exec teardown is the
// safety net.
func NewCallback(scope *Scope, next starlark.Value) (*Stream, error) {
	if _, ok := next.(starlark.Callable); !ok {
		return nil, errors.New("stream.new: next_fn must be callable")
	}
	pull := func(t *starlark.Thread) (starlark.Value, bool, error) {
		v, err := starlark.Call(t, next, nil, nil)
		if err != nil {
			return nil, false, err
		}
		tup, ok := v.(starlark.Tuple)
		if !ok || len(tup) != 2 {
			return nil, false, errors.New("stream.new: next_fn must return (has_value, value)")
		}
		has, ok := tup[0].(starlark.Bool)
		if !ok {
			return nil, false, errors.New("stream.new: next_fn must return (has_value, value)")
		}
		if !bool(has) {
			return starlark.None, false, nil
		}
		return tup[1], true, nil
	}
	return New(scope, pull, nil, nil)
}

func NewList(scope *Scope, values starlark.Value) (*Stream, error) {
	var iterable starlark.Iterable
	switch v := values.(type) {
	case *starlark.List:
		iterable = v
	case starlark.Tuple:
		iterable = v
	default:
		return nil, errors.New("stream: values must be a list or tuple")
	}
	it := iterable.Iterate()
	st, err := New(scope, func(_ *starlark.Thread) (starlark.Value, bool, error) {
		var v starlark.Value
		if it.Next(&v) {
			return v, true, nil
		}
		return starlark.None, false, nil
	}, func() error {
		it.Done()
		return nil
	}, nil)
	if err != nil {
		it.Done()
		return nil, err
	}
	return st, nil
}

func byteLen(v starlark.Value) int64 {
	switch t := v.(type) {
	case starlark.Bytes:
		return int64(len(t))
	case starlark.String:
		return int64(len(t))
	}
	return 0
}
