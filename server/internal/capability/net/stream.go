package net

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"calcside/internal/capability"
	"calcside/internal/secrets"
	"calcside/internal/stream"
	"calcside/internal/types"
)

// OpStream is the streaming HTTP op.
const OpStream types.Op = "stream"

const (
	streamReadChunk = 32 << 10 // max raw bytes read per pull
	streamOutChunk  = 64 << 10 // max redacted bytes yielded per pull
)

// openRequest performs the eager half of an HTTP call: secret expansion,
// authorization, request construction and hc.Do. The returned response
// body is live; the caller owns closing it. The injected secret names
// ride the request context for the redirect checker.
func (c *client) openRequest(ctx context.Context, method, rawURL, body string, headers map[string]string, contentType string) (*http.Response, error) {
	expURL, expHeaders, expBody, injected, err := c.expand(rawURL, headers, body)
	if err != nil {
		return nil, err
	}
	if _, _, err := c.authorize(method, expURL); err != nil {
		return nil, err
	}
	if len(injected) > 0 {
		ctx = context.WithValue(ctx, injectedKey, injected)
	}
	var rdr io.Reader
	if expBody != "" {
		rdr = strings.NewReader(expBody)
	}
	req, err := http.NewRequestWithContext(ctx, method, expURL, rdr)
	if err != nil {
		return nil, fmt.Errorf("net: %w", err)
	}
	for k, v := range expHeaders {
		req.Header.Set(k, v)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return c.hc.Do(req)
}

// streamMethod builds the thread-aware net.stream builtin. It cannot go
// through capability.Bind because the binding needs the calling thread
// to resolve the execution scope for deferred pulls.
func (c *client) streamMethod(gate *capability.Gate) starlark.Value {
	return starlark.NewBuiltin("net.stream",
		func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			var method, rawURL, body string
			var hdrs starlark.Value
			if err := starlark.UnpackArgs("net.stream", args, kwargs, "method", &method, "url", &rawURL, "body?", &body, "headers?", &hdrs); err != nil {
				return nil, fmt.Errorf("net.stream: invalid arguments")
			}
			if _, err := types.ParseHTTPMethod(method); err != nil {
				return nil, fmt.Errorf("net: %w", err)
			}
			method = strings.ToUpper(method)
			headers, err := unpackStringDict(hdrs)
			if err != nil {
				return nil, err
			}
			host, port, err := extractHostPort(rawURL)
			if err != nil {
				return nil, err
			}
			// The execution scope must be active BEFORE any network
			// resource is acquired; the body registers there so teardown
			// always reaches it.
			scope := stream.FromThread(thread)
			if scope == nil || !scope.Active() {
				return nil, stream.ErrOutOfScope
			}
			cargs := netCallArgs(method, rawURL, host, port)
			cargs["secrets"] = secretRefs(rawURL, headers, body)
			cargs[capability.StreamActionArg] = "open"
			cargs[capability.StreamIDArg] = scope.Reserve()
			// Every phase record carries the declared stream arg set
			// (schema completeness); terminal values replace these.
			cargs["stream_status"] = ""
			cargs["pulls"] = int64(0)
			cargs["bytes"] = int64(0)
			cargs["raw_bytes"] = int64(0)
			cargs["status"] = int64(0)
			execCtx := capability.ThreadContext(thread)

			// TerminalMeta reports the response status once installed
			// and the cumulative raw bytes read — safe counters only.
			meta0 := map[string]any{"status": int64(0),
				"secrets_injected": len(secretRefs(rawURL, headers, body)),
				"stream_id":        cargs[capability.StreamIDArg], "stream_state": "",
				"stream_status": "", "pulls": int64(0), "bytes": int64(0),
				"raw_bytes": int64(0), "chunks": int64(0)}
			var wrapped *stream.Stream
			v, ierr := gate.Invoke(execCtx, types.CapNet, OpStream, cargs,
				func(ctx context.Context, _ map[string]any) (starlark.Value, map[string]any, error) {
					// The request context lives for the whole stream:
					// body close cancels it; the client timeout covers
					// open plus every pull with no idle reset.
					reqCtx, cancel := context.WithCancel(ctx)
					// Registry and gated wrapper are allocated BEFORE
					// the network request: a full scope rejects the
					// stream without a billable request, and teardown
					// can always reach the resource.
					src, r, err := c.newBodyStream(scope, reqCtx, cancel)
					if err != nil {
						cancel()
						return nil, nil, sanitizeErr(c, err)
					}
					w, err := gate.WrapStream(thread, types.CapNet, OpStream, cargs, meta0, src)
					if err != nil {
						cancel()
						return nil, nil, sanitizeErr(c, err)
					}
					wrapped = w
					resp, err := c.openRequest(reqCtx, method, rawURL, body, headers, "")
					if err != nil {
						serr := sanitizeErr(c, err)
						w.Fail(serr) // latch the original open failure before cleanup
						_ = w.Close()
						return nil, nil, serr
					}
					r.SetBody(resp)
					meta := map[string]any{}
					for k, v := range meta0 {
						meta[k] = v
					}
					meta["status"] = resp.StatusCode
					meta["stream_state"] = "open"
					d := starlark.NewDict(3)
					_ = d.SetKey(starlark.String("status"), starlark.MakeInt(resp.StatusCode))
					hd := starlark.NewDict(len(resp.Header))
					for k := range resp.Header {
						_ = hd.SetKey(starlark.String(k), starlark.String(c.redact(resp.Header.Get(k))))
					}
					_ = d.SetKey(starlark.String("headers"), hd)
					_ = d.SetKey(starlark.String("body"), w)
					return d, meta, nil
				})
			if ierr != nil && wrapped != nil {
				// Opening After denial (or any open failure): latch the
				// primary error before native cleanup.
				wrapped.Fail(ierr)
				_ = wrapped.Close()
			}
			return v, ierr
		})
}

