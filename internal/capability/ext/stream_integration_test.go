package ext_test

// EP-0021 provider-stream integration: real contrib extensions and the
// real net capability against local httptest fixtures. No external
// network, no DB; every server gets bounded deadlines and released
// channels so Server.Close never hangs.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"calcside/internal/capability"
	"calcside/internal/capability/ext"
	capnet "calcside/internal/capability/net"
	"calcside/internal/engine"
	"calcside/internal/secrets"
	"calcside/internal/types"
)

const contribRoot = "../../../contrib"

type obs struct{ recs []capability.Record }

func (o *obs) Observe(r capability.Record) { o.recs = append(o.recs, r) }

// providerFixture describes one bundled provider's wire protocol.
type providerFixture struct {
	name       string
	source     string
	model      string
	cfg        func(srv *httptest.Server) map[string]any
	wantPath   string
	wantStream bool // assert JSON body stream:true (Gemini uses the route)
	frames     string
	deltas     []string
	finish     string
	usage      map[string]int64 // nil => done carries null usage
}

func providerFixtures() []providerFixture {
	return []providerFixture{
		{
			name: "openai", source: "contrib/llm-openai@0.1.0", model: "m1",
			cfg:      func(srv *httptest.Server) map[string]any { return map[string]any{"base_url": srv.URL, "api_key": ""} },
			wantPath: "/responses", wantStream: true,
			frames: "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hé\"}\n\n" +
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\"w0rld-δ2\"}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":7,\"output_tokens\":3}}}\n\n",
			deltas: []string{"hé", "w0rld-δ2"}, finish: "completed",
			usage: map[string]int64{"input_tokens": 7, "output_tokens": 3},
		},
		{
			name: "compat", source: "contrib/llm-openai-compat@0.1.0", model: "m1",
			cfg:      func(srv *httptest.Server) map[string]any { return map[string]any{"base_url": srv.URL, "api_key": ""} },
			wantPath: "/chat/completions", wantStream: true,
			frames: "data: {\"choices\":[{\"delta\":{\"content\":\"wörld\"},\"finish_reason\":null}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
				"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":5}}\n\n" +
				"data: [DONE]\n\n",
			deltas: []string{"wörld"}, finish: "stop",
			usage: map[string]int64{"input_tokens": 11, "output_tokens": 5},
		},
		{
			name: "anth", source: "contrib/llm-anthropic@0.1.0", model: "m1",
			cfg: func(srv *httptest.Server) map[string]any {
				return map[string]any{"base_url": srv.URL, "api_key": "{{secrets.KEY}}"}
			},
			wantPath: "/v1/messages", wantStream: true,
			frames: "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":9}}}\n\n" +
				"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
				"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"日本語\"}}\n\n" +
				"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":4}}\n\n" +
				"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
			deltas: []string{"日本語"}, finish: "end_turn",
			usage: map[string]int64{"input_tokens": 9, "output_tokens": 4},
		},
		{
			name: "gemini", source: "contrib/llm-gemini@0.1.0", model: "m1",
			cfg: func(srv *httptest.Server) map[string]any {
				return map[string]any{"base_url": srv.URL, "api_key": "{{secrets.KEY}}"}
			},
			wantPath: "/v1beta/models/m1:streamGenerateContent", wantStream: false,
			frames: "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"olá\"}]}}]}\n\n" +
				"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"!\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":6,\"candidatesTokenCount\":2}}\n\n",
			deltas: []string{"olá", "!"}, finish: "STOP",
			usage: map[string]int64{"input_tokens": 6, "output_tokens": 2},
		},
	}
}

// llmSession builds an engine session with real net + ext factories and
// the llm capability bound to the given provider specs.
func llmSession(t *testing.T, hooks []capability.Hook, ob capability.Observer, provs []providerFixture, srvFor map[string]*httptest.Server) (*engine.Session, *engine.Engine) {
	t.Helper()
	reg := capability.NewRegistry()
	limits := capability.ServerLimits{SecretsAllowHTTP: true}
	nf := capnet.Factory()
	reg.Register(nf)
	ef := ext.Factory(ext.Options{LocalRoots: []string{contribRoot, t.TempDir()}})
	reg.Register(ef)

	hosts := []string{}
	for _, pf := range provs {
		h := strings.TrimPrefix(srvFor[pf.name].URL, "http://")
		hosts = append(hosts, h)
	}
	netJSON, _ := json.Marshal(map[string]any{"allow_hosts": hosts, "methods": []string{"POST"}, "max_response_bytes": 1 << 20})
	netCfg, err := nf.Validate(netJSON, limits)
	if err != nil {
		t.Fatal(err)
	}
	var llmSpecs []any
	for _, pf := range provs {
		llmSpecs = append(llmSpecs, map[string]any{
			"name":   pf.name,
			"source": pf.source,
			"models": []any{pf.model},
			"config": pf.cfg(srvFor[pf.name]),
		})
	}
	spec, _ := json.Marshal(map[string]any{"llm": map[string]any{"source": "contrib/llm@0.1.0", "llm": llmSpecs}})
	extCfg, err := ef.Validate(spec, limits)
	if err != nil {
		t.Fatal(err)
	}
	secs := secrets.NewSet()
	secs.Add("KEY", []byte("test-api-key"), nil)
	s, err := engine.NewSession(engine.SessionDeps{
		Context: context.Background(), Registry: reg, Limits: limits,
		Hooks: hooks, Observer: ob, Secrets: secs,
	}, engine.SessionCreation{
		InstanceID: "ins", Capabilities: map[string]any{"net": netCfg, "ext": extCfg},
		MaxSteps: 1 << 20, MaxOutputBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, engine.New(4)
}

// sseServer serves the fixture frames with SSE content type; checks is
// an optional request-body assertion.
func sseServer(t *testing.T, f providerFixture, checks func(t *testing.T, body string)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("%s: method %s", f.name, r.Method)
		}
		if f.source == "contrib/llm-gemini@0.1.0" {
			if r.URL.Path != f.wantPath || r.URL.Query().Get("alt") != "sse" {
				t.Errorf("gemini path %s?%s", r.URL.Path, r.URL.RawQuery)
			}
		} else if r.URL.Path != f.wantPath {
			t.Errorf("%s path %s", f.name, r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
			t.Errorf("%s Content-Type %q", f.name, ct)
		}
		body, _ := io.ReadAll(r.Body)
		if f.wantStream && !strings.Contains(string(body), `"stream":true`) {
			t.Errorf("%s body missing stream flag: %s", f.name, body)
		}
		if f.source == "contrib/llm-gemini@0.1.0" && strings.Contains(string(body), `"stream"`) {
			t.Errorf("gemini body carries stream key: %s", body)
		}
		if checks != nil {
			checks(t, string(body))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fmt.Fprint(w, f.frames)
	}))
}

