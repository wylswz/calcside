package capability

import (
	"context"
	"errors"
	"maps"

	"go.starlark.net/starlark"

	"calcside/internal/stream"
	"calcside/internal/types"
)

// StreamActionArg is the Call.Args key distinguishing stream phases.
const StreamActionArg = "stream_action"

// StreamIDArg is the Call.Args key carrying the wrapped stream's id.
const StreamIDArg = "stream_id"

// WrapStream wraps a stream returned by a capability op so every
// deferred pull and the final close stay inside the gate's armed scope
// (EP-0021). The wrapper shares the originating capability/op: each
// pull runs Before hooks, then the source pull, then After hooks before
// the value is exposed; the close record is emitted once, and native
// source cleanup runs unconditionally outside the gate body so a hook
// denial or cancelled context cannot abandon the resource. args and
// baseMeta are cloned (stream_action/stream_id added per phase); they
// must already be sanitized — never payload bodies or prompt text.
//
// The wrapper registers in the caller's execution scope: if no scope is
// active the source is closed natively and the call fails, so an ext op
// returning a stream at module-init or teardown time cannot leak.
func (g *Gate) WrapStream(thread *starlark.Thread, name types.CapabilityName, op types.Op, args map[string]any, baseMeta map[string]any, source *stream.Stream) (*stream.Stream, error) {
	if source == nil {
		return nil, errors.New("wrap stream: nil source")
	}
	scope := stream.FromThread(thread)
	if scope == nil {
		_ = source.Close()
		return nil, stream.ErrOutOfScope
	}
	orig := maps.Clone(args)
	if orig == nil {
		orig = map[string]any{}
	}
	meta0 := maps.Clone(baseMeta)
	if meta0 == nil {
		meta0 = map[string]any{}
	}
	ctx := ThreadContext(thread)

	// The caller may have reserved the stream id before acquisition so
	// open/pull/close records share one identity; fall back to a fresh
	// allocation otherwise.
	reserved, _ := orig[StreamIDArg].(int64)
	var w *stream.Stream
	phaseArgs := func(action string) map[string]any {
		a := maps.Clone(orig)
		a[StreamActionArg] = action
		a[StreamIDArg] = w.ID()
		return a
	}

	pull := func(t *starlark.Thread) (starlark.Value, bool, error) {
		var pv starlark.Value
		var pok bool
		var perr error
		_, ierr := g.Invoke(ctx, name, op, phaseArgs("pull"),
			func(_ context.Context, _ map[string]any) (starlark.Value, map[string]any, error) {
				pv, pok, perr = source.Pull(t)
				// After hooks see live safe counters and the source's
				// terminal metadata BEFORE the chunk is exposed —
				// delivered items only (EOF does not increment).
				meta := maps.Clone(meta0)
				meta[StreamIDArg] = w.ID()
				meta["pulls"] = source.Pulls()
				meta["bytes"] = source.Bytes()
				meta["chunks"] = source.Pulls()
				meta["stream_state"] = source.State().String()
				meta["stream_status"] = "open"
				if tm := source.TerminalMeta(); tm != nil {
					for k, v := range tm {
						meta[k] = v
					}
				}
				return pv, meta, perr
			})
		if ierr != nil {
			// Deny or read failure: no chunk is exposed for this pull;
			// chunks already delivered cannot be revoked.
			return nil, false, ierr
		}
		if !pok {
			return starlark.None, false, nil
		}
		return pv, true, nil
	}

	closed := false
	closeFn := func() error {
		if closed {
			return nil
		}
		closed = true
		state := w.State()
		status := "closed"
		switch {
		case ctx.Err() != nil:
			status = "cancelled" // caller cancellation outranks Failed
		case state == stream.Exhausted:
			status = "complete"
		case state == stream.Failed:
			status = "error"
		}
		var tm map[string]any
		if t := source.TerminalMeta(); t != nil {
			tm = t
			if t["complete"] == true && ctx.Err() == nil && state != stream.Failed {
				status = "complete" // only a validated terminal event asserts completion
			}
		}
		// Terminal fields ride inside Call.Args too, because the audit
		// Record does not carry Result.Meta: observers must still see
		// status and counters for the once-per-resource close record.
		// Metadata only — never payload.
		cargs := phaseArgs("close")
		cargs["stream_status"] = status
		cargs["pulls"] = w.Pulls()
		cargs["bytes"] = w.Bytes()
		for k, v := range tm {
			cargs[k] = v
		}
		meta := maps.Clone(meta0)
		meta[StreamIDArg] = w.ID()
		meta["stream_state"] = state.String()
		meta["stream_status"] = status
		meta["pulls"] = w.Pulls()
		meta["bytes"] = w.Bytes()
		for k, v := range tm {
			meta[k] = v
		}
		meta["chunks"] = w.Pulls()
		// The terminal record: exactly one per acquired resource. For a
		// failed stream the record's Result.Err carries the original
		// read failure, not a synthetic success.
		var termErr error
		if state == stream.Failed {
			termErr = w.Err()
		}
		_, ierr := g.Invoke(ctx, name, op, cargs,
			func(_ context.Context, _ map[string]any) (starlark.Value, map[string]any, error) {
				return starlark.None, meta, termErr
			})
		// Native cleanup is unconditional — outside the gate body, so a
		// Before denial cannot skip it.
		cerr := source.Close()
		if ierr != nil {
			scope.Fail(ierr)
			return ierr
		}
		return cerr
	}

	wrapped, err := stream.NewWithID(scope, reserved, pull, closeFn, source)
	if err != nil {
		_ = source.Close()
		return nil, err
	}
	w = wrapped
	return w, nil
}