// newBodyStream registers the native read cursor placeholder BEFORE the
// response exists; SetBody installs it after the eager open. Each pull
// reads at most streamReadChunk raw bytes, feeds the incremental
// redactor, and yields at most streamOutChunk redacted bytes. Raw bytes
// — including framing and discarded bytes — count against
// max_response_bytes cumulatively for the whole response.
func (c *client) newBodyStream(scope *stream.Scope, ctx context.Context, cancel context.CancelFunc) (*stream.Stream, *bodyReader, error) {
	capBytes := c.cfg.MaxResponseBytes
	if capBytes <= 0 {
		capBytes = defaultMaxResponse
	}
	r := &bodyReader{
		red:    c.secrets.NewStreamRedactor(capBytes),
		rawCap: capBytes,
		buf:    make([]byte, streamReadChunk),
	}
	pull := func(t *starlark.Thread) (starlark.Value, bool, error) {
		v, ok, err := r.next(ctx)
		if err != nil {
			return nil, false, sanitizeErr(c, err)
		}
		return v, ok, nil
	}
	closeFn := func() error {
		cancel()
		if r.resp == nil {
			return nil
		}
		return r.resp.Body.Close()
	}
	st, err := stream.New(scope, pull, closeFn, nil)
	if err != nil {
		return nil, nil, err
	}
	st.SetTerminalMeta(func() map[string]any {
		m := map[string]any{"raw_bytes": r.raw}
		if r.resp != nil {
			m["status"] = r.resp.StatusCode
		}
		return m
	})
	return st, r, nil
}

// SetBody installs the opened response; pulls before installation are
// unreachable (the wrapper is only returned after open succeeds).
func (r *bodyReader) SetBody(resp *http.Response) { r.resp = resp }

type bodyReader struct {
	resp   *http.Response
	red    *secrets.StreamRedactor
	rawCap int64
	raw    int64
	buf    []byte
	outQ   [][]byte // pending redacted output chunks, each <= streamOutChunk
	done   bool
}

// next returns the next bounded redacted chunk.
func (r *bodyReader) next(ctx context.Context) (starlark.Value, bool, error) {
	for len(r.outQ) == 0 {
		if r.done {
			return starlark.None, false, nil
		}
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		n, err := r.resp.Body.Read(r.buf)
		if n > 0 {
			r.raw += int64(n)
			if r.raw > r.rawCap {
				return nil, false, fmt.Errorf("net: response body exceeds %d bytes", r.rawCap)
			}
		}
		if err != nil && err != io.EOF {
			// A read error after partial data must fail before any
			// unsafe pending bytes are exposed.
			return nil, false, fmt.Errorf("net: reading response: %w", err)
		}
		eof := err == io.EOF
		out, rerr := r.red.PushContext(ctx, r.buf[:n], eof)
		if rerr != nil {
			return nil, false, rerr
		}
		for len(out) > 0 {
			m := min(len(out), streamOutChunk)
			r.outQ = append(r.outQ, out[:m])
			out = out[m:]
		}
		if eof {
			r.done = true
		}
	}
	chunk := r.outQ[0]
	r.outQ = r.outQ[1:]
	return starlark.Bytes(chunk), true, nil
}

func sanitizeErr(c *client, err error) error {
	var denied *capability.DeniedError
	if errors.As(err, &denied) {
		return &capability.DeniedError{Hook: denied.Hook, Phase: denied.Phase, Reason: c.redact(denied.Reason)}
	}
	return fmt.Errorf("%s", c.redact(err.Error()))
}

// module assembles the net binding: the buffered ops via
// capability.Bind plus the thread-aware stream builtin.
func (c *client) module(gate *capability.Gate) starlark.Value {
	s := bind(c, gate).(starlark.HasAttrs)
	members := starlark.StringDict{}
	for _, name := range s.AttrNames() {
		v, _ := s.Attr(name)
		members[name] = v
	}
	members["stream"] = c.streamMethod(gate)
	return starlarkstruct.FromStringDict(starlarkstruct.Default, members)
}