const collectScript = `
evts = []
res = ext.llm.stream(PROVIDER, [{"role": "user", "content": "PROMPTMARKER"}], model = MODEL, options = OPTIONS)
for ev in res:
    evts.append(ev)
print(json.encode(evts))
`

func runStream(t *testing.T, e *engine.Engine, s *engine.Session, provider, model string, options string) ([]map[string]any, *engine.Error) {
	t.Helper()
	code := strings.NewReplacer("PROVIDER", fmt.Sprintf("%q", provider), "MODEL", fmt.Sprintf("%q", model), "OPTIONS", options).Replace(collectScript)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res := e.Exec(ctx, s, "x", code, 8*time.Second, 1<<20, s.Out.String)
	if res.Error != nil {
		return nil, res.Error
	}
	line := strings.TrimSpace(s.Out.String())
	var evts []map[string]any
	if err := json.Unmarshal([]byte(line), &evts); err != nil {
		t.Fatalf("output %q: %v", line, err)
	}
	return evts, nil
}

func TestProviderStreamHappyPath(t *testing.T) {
	for _, f := range providerFixtures() {
		t.Run(f.name, func(t *testing.T) {
			srv := sseServer(t, f, nil)
			defer srv.Close()
			o := &obs{}
			s, e := llmSession(t, nil, o, []providerFixture{f}, map[string]*httptest.Server{f.name: srv})
			evts, err := runStream(t, e, s, f.name, f.model, "None")
			if err != nil {
				t.Fatalf("exec: %+v", err)
			}
			var deltas []string
			var done map[string]any
			for _, ev := range evts {
				if ev["type"] == "text_delta" {
					deltas = append(deltas, ev["text"].(string))
				} else if ev["type"] == "done" {
					if done != nil {
						t.Fatal("two done events")
					}
					done = ev
				} else {
					t.Fatalf("unexpected event %v", ev)
				}
			}
			if strings.Join(deltas, "|") != strings.Join(f.deltas, "|") {
				t.Fatalf("deltas %v want %v", deltas, f.deltas)
			}
			if done == nil || done["finish_reason"] != f.finish {
				t.Fatalf("done %v", done)
			}
			if f.usage == nil {
				if done["usage"] != nil {
					t.Fatalf("usage %v want null", done["usage"])
				}
			} else {
				u, ok := done["usage"].(map[string]any)
				if !ok {
					t.Fatalf("usage %v", done["usage"])
				}
				for k, want := range f.usage {
					if int64(u[k].(float64)) != want {
						t.Fatalf("usage %s=%v want %d", k, u[k], want)
					}
				}
			}
			// audit: terminal record carries safe provider/model meta
			var closeRec *capability.Record
			for i := range o.recs {
				r := &o.recs[i]
				if r.Call.Capability == types.CapExt && r.Call.Args[capability.StreamActionArg] == "close" {
					closeRec = r
				}
			}
			if closeRec == nil {
				t.Fatal("no close record")
			}
			if closeRec.Call.Args["provider"] != f.name || closeRec.Call.Args["model"] != f.model {
				t.Fatalf("provider/model meta %v", closeRec.Call.Args)
			}
			blob, _ := json.Marshal(o.recs)
			if strings.Contains(string(blob), "PROMPTMARKER") {
				t.Fatal("prompt leaked into audit")
			}
			for _, d := range f.deltas {
				if strings.Contains(string(blob), d) {
					t.Fatalf("completion delta %q leaked into audit", d)
				}
			}
			// validated usage visible in the terminal record args/meta
			if f.usage != nil {
				u, _ := closeRec.Call.Args["usage"].(map[string]any)
				got, _ := u["input_tokens"].(int64)
				if u == nil || got != f.usage["input_tokens"] {
					t.Fatalf("terminal usage %v", closeRec.Call.Args["usage"])
				}
			}
		})
	}
}

