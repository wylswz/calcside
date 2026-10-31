package application

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"calcside/cmd/calcside/internal/audit"
	"calcside/cmd/calcside/internal/service"
	"calcside/cmd/calcside/internal/service/access"
	"calcside/cmd/calcside/internal/service/sandbox"
	"calcside/internal/capability"
	capfs "calcside/internal/capability/fs"
	"calcside/internal/engine"
	"calcside/internal/runtime"
	"calcside/internal/runtime/instance"
	"calcside/internal/runtime/remote"
	"calcside/internal/store"
	"calcside/internal/store/storetest"
)

var lcTestKey = []byte("lc-test-shared-key-0123456789abcdef")

// gateRT fails requests to a path suffix — a real transport error, the
// only prepare failure the contract cannot classify.
type gateRT struct {
	base   http.RoundTripper
	suffix string
	fail   atomic.Bool
}

func (g *gateRT) RoundTrip(r *http.Request) (*http.Response, error) {
	if g.fail.Load() && strings.HasSuffix(r.URL.Path, g.suffix) {
		return nil, errors.New("gate: transport failure")
	}
	return g.base.RoundTrip(r)
}

// lifecycleHook observes what the service asked the real wire client
// for. It is an observer on the production hook, never a replacement
// runtime or audit sink.
type lifecycleHook struct {
	gate   *gateRT
	events []string
}

func (r *lifecycleHook) Create(context.Context, *runtime.CreateRequest) {
	r.events = append(r.events, "create")
}

func (r *lifecycleHook) Prepare(context.Context, *runtime.PrepareRequest) {
	r.events = append(r.events, "prepare")
}

func (r *lifecycleHook) Invoke(context.Context, *runtime.InvokeRequest) {
	r.events = append(r.events, "invoke")
}

func (r *lifecycleHook) Delete(context.Context, *runtime.DeleteRequest) {
	r.events = append(r.events, "delete")
}

func lifecycleService(t *testing.T) (*Service, store.Store, *lifecycleHook, service.Actor) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, "sqlite", storetest.SQLite(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ra := audit.NewRecorder(st)
	t.Cleanup(ra.Close)
	reg := capability.NewRegistry()
	reg.Register(capfs.Factory())
	limits := capability.ServerLimits{DefaultTTL: time.Minute, MaxTTL: time.Hour, MaxFSQuotaBytes: 256 << 20}
	mgr := instance.New(instance.Options{
		Engine: engine.New(8), Registry: reg, Limits: limits,
		EvalTimeout: time.Second, ReapInterval: time.Hour,
	})
	t.Cleanup(mgr.StopReaper)
	srv := httptest.NewServer(remote.NewHandler(mgr, lcTestKey))
	t.Cleanup(srv.Close)
	gate := &gateRT{base: http.DefaultTransport, suffix: "/prepare"}
	direct, err := remote.NewDirect(srv.URL, &http.Client{Transport: gate}, lcTestKey, "lifecycle-test")
	if err != nil {
		t.Fatal(err)
	}
	rt := &lifecycleHook{gate: gate}
	direct.SetLifecycleHook(rt)
	ac := access.New(st)
	sb := sandbox.New(sandbox.Options{
		Store: st, Runtime: direct, Registry: reg, Limits: limits, Secrets: nopSecrets{}, Access: ac,
	})
	s, err := New(Options{Store: st, Runtime: direct, Sandbox: sb, Access: ac, Audit: ra, MaxPerUser: 4})
	if err != nil {
		t.Fatal(err)
	}
	a := ownerActor(st)
	a.Email = "o@x.com"
	return s, st, rt, a
}

