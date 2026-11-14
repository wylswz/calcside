package remote

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"calcside/internal/runtime"
	"calcside/internal/runtime/remote/gen"
)

const (
	ndjsonMediaType  = "application/x-ndjson"
	streamMaxFrame   = 8 << 20
	streamWriteGrace = 30 * time.Second
)

func acceptsNDJSON(ctx context.Context) bool {
	if c, ok := ctx.(interface{ Request() *http.Request }); ok {
		return strings.Contains(c.Request().Header.Get("Accept"), ndjsonMediaType)
	}
	type ginCtx interface{ GetHeader(string) string }
	if c, ok := ctx.(ginCtx); ok {
		return strings.Contains(c.GetHeader("Accept"), ndjsonMediaType)
	}
	return false
}

type ndjsonSink struct {
	w        http.ResponseWriter
	rc       *http.ResponseController
	deadline time.Time
	started  bool
	err      error
	items    int64
	bytes    int64
}

var _ runtime.InvokeStreamSink = (*ndjsonSink)(nil)
var _ runtime.InvokeStreamSinkStats = (*ndjsonSink)(nil)

func newNDJSONSink(ctx context.Context, w http.ResponseWriter) *ndjsonSink {
	s := &ndjsonSink{w: w, rc: http.NewResponseController(w)}
	if d, ok := ctx.Deadline(); ok {
		s.deadline = d
	}
	return s
}

func (s *ndjsonSink) Stats() (bool, int64, int64) { return s.started, s.items, s.bytes }

func (s *ndjsonSink) Start() error {
	if s.err != nil {
		return s.err
	}
	if s.started {
		return errors.New("remote: duplicate stream start")
	}
	s.w.Header().Set("Content-Type", ndjsonMediaType)
	s.w.Header().Set("Cache-Control", "no-cache")
	s.w.WriteHeader(http.StatusOK)
	s.started = true
	return s.frame([]byte(`{"type":"start"}`))
}

func (s *ndjsonSink) Item(raw json.RawMessage) error {
	if !s.started {
		return errors.New("remote: stream item before start")
	}
	frame := make([]byte, 0, len(raw)+32)
	frame = append(frame, `{"type":"item","item":`...)
	frame = append(frame, raw...)
	frame = append(frame, '}')
	if err := s.frame(frame); err != nil {
		return err
	}
	s.items++
	s.bytes += int64(len(raw))
	return nil
}

func (s *ndjsonSink) frame(b []byte) error {
	if s.err != nil {
		return s.err
	}
	deadline := time.Now().Add(streamWriteGrace)
	if !s.deadline.IsZero() && s.deadline.Before(deadline) {
		deadline = s.deadline
	}
	_ = s.rc.SetWriteDeadline(deadline)
	b = append(b, '\n')
	if _, err := s.w.Write(b); err != nil {
		s.err = err
		return err
	}
	if err := s.rc.Flush(); err != nil {
		s.err = err
		return err
	}
	return nil
}

type invokeStreamResponse struct {
	rt  runtime.Runtime
	ctx context.Context
	req *runtime.InvokeRequest
}

var _ gen.RuntimeInvokeResponseObject = (*invokeStreamResponse)(nil)