// errorFixture variants per provider: EOF after delta, provider error
// terminal, malformed JSON, wrong content type.
func TestProviderStreamErrors(t *testing.T) {
	mk := func(f providerFixture, frames string, ct string) (*httptest.Server, *engine.Session, *engine.Engine) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", ct)
			fmt.Fprint(w, frames)
		}))
		t.Cleanup(srv.Close)
		s, e := llmSession(t, nil, nil, []providerFixture{f}, map[string]*httptest.Server{f.name: srv})
		return srv, s, e
	}
	errFrames := map[string]struct{ eofAfterDelta, provErr, badJSON string }{
		"openai": {
			eofAfterDelta: "data: {\"type\":\"response.output_text.delta\",\"delta\":\"x\"}\n\n",
			provErr:       "data: {\"type\":\"response.failed\"}\n\n",
			badJSON:       "data: {not json\n\n",
		},
		"compat": {
			eofAfterDelta: "data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n",
			provErr:       "data: {\"error\":{\"message\":\"boom\"}}\n\n",
			badJSON:       "data: {not json\n\n",
		},
		"anth": {
			eofAfterDelta: "event: content_block_delta\ndata: {\"delta\":{\"type\":\"text_delta\",\"text\":\"x\"}}\n\n",
			provErr:       "event: error\ndata: {\"type\":\"error\"}\n\n",
			badJSON:       "event: x\ndata: {not json\n\n",
		},
		"gemini": {
			eofAfterDelta: "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"x\"}]}}]}\n\n",
			provErr:       "data: {\"candidates\":[{\"content\":{\"parts\":[]},\"finishReason\":\"SAFETY\"}]}\n\n",
			badJSON:       "data: {not json\n\n",
		},
	}
	for _, f := range providerFixtures() {
		ef := errFrames[f.name]
		t.Run(f.name+"/eof-after-delta", func(t *testing.T) {
			_, s, e := mk(f, ef.eofAfterDelta, "text/event-stream")
			if _, err := runStream(t, e, s, f.name, f.model, "None"); err == nil || err.Type != types.ErrRuntime {
				t.Fatalf("expected runtime error, got %+v", err)
			}
		})
		t.Run(f.name+"/provider-error", func(t *testing.T) {
			_, s, e := mk(f, ef.provErr, "text/event-stream")
			if _, err := runStream(t, e, s, f.name, f.model, "None"); err == nil || err.Type != types.ErrRuntime {
				t.Fatalf("expected runtime error, got %+v", err)
			}
		})
		t.Run(f.name+"/malformed-json", func(t *testing.T) {
			_, s, e := mk(f, ef.badJSON, "text/event-stream")
			if _, err := runStream(t, e, s, f.name, f.model, "None"); err == nil || err.Type != types.ErrRuntime {
				t.Fatalf("expected runtime error, got %+v", err)
			}
		})
		t.Run(f.name+"/wrong-content-type", func(t *testing.T) {
			_, s, e := mk(f, f.frames, "application/json")
			if _, err := runStream(t, e, s, f.name, f.model, "None"); err == nil || err.Type != types.ErrRuntime {
				t.Fatalf("expected runtime error, got %+v", err)
			}
		})
	}
}

// Laziness: the server releases the terminal frame only after the
// consumer has observed the first delta — no sleeps.
func TestProviderStreamLazinessHandshake(t *testing.T) {
	f := providerFixtures()[0] // openai
	gotDelta := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		// first frame CR-only terminated: exercises immediate dispatch
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\r\r")
		fl.Flush()
		select {
		case <-gotDelta:
		case <-r.Context().Done():
			return
		case <-time.After(5 * time.Second):
			t.Error("consumer never observed first delta")
			return
		}
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
	}))
	defer srv.Close()
	s, e := llmSession(t, nil, nil, []providerFixture{f}, map[string]*httptest.Server{f.name: srv})
	code := `
s = ext.llm.stream("openai", [{"role": "user", "content": "hi"}], model = "m1")
evts = []
for ev in s:
    evts.append(ev)
    print("OBSERVED")
print(json.encode(evts))
`
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// signal after the consumer prints OBSERVED for the first delta:
	go func() {
		for {
			if strings.Contains(s.Out.String(), "OBSERVED") {
				close(gotDelta)
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
	}()
	res := e.Exec(ctx, s, "x", code, 8*time.Second, 1<<20, s.Out.String)
	if res.Error != nil {
		t.Fatalf("exec: %+v", res.Error)
	}
}

// After break, exec-end teardown (not the iterator) releases the
// request; the server unblocks at exec completion. An explicit
// body.close() in the script releases the raw HTTP response earlier.
func TestProviderStreamBreakCancels(t *testing.T) {
	f := providerFixtures()[0]
	cancelled := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(cancelled)
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done() // block until cancelled
	}))
	defer srv.Close()
	s, e := llmSession(t, nil, nil, []providerFixture{f}, map[string]*httptest.Server{f.name: srv})
	code := `
s = ext.llm.stream("openai", [{"role": "user", "content": "hi"}], model = "m1")
for ev in s:
    break
print("broke")
`
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res := e.Exec(ctx, s, "x", code, 8*time.Second, 1<<20, s.Out.String)
	if res.Error != nil {
		t.Fatalf("exec: %+v", res.Error)
	}
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("server not unblocked by break")
	}
}