func TestRealDraftLifecycle(t *testing.T) {
	s, st, rt, actor := lifecycleService(t)
	ctx := context.Background()
	code := "def main(input): return input"
	v, err := s.Create(ctx, actor, CreateRequest{Code: &code})
	if err != nil {
		t.Fatal(err)
	}
	id := v.Application.ID
	input := json.RawMessage(`{"arbitrary":{"n":9007199254740993}}`)
	out, err := s.Test(ctx, actor, id, 1, input)
	if err != nil || out.Result.Error != nil || out.OutputJSON != string(input) {
		t.Fatalf("precise dynamic object: result=%+v err=%v", out, err)
	}
	if !reflect.DeepEqual(rt.events, []string{"prepare", "invoke", "delete"}) {
		t.Fatalf("test lifecycle: %v", rt.events)
	}
	app, err := st.GetApplication(ctx, id)
	if err != nil || app.PersistentInstanceID != "" || app.PublishedVersion != 0 {
		t.Fatalf("test unexpectedly published: %+v, %v", app, err)
	}
	for _, bad := range []string{`[]`, `42`, `null`} {
		out, err := s.Test(ctx, actor, id, 1, json.RawMessage(bad))
		if err != nil || out.Result.Error == nil || out.Result.Error.Code != "invalid_input" || out.OutputJSON != "" {
			t.Fatalf("default object accepted %s: %+v, %v", bad, out, err)
		}
	}
	rt.events = nil
	pub, err := s.Publish(ctx, actor, id, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rt.events, []string{"prepare", "delete"}) {
		t.Fatalf("publish executed main or leaked temporary replica: %v", rt.events)
	}
	rel, err := st.GetServingRelease(ctx, actor.WorkspaceID, pub.ReleaseID)
	if err != nil || !strings.Contains(string(rel.Program.InputSchema), `"type":"object"`) ||
		!strings.Contains(string(rel.Program.OutputSchema), `"type":"object"`) {
		t.Fatalf("release default contracts: %+v, %v", rel, err)
	}
	updated, err := s.Update(ctx, actor, id, UpdateRequest{Version: 1, Code: code + "\n"})
	if err != nil || updated.Application.Version != 2 {
		t.Fatalf("update: %+v, %v", updated, err)
	}
	rt.events = nil
	if _, err := s.Publish(ctx, actor, id, 1); err == nil {
		t.Fatal("old published version bypassed current draft CAS")
	}
	if len(rt.events) != 0 {
		t.Fatalf("stale publish touched runtime: %v", rt.events)
	}
}

func TestInvalidProgramsCannotPublish(t *testing.T) {
	for _, tc := range []struct {
		name, code, spec string
	}{
		{"syntax", "def main(", `{}`},
		{"initialization effect", "fs.write(\"x\", \"not allowed\")\ndef main(input): return input", `{"capabilities":{"fs":{}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, st, rt, actor := lifecycleService(t)
			ctx := context.Background()
			v, err := s.Create(ctx, actor, CreateRequest{Code: &tc.code, Spec: json.RawMessage(tc.spec)})
			if err != nil {
				t.Fatal(err)
			}
			candidate, _, err := s.release(ctx, actor, v.Application)
			if err != nil {
				t.Fatal(err)
			}
			result, err := s.Test(ctx, actor, v.Application.ID, 1, json.RawMessage(`{}`))
			if err != nil || result.Result.Error == nil {
				t.Fatalf("invalid draft not reported: %+v, %v", result, err)
			}
			rt.events = nil
			if _, err := s.Publish(ctx, actor, v.Application.ID, 1); err == nil {
				t.Fatal("invalid program was published")
			}
			if !reflect.DeepEqual(rt.events, []string{"prepare", "delete"}) {
				t.Fatalf("failed prepare lifecycle: %v", rt.events)
			}
			app, err := st.GetApplication(ctx, v.Application.ID)
			if err != nil || app.PersistentInstanceID != "" {
				t.Fatalf("failed publication persisted pointer: %+v, %v", app, err)
			}
			if _, err := st.GetServingRelease(ctx, actor.WorkspaceID, candidate.ID); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("failed publication persisted release: %v", err)
			}
		})
	}
}

func TestUncertainPrepareIsCleaned(t *testing.T) {
	s, _, rt, actor := lifecycleService(t)
	ctx := context.Background()
	v, err := s.Create(ctx, actor, CreateRequest{})
	if err != nil {
		t.Fatal(err)
	}
	rt.gate.fail.Store(true)
	if _, err := s.Test(ctx, actor, v.Application.ID, 1, json.RawMessage(`{}`)); err == nil ||
		strings.Contains(err.Error(), "gate") {
		t.Fatalf("transport error was lost or leaked: %v", err)
	}
	// A transport-dead prepare has no response, but cleanup still runs.
	if !reflect.DeepEqual(rt.events, []string{"prepare", "delete"}) {
		t.Fatalf("uncertain preparation escaped cleanup: %v", rt.events)
	}
}