func (r *invokeStreamResponse) VisitRuntimeInvokeResponse(w http.ResponseWriter) error {
	sink := newNDJSONSink(r.ctx, w)
	resp, err := r.rt.Invoke(runtime.WithInvokeStream(r.ctx, sink), r.req)
	if !sink.started {
		if err != nil {
			body, merr := json.Marshal(envelope(err))
			if merr != nil {
				return merr
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(statusOf(err))
			_, werr := w.Write(body)
			return werr
		}
		body, merr := json.Marshal(resp)
		if merr != nil {
			return merr
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, werr := w.Write(body)
		return werr
	}
	if err != nil {
		body, merr := json.Marshal(map[string]any{"type": "error", "error": envelope(err)})
		if merr != nil {
			return merr
		}
		if ferr := sink.frame(body); ferr != nil {
			return ferr
		}
		return sink.err
	}
	body, merr := json.Marshal(map[string]any{"type": "result", "response": resp})
	if merr != nil {
		return merr
	}
	if ferr := sink.frame(body); ferr != nil {
		return ferr
	}
	return sink.err
}

type invokeFrame struct {
	Type     string                  `json:"type"`
	Item     json.RawMessage         `json:"item,omitempty"`
	Response *runtime.InvokeResponse `json:"response,omitempty"`
	Error    *gen.ErrorEnvelope      `json:"error,omitempty"`
}

func readBoundedLine(br *bufio.Reader, max int) ([]byte, error) {
	var buf []byte
	for {
		frag, err := br.ReadSlice('\n')
		buf = append(buf, frag...)
		if len(buf) > max {
			return nil, errors.New("remote: stream frame too large")
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil {
			return buf, err
		}
		return buf[:len(buf)-1], nil
	}
}

func decodeInvokeStream(body io.Reader, sink runtime.InvokeStreamSink) (*runtime.InvokeResponse, error) {
	br := bufio.NewReaderSize(body, 64<<10)
	started := false
	var terminal *invokeFrame
	var items, bytes int64
	for {
		line, err := readBoundedLine(br, streamMaxFrame)
		if err != nil {
			if terminal != nil && errors.Is(err, io.EOF) && len(line) == 0 {
				break
			}
			return nil, fmt.Errorf("remote: stream ended before the terminal frame: %w", err)
		}
		if terminal != nil {
			return nil, errors.New("remote: frame after terminal")
		}
		var f invokeFrame
		if err := json.Unmarshal(line, &f); err != nil {
			return nil, errors.New("remote: invalid stream frame")
		}
		payloads := 0
		if f.Item != nil {
			payloads++
		}
		if f.Response != nil {
			payloads++
		}
		if f.Error != nil {
			payloads++
		}
		expect := map[string]int{"start": 0, "item": 1, "result": 1, "error": 1}[f.Type]
		if expect == 0 && f.Type != "start" {
			return nil, errors.New("remote: unknown stream frame")
		}
		if payloads != expect || (f.Type == "item" && f.Item == nil) || (f.Type == "result" && f.Response == nil) || (f.Type == "error" && f.Error == nil) {
			return nil, errors.New("remote: invalid stream frame payload")
		}
		switch f.Type {
		case "start":
			if started {
				return nil, errors.New("remote: duplicate start frame")
			}
			started = true
			if err := sink.Start(); err != nil {
				return nil, err
			}
		case "item":
			if !started || f.Item == nil {
				return nil, errors.New("remote: stream frame out of order")
			}
			if err := sink.Item(f.Item); err != nil {
				return nil, err
			}
			items++
			bytes += int64(len(f.Item))
		case "result", "error":
			if !started {
				return nil, errors.New("remote: terminal frame before start")
			}
			if f.Type == "result" && f.Response == nil {
				return nil, errors.New("remote: invalid result frame")
			}
			if f.Type == "error" && (f.Error == nil || f.Error.Error.Kind == "" || f.Error.Error.Message == "") {
				return nil, errors.New("remote: invalid error frame")
			}
			g := f
			terminal = &g
		default:
			return nil, errors.New("remote: unknown stream frame")
		}
	}
	if terminal == nil {
		return nil, errors.New("remote: stream ended before the terminal frame")
	}
	if terminal.Type == "error" {
		return nil, errorOf(string(terminal.Error.Error.Kind), terminal.Error.Error.Message)
	}
	resp := terminal.Response
	if resp.Result.Waiting != nil {
		return nil, errors.New("remote: terminal result cannot wait mid-stream")
	}
	if !resp.Result.Streaming {
		return nil, errors.New("remote: terminal result is not a stream result")
	}
	if resp.Result.StreamItems != items || resp.Result.StreamBytes != bytes {
		return nil, errors.New("remote: stream counters do not match delivered frames")
	}
	return resp, nil
}