// options.stream=False cannot disable streaming.
func TestProviderStreamOptionCannotDisable(t *testing.T) {
	for _, f := range providerFixtures() {
		t.Run(f.name, func(t *testing.T) {
			srv := sseServer(t, f, func(t *testing.T, body string) {
				if f.wantStream && !strings.Contains(body, `"stream":true`) {
					t.Errorf("stream disabled by options: %s", body)
				}
				if f.source == "contrib/llm-gemini@0.1.0" && strings.Contains(body, `"stream"`) {
					t.Errorf("gemini generationConfig.stream leaked: %s", body)
				}
			})
			defer srv.Close()
			s, e := llmSession(t, nil, nil, []providerFixture{f}, map[string]*httptest.Server{f.name: srv})
			if _, err := runStream(t, e, s, f.name, f.model, `{"stream": False}`); err != nil {
				t.Fatalf("exec: %+v", err)
			}
		})
	}
}

// Compat usage may arrive after finish_reason, before [DONE].
func TestProviderStreamUsageAfterFinish(t *testing.T) {
	f := providerFixtures()[1]
	f.frames = "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":2}}\n\n" +
		"data: [DONE]\n\n"
	srv := sseServer(t, f, nil)
	defer srv.Close()
	s, e := llmSession(t, nil, nil, []providerFixture{f}, map[string]*httptest.Server{f.name: srv})
	evts, err := runStream(t, e, s, f.name, f.model, "None")
	if err != nil {
		t.Fatal(err)
	}
	done := evts[len(evts)-1]
	u := done["usage"].(map[string]any)
	if int64(u["input_tokens"].(float64)) != 4 {
		t.Fatalf("post-finish usage lost: %v", done)
	}
}

// Compat tool-call finish is rejected even without a tool delta.
func TestProviderStreamToolFinishRejected(t *testing.T) {
	f := providerFixtures()[1]
	f.frames = "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: [DONE]\n\n"
	srv := sseServer(t, f, nil)
	defer srv.Close()
	s, e := llmSession(t, nil, nil, []providerFixture{f}, map[string]*httptest.Server{f.name: srv})
	if _, err := runStream(t, e, s, f.name, f.model, "None"); err == nil {
		t.Fatal("tool_calls finish accepted")
	}
}

// A done event with null usage still records completion.
func TestProviderStreamNilUsageComplete(t *testing.T) {
	f := providerFixtures()[0]
	f.frames = "data: {\"type\":\"response.output_text.delta\",\"delta\":\"x\"}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"
	srv := sseServer(t, f, nil)
	defer srv.Close()
	o := &obs{}
	s, e := llmSession(t, nil, o, []providerFixture{f}, map[string]*httptest.Server{f.name: srv})
	code := `s = ext.llm.stream("openai", [{"role": "user", "content": "x"}], model = "m1")
evts = []
for ev in s:
    evts.append(ev)
    if ev["type"] == "done":
        break
print(json.encode(evts))
`
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res := e.Exec(ctx, s, "x", code, 8*time.Second, 1<<20, s.Out.String)
	if res.Error != nil {
		t.Fatalf("exec: %+v", res.Error)
	}
	var closeRec *capability.Record
	for i := range o.recs {
		r := &o.recs[i]
		if r.Call.Capability == types.CapExt && r.Call.Args[capability.StreamActionArg] == "close" {
			closeRec = r
		}
	}
	if closeRec == nil || closeRec.Call.Args["complete"] != true {
		t.Fatalf("complete not recorded: %+v", closeRec)
	}
	if closeRec.Call.Args["stream_status"] != "complete" {
		t.Fatalf("status %v", closeRec.Call.Args["stream_status"])
	}
}

