// Package runtime defines the contract between the API tier and the
// execution tier.
//
// The execution tier never reads the database. Every request therefore
// carries the complete context an instance needs — its spec, its policy
// snapshot, its resolved secrets — and every response carries back the
// state and the records the API tier is responsible for persisting.
//
// Nothing here may reference the store, the Starlark engine, or any
// transport: this package is the serializable middle both tiers agree
// on, and it is the only thing a remote transport has to marshal.
package runtime

import (
	"context"
	"errors"
	"fmt"
)

// Sentinel errors the execution tier reports. A remote transport must
// map these across the wire so callers keep switching on errors.Is
// rather than on strings.
var (
	ErrNotFound      = errors.New("runtime: instance not found")
	ErrNotRunning    = errors.New("runtime: instance not running")
	ErrTooMany       = errors.New("runtime: instance limit reached")
	ErrBadCapability = errors.New("runtime: unknown capability")
	ErrNoCapability  = errors.New("runtime: capability not granted")
	ErrNotOwner      = errors.New("runtime: instance belongs to another user")
	ErrBadSpec       = errors.New("runtime: invalid spec")
	// ErrStaleEpoch means the request's fencing epoch no longer matches
	// the binding the node holds — the caller is working from a stale
	// placement claim and must not be allowed to act.
	ErrStaleEpoch = errors.New("runtime: stale placement epoch")
	// ErrNoSuchPath is a browse against a path the instance's VFS does
	// not have. It is a distinct sentinel so the API tier never has to
	// match on filesystem error strings across the wire.
	ErrNoSuchPath = errors.New("runtime: no such path")
	// ErrFS is any other filesystem failure during a browse.
	ErrFS = errors.New("runtime: filesystem error")
)

// Error pairs a stable kind with a message that is safe to surface to
// the API caller.
//
// The two are deliberately separate. The kind is what the API tier
// routes on (and what a transport must carry so errors.Is keeps working
// across a process boundary); the message is what the user sees, and it
// must not accumulate wrapper prefixes on the way out.
type Error struct {
	Kind error
	Msg  string
}

func (e *Error) Error() string { return e.Msg }
func (e *Error) Unwrap() error { return e.Kind }

// Errf builds an Error of the given kind.
func Errf(kind error, format string, args ...any) *Error {
	return &Error{Kind: kind, Msg: fmt.Sprintf(format, args...)}
}

// Runtime is the execution tier as seen by the API tier. Every hop on
// the execution path implements it, so each hop is a router in front of
// the next (see docs/execution-routing.md):
//
//	API tier          remote.Client         resolves the owning worker
//	worker            subproc.Supervisor    one OS process per instance
//	instance process  *instance.Manager     in-memory state, runs Starlark
//
// remote.Direct carries the worker→instance-process hop; the single
// binary and tests use *instance.Manager directly.
//
// Retry semantics differ per method and are part of the contract:
// Create, Exec and Delete have side effects and must never be retried
// transparently — an Exec may have injected secrets into outbound
// requests before failing. Keepalive, Browse, Prompt and Inspect are
// read-only with respect to script state and may be retried.
type Runtime interface {
	Create(ctx context.Context, req *CreateRequest) (*CreateResponse, error)
	Exec(ctx context.Context, req *ExecRequest) (*ExecResponse, error)
	Keepalive(ctx context.Context, req *KeepaliveRequest) (*KeepaliveResponse, error)
	Delete(ctx context.Context, req *DeleteRequest) (*DeleteResponse, error)
	Browse(ctx context.Context, req *BrowseRequest) (*BrowseResponse, error)
	Prompt(ctx context.Context, req *PromptRequest) (*PromptResponse, error)
	Inspect(ctx context.Context, req *InspectRequest) (*InspectResponse, error)
}
