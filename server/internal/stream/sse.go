package stream

import (
	"bytes"
	"errors"
	"unicode/utf8"

	"go.starlark.net/starlark"
)

// SSE framing bounds. The source is pulled lazily; a single yielded
// chunk may not exceed maxSSEChunk, and any line or accumulated event
// is capped at maxSSEEvent — no bufio.Scanner default hazards and no
// unbounded pending frame.
const (
	maxSSEChunk = 64 << 10
	maxSSEEvent = 1 << 20
)

// NewSSE derives a bounded incremental server-sent-events parser over
// source, a stream of byte chunks. It emits {event, data, id} dicts:
// event defaults to "message", multiline data: fields join with "\n",
// comments and unknown fields (including retry:) are ignored, and id
// persists across events unless a field contains NUL. Framing handles
// UTF-8 and CRLF/CR/LF split arbitrarily across chunks. At EOF an
// incomplete pending event is discarded, never synthesized; malformed
// UTF-8 is an error.
func NewSSE(scope *Scope, source *Stream) (*Stream, error) {
	if source == nil {
		return nil, errors.New("stream.sse: source stream required")
	}
	p := &sseParser{scope: scope}
	return New(scope, p.pullFunc(source), nil, source)
}

type sseParser struct {
	scope    *Scope
	buf      []byte // undecoded bytes; bounded by maxSSEEvent
	bomDone  bool
	skipLF   bool // a CR already terminated a line; swallow the next LF
	eof      bool
	data     []byte // accumulated data payload for the pending event
	event    string
	id       string
	haveData bool
}

func (p *sseParser) pullFunc(source *Stream) PullFunc {
	return func(t *starlark.Thread) (starlark.Value, bool, error) {
		for {
			if err := p.scope.ctx.Err(); err != nil {
				return nil, false, err
			}
			if ev, ok, err := p.nextEvent(); err != nil || ok {
				return ev, ok, err
			}
			if p.eof {
				return starlark.None, false, nil
			}
			v, ok, err := source.Pull(t)
			if err != nil {
				return nil, false, err
			}
			if !ok {
				p.eof = true
				// Whatever remains is an unfinished frame — CR already
				// terminated lines immediately, so a leftover trailing
				// CR is impossible here. Discard the partial frame but
				// still validate it: malformed UTF-8 is never tolerated.
				if !utf8.Valid(p.buf) {
					return nil, false, errors.New("stream.sse: invalid UTF-8")
				}
				p.buf = nil
				p.data, p.event, p.haveData = nil, "", false
				continue
			}
			chunk, isBytes := v.(starlark.Bytes)
			if !isBytes {
				return nil, false, errors.New("stream.sse: source must yield bytes")
			}
			if len(chunk) > maxSSEChunk {
				return nil, false, errors.New("stream.sse: source chunk exceeds byte limit")
			}
			if len(p.buf)+len(chunk) > maxSSEEvent {
				return nil, false, errors.New("stream.sse: event exceeds byte limit")
			}
			p.buf = append(p.buf, chunk...)
		}
	}
}

// nextEvent scans p.buf for one complete event. It returns
// (value, true, nil) for a dispatched event, (nil, false, nil) when
// more input is needed, or an error on malformed UTF-8 / overflow.
func (p *sseParser) nextEvent() (starlark.Value, bool, error) {
	for {
		line, ok, err := p.nextLine()
		if err != nil {
			return nil, false, err
		}
		if !ok {
			return nil, false, nil
		}
		ev, ok, err := p.line(line)
		if err != nil {
			return nil, false, err
		}
		if ok {
			return ev, true, nil
		}
	}
}

// nextLine extracts one full line. CR terminates a line immediately
// (no latency wait); a following LF — even in the next chunk — is
// swallowed via skipLF so CRLF counts once.
func (p *sseParser) nextLine() ([]byte, bool, error) {
	for i := 0; i < len(p.buf); i++ {
		if p.skipLF {
			p.skipLF = false
			if p.buf[i] == '\n' {
				p.buf = p.buf[i+1:]
			}
			i = -1
			continue
		}
		switch p.buf[i] {
		case '\n':
			line := p.buf[:i]
			p.buf = p.buf[i+1:]
			return line, true, nil
		case '\r':
			line := p.buf[:i]
			p.buf = p.buf[i+1:]
			p.skipLF = true
			return line, true, nil
		}
	}
	return nil, false, nil
}

func (p *sseParser) stripCR(line []byte) []byte {
	if n := len(line); n > 0 && line[n-1] == '\r' {
		return line[:n-1]
	}
	return line
}

// line processes one complete SSE line; it returns a dispatched event
// dict only on the blank-line terminator when data was accumulated.
func (p *sseParser) line(line []byte) (starlark.Value, bool, error) {
	if !p.bomDone {
		p.bomDone = true
		line = bytes.TrimPrefix(line, []byte{0xEF, 0xBB, 0xBF})
	}
	if len(line) == 0 {
		// Blank line: dispatch if data accumulated, and reset the
		// pending event fields either way (an event: line alone does
		// not leak into the next event).
		event := p.event
		data := p.data
		have := p.haveData
		p.data, p.event, p.haveData = nil, "", false
		if !have {
			return nil, false, nil
		}
		if event == "" {
			event = "message"
		}
		d := starlark.NewDict(3)
		_ = d.SetKey(starlark.String("event"), starlark.String(event))
		_ = d.SetKey(starlark.String("data"), starlark.String(string(data)))
		_ = d.SetKey(starlark.String("id"), starlark.String(p.id))
		return d, true, nil
	}
	if !utf8.Valid(line) {
		return nil, false, errors.New("stream.sse: invalid UTF-8")
	}
	if line[0] == ':' {
		return nil, false, nil // comment
	}
	field, value := line, []byte(nil)
	if i := bytes.IndexByte(line, ':'); i >= 0 {
		field, value = line[:i], line[i+1:]
		if len(value) > 0 && value[0] == ' ' {
			value = value[1:]
		}
	}
	switch string(field) {
	case "data":
		if len(p.data)+len(value)+1 > maxSSEEvent {
			return nil, false, errors.New("stream.sse: event exceeds byte limit")
		}
		if p.haveData {
			p.data = append(p.data, '\n')
		}
		p.data = append(p.data, value...)
		p.haveData = true
	case "event":
		p.event = string(value)
	case "id":
		if bytes.IndexByte(value, 0) < 0 {
			p.id = string(value)
		}
	}
	return nil, false, nil
}