// A provider without a stream export fails before any network call.
func TestProviderStreamUnsupportedNoNetwork(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, "minip/0.1.0/capability.yaml", `name: minip
version: 0.1.0
interface: llm
models: [m1]
ops:
  - {name: respond, params: [messages, model, options]}
  - {name: structured, params: [messages, schema, model]}
`)
	writeTree(t, dir, "minip/0.1.0/main.star", "def respond(messages, model, options):\n    return {}\n\ndef structured(messages, schema, model):\n    return {}\n")

	reg := capability.NewRegistry()
	limits := capability.ServerLimits{SecretsAllowHTTP: true}
	nf := capnet.Factory()
	reg.Register(nf)
	ef := ext.Factory(ext.Options{LocalRoots: []string{contribRoot, dir}})
	reg.Register(ef)
	netCalls := 0
	countNet := countNetBefore{n: &netCalls}
	netCfg, err := nf.Validate([]byte(`{"methods":["POST"]}`), limits)
	if err != nil {
		t.Fatal(err)
	}
	base := dir[strings.LastIndex(dir, "/")+1:]
	spec, _ := json.Marshal(map[string]any{"llm": map[string]any{"source": "contrib/llm@0.1.0",
		"llm": []any{map[string]any{"name": "minip", "source": base + "/minip@0.1.0", "models": []any{"m1"}, "config": map[string]any{}}}}})
	extCfg, err := ef.Validate(spec, limits)
	if err != nil {
		t.Fatal(err)
	}
	s, err := engine.NewSession(engine.SessionDeps{Context: context.Background(), Registry: reg, Limits: limits, Hooks: []capability.Hook{countNet}},
		engine.SessionCreation{InstanceID: "i", Capabilities: map[string]any{"net": netCfg, "ext": extCfg}, MaxSteps: 1 << 20, MaxOutputBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	code := `s = ext.llm.stream("minip", [{"role":"user","content":"x"}], model="m1")`
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res := engine.New(4).Exec(ctx, s, "x", code, 5*time.Second, 1<<20, s.Out.String)
	if res.Error == nil || !strings.Contains(res.Error.Message, "unsupported_stream") {
		t.Fatalf("expected unsupported_stream, got %+v", res.Error)
	}
	if netCalls != 0 {
		t.Fatalf("network invoked for unsupported provider (%d gate calls)", netCalls)
	}
}

// countNetBefore counts gated net invocations at Before.
type countNetBefore struct{ n *int }

func (h countNetBefore) Name() string { return "count-net" }
func (h countNetBefore) Before(_ context.Context, c *capability.Call) error {
	if c.Capability == types.CapNet {
		*h.n++
	}
	return nil
}
func (h countNetBefore) After(context.Context, *capability.Call, *capability.Result) error {
	return nil
}

// Custom provider stream must be a real Stream with valid events.
func TestProviderStreamValidator(t *testing.T) {
	cases := map[string]struct{ body, wantErr string }{
		"list": {
			body:    "def stream(messages, model, options):\n    return [1, 2]\n",
			wantErr: "must return a stream",
		},
		"bad_event": {
			body:    "_streams = stream\n\ndef stream(messages, model, options):\n    def nxt():\n        return (True, {\"type\": \"bogus\"})\n    return _streams.new(nxt)\n",
			wantErr: "unsupported event type",
		},
		"bool_usage": {
			body:    "_streams = stream\n\ndef stream(messages, model, options):\n    def nxt():\n        return (True, {\"type\": \"done\", \"finish_reason\": \"stop\", \"usage\": {\"input_tokens\": True}})\n    return _streams.new(nxt)\n",
			wantErr: "invalid usage",
		},
		"no_done": {
			body:    "_streams = stream\n\ndef stream(messages, model, options):\n    def nxt():\n        return (False, None)\n    return _streams.new(nxt)\n",
			wantErr: "without terminal done",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeTree(t, dir, "minip/0.1.0/capability.yaml", `name: minip
version: 0.1.0
interface: llm
models: [m1]
ops:
  - {name: respond, params: [messages, model, options]}
  - {name: structured, params: [messages, schema, model]}
`)
			writeTree(t, dir, "minip/0.1.0/main.star",
				"def respond(messages, model, options):\n    return {}\n\ndef structured(messages, schema, model):\n    return {}\n\n"+tc.body)
			reg := capability.NewRegistry()
			limits := capability.ServerLimits{}
			nf := capnet.Factory()
			reg.Register(nf)
			netCfg, nerr := nf.Validate(nil, limits)
			if nerr != nil {
				t.Fatal(nerr)
			}
			ef := ext.Factory(ext.Options{LocalRoots: []string{dir, contribRoot}})
			reg.Register(ef)
			base := dir[strings.LastIndex(dir, "/")+1:]
			spec, _ := json.Marshal(map[string]any{"llm": map[string]any{"source": "contrib/llm@0.1.0",
				"llm": []any{map[string]any{"name": "minip", "source": base + "/minip@0.1.0", "models": []any{"m1"}, "config": map[string]any{}}}}})
			extCfg, err := ef.Validate(spec, limits)
			if err != nil {
				t.Fatal(err)
			}
			s, err := engine.NewSession(engine.SessionDeps{Context: context.Background(), Registry: reg, Limits: limits},
				engine.SessionCreation{InstanceID: "i", Capabilities: map[string]any{"net": netCfg, "ext": extCfg}, MaxSteps: 1 << 20, MaxOutputBytes: 1 << 20})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			code := `s = ext.llm.stream("minip", [{"role":"user","content":"x"}], model="m1")
for ev in s:
    pass`
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			res := engine.New(4).Exec(ctx, s, "x", code, 5*time.Second, 1<<20, s.Out.String)
			if res.Error == nil {
				t.Fatal("invalid provider stream accepted")
			}
			if res.Error.Type != types.ErrRuntime {
				t.Fatalf("error type %s (masking non-runtime failure?)", res.Error.Type)
			}
			if !strings.Contains(res.Error.Message, tc.wantErr) {
				t.Fatalf("error %q want substring %q", res.Error.Message, tc.wantErr)
			}
			if strings.Contains(res.Error.Message, "bogus") || strings.Contains(res.Error.Message, "ZZPAYLOAD") {
				t.Fatalf("payload echoed in error: %q", res.Error.Message)
			}
		})
	}
}

