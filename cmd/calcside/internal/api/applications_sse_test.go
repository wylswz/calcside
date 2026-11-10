package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"calcside/cmd/calcside/internal/service/application"
	"calcside/internal/runtime"
	"calcside/internal/store"
)

type sseW struct {
	header   http.Header
	status   int
	wrote    [][]byte
	writeErr error
	flushErr error
}

func (w *sseW) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}
func (w *sseW) WriteHeader(c int) { w.status = c }
func (w *sseW) Write(b []byte) (int, error) {
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	w.wrote = append(w.wrote, append([]byte(nil), b...))
	return len(b), nil
}
func (w *sseW) Flush()            {}
func (w *sseW) FlushError() error { return w.flushErr }

type bareW struct {
	header http.Header
	wrote  [][]byte
}

func (w *bareW) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}
func (w *bareW) WriteHeader(int) {}
func (w *bareW) Write(b []byte) (int, error) {
	w.wrote = append(w.wrote, append([]byte(nil), b...))
	return len(b), nil
}

func TestSSESinkStartItemErrors(t *testing.T) {
	w := &sseW{}
	s := &sseSink{w: w, rc: http.NewResponseController(w)}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(); err == nil {
		t.Fatal("duplicate Start accepted")
	}
	if !strings.Contains(w.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("ct %s", w.Header().Get("Content-Type"))
	}
	if err := s.Item(json.RawMessage(`{"n":1}`)); err != nil {
		t.Fatal(err)
	}
	if s.items != 1 || s.bytes != 7 {
		t.Fatalf("items=%d bytes=%d", s.items, s.bytes)
	}
	if !strings.Contains(string(w.wrote[len(w.wrote)-1]), "event: item\ndata: {\"n\":1}") {
		t.Fatalf("frame %s", w.wrote[len(w.wrote)-1])
	}

	wf := &bareW{}
	sf := &sseSink{w: wf, rc: http.NewResponseController(wf)}
	if err := sf.Start(); err == nil {
		t.Fatal("flush failure must surface")
	}
	if err := sf.Item(json.RawMessage(`1`)); err == nil {
		t.Fatal("later writes must shortcircuit")
	}

	w2 := &sseW{writeErr: errors.New("gone")}
	s2 := &sseSink{w: w2, rc: http.NewResponseController(w2)}
	if err := s2.Start(); err == nil || err.Error() != "gone" {
		t.Fatalf("write error must surface: %v", err)
	}
	if s2.items != 0 {
		t.Fatal("failed delivery counted")
	}
	if err := s2.Item(json.RawMessage(`1`)); err == nil {
		t.Fatal("later writes must shortcircuit")
	}
}

func TestSSESinkFlushFailureAfterStart(t *testing.T) {
	w := &sseW{}
	s := &sseSink{w: w, rc: http.NewResponseController(w)}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("flush failed")
	w.flushErr = boom
	if err := s.Item(json.RawMessage(`1`)); !errors.Is(err, boom) {
		t.Fatalf("flush error: %v", err)
	}
	if s.items != 0 || s.bytes != 0 {
		t.Fatalf("failed delivery counted: %d/%d", s.items, s.bytes)
	}
	writes := len(w.wrote)
	if err := s.Item(json.RawMessage(`2`)); !errors.Is(err, boom) || len(w.wrote) != writes {
		t.Fatalf("write after failure: %v, %d -> %d", err, writes, len(w.wrote))
	}
}

func rvWithError(code string) *application.RunView {
	return &application.RunView{
		Result: runtime.ProgramResult{
			ID: "inv_t", Streaming: true,
			Error: &runtime.ProgramError{Code: code, Message: "x"},
		},
		RunID: "run_t", RunStatus: store.RunFailed,
	}
}

func lastFrame(w *sseW) string {
	if len(w.wrote) == 0 {
		return ""
	}
	return string(w.wrote[len(w.wrote)-1])
}

func TestAppRunRespResultErrorEmitsErrorNotDone(t *testing.T) {
	w := &sseW{}
	resp := appRunResp{ctx: context.Background(), call: func(ctx context.Context) (*application.RunView, error) {
		s := runtime.InvokeStreamFromContext(ctx).(*sseSink)
		if err := s.Start(); err != nil {
			t.Fatal(err)
		}
		if err := s.Item(json.RawMessage(`{"ok":true}`)); err != nil {
			t.Fatal(err)
		}
		return rvWithError("invalid_output"), nil
	}}
	if err := resp.serve(w); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(lastFrame(w), "event: error") {
		t.Fatalf("want error terminal: %s", lastFrame(w))
	}
	if strings.Contains(string(w.wrote[len(w.wrote)-1]), "event: done") {
		t.Fatal("done emitted")
	}
	if !strings.Contains(lastFrame(w), "run_t") || !strings.Contains(lastFrame(w), `"stream_items":1`) {
		t.Fatalf("metadata lost: %s", lastFrame(w))
	}
}

func TestAppRunRespUnknownOutcomeEmitsErrorNotDone(t *testing.T) {
	w := &sseW{}
	resp := appRunResp{ctx: context.Background(), call: func(ctx context.Context) (*application.RunView, error) {
		s := runtime.InvokeStreamFromContext(ctx).(*sseSink)
		if err := s.Start(); err != nil {
			t.Fatal(err)
		}
		return &application.RunView{
			Result: runtime.ProgramResult{ID: "inv_t", Streaming: true},
			RunID:  "run_u", RunStatus: store.RunUnknown,
		}, nil
	}}
	if err := resp.serve(w); err != nil {
		t.Fatal(err)
	}
	body := lastFrame(w)
	if !strings.Contains(body, "event: error") || strings.Contains(body, "event: done") {
		t.Fatalf("unknown must be error terminal: %s", body)
	}
	if !strings.Contains(body, `"code":"internal"`) {
		t.Fatalf("synthesized safe error missing: %s", body)
	}
}

func TestAppRunRespServiceErrorAfterStart(t *testing.T) {
	w := &sseW{}
	resp := appRunResp{ctx: context.Background(), call: func(ctx context.Context) (*application.RunView, error) {
		s := runtime.InvokeStreamFromContext(ctx).(*sseSink)
		if err := s.Start(); err != nil {
			t.Fatal(err)
		}
		if err := s.Item(json.RawMessage(`1`)); err != nil {
			t.Fatal(err)
		}
		return nil, errors.New("boom")
	}}
	if err := resp.serve(w); err != nil {
		t.Fatal(err)
	}
	body := lastFrame(w)
	if !strings.Contains(body, "event: error") || strings.Contains(body, `"stream_items":1`) == false {
		t.Fatalf("service error terminal: %s", body)
	}
	if strings.Contains(body, "boom") {
		t.Fatal("internal detail leaked")
	}
}
