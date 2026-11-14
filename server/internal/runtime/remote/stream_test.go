package remote

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

type recSink struct {
	started  int
	items    []string
	startErr error
	itemErr  error
}

func (s *recSink) Start() error {
	s.started++
	return s.startErr
}
func (s *recSink) Item(b json.RawMessage) error {
	if s.itemErr != nil {
		return s.itemErr
	}
	s.items = append(s.items, string(b))
	return nil
}

const validPrefix = `{"type":"start"}` + "\n" + `{"type":"item","item":123}` + "\n" + `{"type":"item","item":456}` + "\n"

const termOK = `{"type":"result","response":{"result":{"id":"r1","streaming":true,"stream_items":2,"stream_bytes":6},"audit":{}}}` + "\n"

func runFrames(t *testing.T, frames string) (*recSink, error) {
	t.Helper()
	s := &recSink{}
	_, err := decodeInvokeStream(strings.NewReader(frames), s)
	return s, err
}

func TestDecodeInvokeStreamValid(t *testing.T) {
	s, err := runFrames(t, `{"type":"start"}`+"\n"+
		`{"type":"item","item":null}`+"\n"+
		`{"type":"item","item":{"n":9007199254740993}}`+"\n"+
		`{"type":"result","response":{"result":{"id":"r1","streaming":true,"stream_items":2,"stream_bytes":26},"audit":{}}}`+"\n")
	if err != nil {
		t.Fatal(err)
	}
	if s.started != 1 || len(s.items) != 2 || s.items[0] != "null" || s.items[1] != `{"n":9007199254740993}` {
		t.Fatalf("%d %v", s.started, s.items)
	}
}