// writeTree writes a file under dir (dir may be nested like "a/b").
func writeTree(t *testing.T, dir, rel, content string) {
	t.Helper()
	parts := strings.Split(rel, "/")
	path := dir
	for _, p := range parts[:len(parts)-1] {
		path = path + "/" + p
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+"/"+parts[len(parts)-1], []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Terminal provider failures surface a fixed label + whitelisted
// code/reason — never provider payload/messages.
func TestOpenAIStreamFailureDiagnostics(t *testing.T) {
	f := providerFixtures()[0]
	mk := func(frames string) (*engine.Session, *engine.Engine) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, frames)
		}))
		t.Cleanup(srv.Close)
		s, e := llmSession(t, nil, nil, []providerFixture{f}, map[string]*httptest.Server{f.name: srv})
		return s, e
	}
	cases := []struct {
		name  string
		frame string
		want  string
	}{
		{"failed-server-error", `data: {"type":"response.failed","response":{"error":{"code":"server_error","message":"ZZPRIVATE_PAYLOAD"}}}` + "\n\n", "llm provider: response.failed (code=server_error)"},
		{"failed-rate-limit", `data: {"type":"response.failed","response":{"error":{"code":"rate_limit_exceeded"}}}` + "\n\n", "llm provider: response.failed (code=rate_limit_exceeded)"},
		{"incomplete-max-tokens", `data: {"type":"response.incomplete","response":{"incomplete_details":{"reason":"max_output_tokens"}}}` + "\n\n", "llm provider: response.incomplete (reason=max_output_tokens)"},
		{"incomplete-content-filter", `data: {"type":"response.incomplete","response":{"incomplete_details":{"reason":"content_filter"}}}` + "\n\n", "llm provider: response.incomplete (reason=content_filter)"},
		{"error-top-code", `data: {"type":"error","code":"insufficient_quota","message":"ZZPRIVATE_PAYLOAD"}` + "\n\n", "llm provider: error (code=insufficient_quota)"},
		{"error-nested-code", `data: {"type":"error","error":{"code":"invalid_request_error","message":"ZZPRIVATE_PAYLOAD"}}` + "\n\n", "llm provider: error (code=invalid_request_error)"},
		{"unknown-code-hidden", `data: {"type":"response.failed","response":{"error":{"code":"ZZPRIVATE_PAYLOAD","message":"ZZPRIVATE_PAYLOAD"}}}` + "\n\n", "llm provider: response.failed"},
		{"malformed-response", `data: {"type":"response.failed","response":"ZZPRIVATE_PAYLOAD"}` + "\n\n", "llm provider: response.failed"},
		{"malformed-error-list", `data: {"type":"response.failed","response":{"error":[{"code":"ZZPRIVATE_PAYLOAD"}]}}` + "\n\n", "llm provider: response.failed"},
		{"error-code-bool", `data: {"type":"error","code":true}` + "\n\n", "llm provider: error"},
		{"incomplete-reason-bad", `data: {"type":"response.incomplete","response":{"incomplete_details":{"reason":"ZZPRIVATE_PAYLOAD"}}}` + "\n\n", "llm provider: response.incomplete"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, e := mk(tc.frame)
			_, err := runStream(t, e, s, f.name, f.model, "None")
			if err == nil || err.Type != types.ErrRuntime {
				t.Fatalf("expected runtime error, got %+v", err)
			}
			if !strings.Contains(err.Message, tc.want) {
				t.Fatalf("error %q want %q", err.Message, tc.want)
			}
			if strings.Contains(err.Message, "ZZPRIVATE_PAYLOAD") {
				t.Fatalf("payload echoed: %q", err.Message)
			}
		})
	}
}

// debug_errors opt-in: bounded escaped provider message in diagnostics,
// default off, never sent upstream, secrets still redacted.
func TestOpenAIStreamDebugErrors(t *testing.T) {
	f := providerFixtures()[0]
	mk := func(frames string, obs capability.Observer, bodyChecks func(t *testing.T, b string)) (*engine.Session, *engine.Engine) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if bodyChecks != nil {
				b, _ := io.ReadAll(r.Body)
				bodyChecks(t, string(b))
			}
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, frames)
		}))
		t.Cleanup(srv.Close)
		s, e := llmSession(t, nil, obs, []providerFixture{f}, map[string]*httptest.Server{f.name: srv})
		return s, e
	}
	failFrame := `data: {"type":"response.failed","response":{"error":{"code":"ZZPRIVATE_CODE","message":"ZZDIAGNOSTIC_MESSAGE","prompt":"ZZUNSELECTED_PAYLOAD"}}}` + "\n\n"

	// default off + explicit False: still hidden
	for _, opts := range []string{"None", `{"debug_errors": False}`} {
		s, e := mk(failFrame, nil, nil)
		_, err := runStream(t, e, s, f.name, f.model, opts)
		if err == nil || err.Type != types.ErrRuntime {
			t.Fatalf("%s: %+v", opts, err)
		}
		if err.Message != "fail: llm provider: response.failed" {
			t.Fatalf("%s: %q", opts, err.Message)
		}
	}
	// True: bounded escaped message, code still suppressed
	s, e := mk(failFrame, nil, nil)
	_, err := runStream(t, e, s, f.name, f.model, `{"debug_errors": True}`)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Message, `: "ZZDIAGNOSTIC_MESSAGE"`) {
		t.Fatalf("message missing: %q", err.Message)
	}
	if strings.Contains(err.Message, "ZZPRIVATE_CODE") || strings.Contains(err.Message, "ZZUNSELECTED_PAYLOAD") {
		t.Fatalf("payload leaked: %q", err.Message)
	}
	// nested error.message / top-level message priority / control chars
	s, e = mk(`data: {"type":"error","error":{"code":"ZZX","message":"nested hi"}}`+"\n\n", nil, nil)
	_, err = runStream(t, e, s, f.name, f.model, `{"debug_errors": True}`)
	if err == nil || !strings.Contains(err.Message, `"nested hi"`) {
		t.Fatalf("nested: %v", err)
	}
	s, e = mk(`data: {"type":"error","message":"top\nraw","error":{"message":"ignored"}}`+"\n\n", nil, nil)
	_, err = runStream(t, e, s, f.name, f.model, `{"debug_errors": True}`)
	if err == nil || !strings.Contains(err.Message, `top\nraw`) {
		t.Fatalf("top/escape: %v", err)
	}
	if strings.Contains(err.Message, `"ignored"`) {
		t.Fatalf("wrong field: %q", err.Message)
	}
	// non-string message ignored
	s, e = mk(`data: {"type":"error","message":["x"]}`+"\n\n", nil, nil)
	_, err = runStream(t, e, s, f.name, f.model, `{"debug_errors": True}`)
	if err == nil || err.Message != "fail: llm provider: error" {
		t.Fatalf("coerced: %v", err)
	}
	// 513+ unicode chars bounded, escaped, tail cut
	long := strings.Repeat("界", 600) + "ZZTAIL"
	s, e = mk(`data: {"type":"error","error":{"message":`+strconv.Quote(long)+`}}`+"\n\n", nil, nil)
	_, err = runStream(t, e, s, f.name, f.model, `{"debug_errors": True}`)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Message, "[truncated]") || strings.Contains(err.Message, "ZZTAIL") {
		t.Fatalf("bound wrong: %.80s", err.Message)
	}
	if !utf8.ValidString(err.Message) {
		t.Fatal("invalid UTF-8")
	}
	// request body never carries debug_errors; stream stays true
	s, e = mk(failFrame, nil, func(t *testing.T, b string) {
		var m map[string]any
		if err := json.Unmarshal([]byte(b), &m); err != nil {
			t.Fatal(err)
		}
		if _, ok := m["debug_errors"]; ok {
			t.Errorf("debug_errors leaked upstream: %s", b)
		}
		if m["stream"] != true {
			t.Errorf("stream flag lost: %s", b)
		}
	})
	_, _ = runStream(t, e, s, f.name, f.model, `{"debug_errors": True}`)
	// invalid type fails BEFORE any network call
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { atomic.AddInt32(&hits, 1) }))
	defer srv.Close()
	s, e = llmSession(t, nil, nil, []providerFixture{f}, map[string]*httptest.Server{f.name: srv})
	for _, bad := range []string{`{"debug_errors": "yes"}`, `{"debug_errors": 1}`} {
		_, err := runStream(t, e, s, f.name, f.model, bad)
		if err == nil || err.Type != types.ErrRuntime || !strings.Contains(err.Message, "debug_errors must be a bool") {
			t.Fatalf("%s: %+v", bad, err)
		}
	}
	if atomic.LoadInt32(&hits) != 0 {
		t.Fatalf("network hit for invalid option (%d)", hits)
	}
	// secret redaction: provider message containing the api key is
	// redacted from the engine error AND audit, even under debug opt-in
	o := &obs{}
	secretMsg := "key test-api-key exposed"
	s, e = mk(`data: {"type":"error","error":{"message":`+strconv.Quote(secretMsg)+`}}`+"\n\n", o, nil)
	_, err = runStream(t, e, s, f.name, f.model, `{"debug_errors": True}`)
	if err == nil {
		t.Fatal("expected error")
	}
	blob, _ := json.Marshal(o.recs)
	if strings.Contains(err.Message, "test-api-key") || strings.Contains(string(blob), "test-api-key") {
		t.Fatalf("secret leaked: %q / audit", err.Message)
	}
}

// Non-2xx diagnostics: default off = bare status, debug=True selects
// only error.message (redacted + bounded) after a 16KiB-capped read.
func TestOpenAIStreamHTTPDiagnostics(t *testing.T) {
	f := providerFixtures()[0]
	mk := func(status int, body string, obs capability.Observer) (*engine.Session, *engine.Engine) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			fmt.Fprint(w, body)
		}))
		t.Cleanup(srv.Close)
		s, e := llmSession(t, nil, obs, []providerFixture{f}, map[string]*httptest.Server{f.name: srv})
		return s, e
	}
	bad := `{"error":{"message":"Unsupported parameter: demo","code":"ZZCODE"},"prompt":"ZZUNSELECTED"}`
	// default: status only
	s, e := mk(400, bad, nil)
	_, err := runStream(t, e, s, f.name, f.model, "None")
	if err == nil || err.Message != "fail: llm provider: status 400" {
		t.Fatalf("default: %v", err)
	}
	// debug: selected message only
	s, e = mk(400, bad, nil)
	_, err = runStream(t, e, s, f.name, f.model, `{"debug_errors": True}`)
	if err == nil || !strings.Contains(err.Message, `: "Unsupported parameter: demo"`) {
		t.Fatalf("debug: %v", err)
	}
	if strings.Contains(err.Message, "ZZCODE") || strings.Contains(err.Message, "ZZUNSELECTED") {
		t.Fatalf("payload leaked: %q", err.Message)
	}
	for _, st := range []int{401, 429} {
		s, e = mk(st, bad, nil)
		_, err = runStream(t, e, s, f.name, f.model, `{"debug_errors": True}`)
		if err == nil || !strings.Contains(err.Message, "Unsupported parameter") {
			t.Fatalf("%d: %v", st, err)
		}
	}
	// malformed bodies stay bare status
	for _, b := range []string{`{bad json`, `<html>err</html>`, `{"error":null}`, `{"error":{"message":true}}`, `{"error":{"message":[1]}}`} {
		s, e = mk(400, b, nil)
		_, err = runStream(t, e, s, f.name, f.model, `{"debug_errors": True}`)
		if err == nil || err.Message != "fail: llm provider: status 400" {
			t.Fatalf("%q: %v", b, err)
		}
	}
	// oversized body -> bare status
	s, e = mk(400, strings.Repeat("a", 20<<10), nil)
	_, err = runStream(t, e, s, f.name, f.model, `{"debug_errors": True}`)
	if err == nil || err.Message != "fail: llm provider: status 400" {
		t.Fatalf("oversize: %v", err)
	}
	// secret in message is redacted even under debug (literal + escaped)
	o := &obs{}
	escTok := strings.Builder{}
	for _, r := range "test-api-key" {
		fmt.Fprintf(&escTok, "\\u%04x", r)
	}
	s, e = mk(400, `{"error":{"message":"`+escTok.String()+`"}}`, o)
	_, err = runStream(t, e, s, f.name, f.model, `{"debug_errors": True}`)
	if err == nil || strings.Contains(err.Message, "test-api-key") {
		t.Fatalf("escaped secret: %q", err)
	}
	blob, _ := json.Marshal(o.recs)
	if strings.Contains(string(blob), "test-api-key") {
		t.Fatal("secret in audit")
	}
}