func TestDecodeInvokeStreamReject(t *testing.T) {
	cases := map[string]string{
		"item before start":      `{"type":"item","item":1}` + "\n",
		"duplicate start":        `{"type":"start"}` + "\n" + `{"type":"start"}` + "\n" + termOK,
		"unknown type":           `{"type":"start"}` + "\n" + `{"type":"mystery"}` + "\n",
		"no terminal":            `{"type":"start"}` + "\n" + `{"type":"item","item":1}` + "\n",
		"truncated json":         `{"type":"start"}` + "\n" + `{"type":"item","i`,
		"terminal partial tail":  validPrefix + termOK + `{"type":"item"`,
		"duplicate terminal":     validPrefix + termOK + termOK,
		"frame after terminal":   validPrefix + termOK + `{"type":"item","item":1}` + "\n",
		"oversized line":         `{"type":"start"}` + "\n" + `{"type":"item","item":"` + strings.Repeat("x", streamMaxFrame) + `"}` + "\n",
		"item missing payload":   `{"type":"start"}` + "\n" + `{"type":"item"}` + "\n",
		"start with item":        `{"type":"start","item":1}` + "\n",
		"item with response":     `{"type":"start"}` + "\n" + `{"type":"item","item":1,"response":{}}` + "\n",
		"error before start":     `{"type":"error","error":{"error":{"kind":"x","message":"y"}}}` + "\n",
		"error empty kind":       `{"type":"start"}` + "\n" + `{"type":"error","error":{"error":{"kind":"","message":"y"}}}` + "\n",
		"error empty message":    `{"type":"start"}` + "\n" + `{"type":"error","error":{"error":{"kind":"x","message":""}}}` + "\n",
		"counter mismatch items": `{"type":"start"}` + "\n" + `{"type":"item","item":1}` + "\n" + termOK,
		"counter mismatch bytes": `{"type":"start"}` + "\n" + `{"type":"item","item":12}` + "\n" + `{"type":"item","item":34}` + "\n" + termOK,
		"result not streaming":   `{"type":"start"}` + "\n" + `{"type":"result","response":{"result":{"id":"r1"},"audit":{}}}` + "\n",
		"terminal with waiting":  `{"type":"start"}` + "\n" + `{"type":"result","response":{"result":{"id":"r1","streaming":true,"waiting":{"interrupt_id":"i","console_url":"u"}},"audit":{}}}` + "\n",
		"empty body":             "",
		"bare text":              "hello\n",
	}
	for name, frames := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := runFrames(t, frames); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestDecodeInvokeStreamErrorTerminal(t *testing.T) {
	frames := `{"type":"start"}` + "\n" + `{"type":"item","item":1}` + "\n" +
		`{"type":"error","error":{"error":{"kind":"runtime","message":"boom"}}}` + "\n"
	s, err := runFrames(t, frames)
	if err == nil || s.items[0] != "1" {
		t.Fatalf("err %v items %v", err, s.items)
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("want typed error, got %v", err)
	}
}

func TestDecodeInvokeStreamSinkError(t *testing.T) {
	s := &recSink{itemErr: errors.New("client gone")}
	_, err := decodeInvokeStream(strings.NewReader(`{"type":"start"}`+"\n"+`{"type":"item","item":1}`+"\n"), s)
	if err == nil || err.Error() != "client gone" {
		t.Fatalf("%v", err)
	}
	if _, err := decodeInvokeStream(strings.NewReader(`{"type":"start"}`+"\n"), &recSink{startErr: errors.New("no")}); err == nil {
		t.Fatal("start error must propagate")
	}
}

type failWriter struct {
	header   http.Header
	status   int
	wrote    [][]byte
	writeErr error
	deadline time.Time
	flushN   int
}

var _ http.Flusher = (*failWriter)(nil)

func (w *failWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}
func (w *failWriter) WriteHeader(code int) { w.status = code }
func (w *failWriter) Write(b []byte) (int, error) {
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	w.wrote = append(w.wrote, append([]byte(nil), b...))
	return len(b), nil
}
func (w *failWriter) Flush() {
	w.flushN++
}
func (w *failWriter) SetWriteDeadline(d time.Time) error {
	w.deadline = d
	return nil
}

func TestNDJSONSinkFlushFailure(t *testing.T) {
	w := &failWriter{}
	s := newNDJSONSink(context.Background(), w)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(); err == nil {
		t.Fatal("duplicate Start accepted")
	}
	if got := w.Header().Get("Content-Type"); got != ndjsonMediaType {
		t.Fatalf("content-type %q", got)
	}
	f2 := &failWriter{writeErr: errors.New("conn reset")}
	s2 := newNDJSONSink(context.Background(), f2)
	if err := s2.Start(); err == nil || err.Error() != "conn reset" {
		t.Fatalf("want write error, got %v", err)
	}
	if s2.items != 0 {
		t.Fatal("failed delivery counted")
	}
	if err := s2.Item(json.RawMessage(`2`)); err == nil {
		t.Fatal("subsequent writes must shortcircuit")
	}

	w3 := &unflushable{header: http.Header{}}
	s3 := newNDJSONSink(context.Background(), w3)
	if err := s3.Start(); err == nil {
		t.Fatal("flush failure must surface")
	}
	if s3.err == nil {
		t.Fatal("s.err not recorded")
	}
	if err := s3.Item(json.RawMessage(`1`)); err == nil {
		t.Fatal("subsequent writes must shortcircuit")
	}
}

type unflushable struct {
	header http.Header
}

func (w *unflushable) Header() http.Header         { return w.header }
func (w *unflushable) Write(b []byte) (int, error) { return len(b), nil }
func (w *unflushable) WriteHeader(int)             {}

func TestNDJSONSinkDeadline(t *testing.T) {
	d := time.Now().Add(time.Second)
	ctx, cancel := context.WithDeadline(context.Background(), d)
	defer cancel()
	w := &failWriter{}
	s := newNDJSONSink(ctx, w)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	if !w.deadline.Equal(d) {
		t.Fatalf("write deadline %v want %v", w.deadline, d)
	}
	w2 := &failWriter{}
	s2 := newNDJSONSink(context.Background(), w2)
	if err := s2.Start(); err != nil {
		t.Fatal(err)
	}
	if got := time.Until(w2.deadline); got <= 0 || got > streamWriteGrace {
		t.Fatalf("grace %v", got)
	}
}