// Official-doc conformance: exact request body for the OpenAI
// Responses stream call — model/input/stream only, no extra keys.
func TestOpenAIStreamOfficialRequest(t *testing.T) {
	f := providerFixtures()[0]
	f.model = "gpt-6-luna"
	cases := []struct {
		name  string
		input string
		opts  string
		want  any
	}{
		{"string-input/no-opts", `"halo"`, "None", "halo"},
		{"string-input/debug", `"halo"`, `{"debug_errors": True}`, "halo"},
		{"list-input/no-opts", `[{"role": "user", "content": "halo"}]`, "None",
			[]any{map[string]any{"role": "user", "content": "halo"}}},
		{"list-input/debug", `[{"role": "user", "content": "halo"}]`, `{"debug_errors": True}`,
			[]any{map[string]any{"role": "user", "content": "halo"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotBody []byte
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/responses" || r.Method != http.MethodPost {
					t.Errorf("%s %s", r.Method, r.URL.Path)
				}
				if ct := r.Header.Get("Content-Type"); ct != "application/json" {
					t.Errorf("Content-Type %q", ct)
				}
				gotBody, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, f.frames)
			}))
			defer srv.Close()
			s, e := llmSession(t, nil, nil, []providerFixture{f}, map[string]*httptest.Server{f.name: srv})
			code := fmt.Sprintf(`s = ext.llm.stream(provider="openai", model="gpt-6-luna", messages=%s, options=%s)
for part in s:
    pass`, tc.input, tc.opts)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			res := e.Exec(ctx, s, "x", code, 8*time.Second, 1<<20, s.Out.String)
			if res.Error != nil {
				t.Fatalf("exec: %+v", res.Error)
			}
			var m map[string]any
			if err := json.Unmarshal(gotBody, &m); err != nil {
				t.Fatal(err)
			}
			want := map[string]any{"model": "gpt-6-luna", "input": tc.want, "stream": true}
			if len(m) != len(want) {
				t.Fatalf("extra keys: %s", gotBody)
			}
			for k, v := range want {
				gb, _ := json.Marshal(m[k])
				vb, _ := json.Marshal(v)
				if string(gb) != string(vb) {
					t.Fatalf("%s: %s vs %s", k, gb, vb)
				}
			}
		})
	}
}

// TestProviderStructuredOptionsReachWire verifies ext.llm.structured
// forwards options (e.g. max_tokens) to the provider request, so
// callers can bound output length and stay inside the exec deadline.
// The provider protocol previously ignored options on structured()
// entirely, which made large structured generations time out.
func TestProviderStructuredOptionsReachWire(t *testing.T) {
	for _, tc := range []struct {
		name     string
		source   string
		optKey   string
		response string
	}{
		{
			name: "openai", source: "contrib/llm-openai@0.1.0",
			optKey:   "max_output_tokens",
			response: `{"output":[{"type":"message","content":[{"type":"output_text","text":"{\"ok\": true}"}]}],"usage":{"input_tokens":2,"output_tokens":1}}`,
		},
		{
			name: "compat", source: "contrib/llm-openai-compat@0.1.0",
			optKey:   "max_tokens",
			response: `{"choices":[{"message":{"content":"{\"ok\": true}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				if !strings.Contains(string(raw), `"`+tc.optKey+`":42`) {
					t.Errorf("%s body missing %s=42: %s", tc.name, tc.optKey, raw)
				}
				if !strings.Contains(string(raw), `"model":"m1"`) {
					t.Errorf("%s body missing model: %s", tc.name, raw)
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, tc.response)
			}))
			defer srv.Close()
			f := providerFixture{
				name: tc.name, source: tc.source, model: "m1",
				cfg: func(srv *httptest.Server) map[string]any { return map[string]any{"base_url": srv.URL, "api_key": ""} },
			}
			s, e := llmSession(t, nil, nil, []providerFixture{f}, map[string]*httptest.Server{f.name: srv})
			code := `res = ext.llm.structured("` + tc.name + `", [{"role": "user", "content": "hi"}], {"type": "object", "properties": {"ok": {"type": "boolean"}}}, model = "m1", options = {"max_tokens": 42})
print(res)`
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			res := e.Exec(ctx, s, "x", code, 8*time.Second, 1<<20, s.Out.String)
			if res.Error != nil {
				t.Fatalf("exec: %+v", res.Error)
			}
			if out := strings.TrimSpace(s.Out.String()); !strings.Contains(out, `"ok": True`) {
				t.Fatalf("structured result %q", out)
			}
		})
	}
}
