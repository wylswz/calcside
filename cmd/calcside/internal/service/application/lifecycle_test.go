package application

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"calcside/cmd/calcside/internal/service"
	"calcside/cmd/calcside/internal/service/access"
	"calcside/cmd/calcside/internal/service/sandbox"
	"calcside/internal/capability"
	capfs "calcside/internal/capability/fs"
	"calcside/internal/engine"
	"calcside/internal/instance"
	"calcside/internal/runtime"
	"calcside/internal/store"
	"calcside/internal/store/storetest"
)

type lifecycleRuntime struct {
	runtime.Runtime
	events      []string
	audits      int
	prepareFail error
	owner       runtime.Owner
}

func (r *lifecycleRuntime) Prepare(ctx context.Context, req *runtime.PrepareRequest) (*runtime.PrepareResponse, error) {
	r.events = append(r.events, "prepare")
	r.owner = req.Owner
	if r.prepareFail != nil {
		return &runtime.PrepareResponse{Epoch: req.Epoch}, r.prepareFail
	}
	return r.Runtime.Prepare(ctx, req)
}

func (r *lifecycleRuntime) Invoke(ctx context.Context, req *runtime.InvokeRequest) (*runtime.InvokeResponse, error) {
	r.events = append(r.events, "invoke")
	return r.Runtime.Invoke(ctx, req)
}

func (r *lifecycleRuntime) Delete(ctx context.Context, req *runtime.DeleteRequest) (*runtime.DeleteResponse, error) {
	r.events = append(r.events, "delete")
	return r.Runtime.Delete(ctx, req)
}

func (r *lifecycleRuntime) Record(runtime.AuditBatch) { r.audits++ }

func lifecycleService(t *testing.T) (*Service, store.Store, *lifecycleRuntime, service.Actor) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, "sqlite", storetest.SQLite(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	reg := capability.NewRegistry()
	reg.Register(capfs.Factory())
	limits := capability.ServerLimits{DefaultTTL: time.Minute, MaxTTL: time.Hour, MaxFSQuotaBytes: 256 << 20}
	mgr := instance.New(instance.Options{
		Engine: engine.New(8), Registry: reg, Limits: limits,
		EvalTimeout: time.Second, ReapInterval: time.Hour,
	})
	t.Cleanup(mgr.StopReaper)
	rt := &lifecycleRuntime{Runtime: mgr}
	ac := access.New(st)
	sb := sandbox.New(sandbox.Options{
		Store: st, Runtime: rt, Registry: reg, Limits: limits, Secrets: nopSecrets{}, Access: ac,
	})
	s, err := New(Options{Store: st, Runtime: rt, Sandbox: sb, Access: ac, Audit: rt, MaxPerUser: 4})
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
	if !reflect.DeepEqual(rt.events, []string{"prepare", "invoke", "delete"}) || rt.audits != 2 {
		t.Fatalf("test lifecycle: %v, audits=%d", rt.events, rt.audits)
	}
	if rt.owner.Email != actor.Email {
		t.Fatal("prepare lost owner email")
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
	rt.events, rt.audits = nil, 0
	pub, err := s.Publish(ctx, actor, id, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rt.events, []string{"prepare", "delete"}) || rt.audits != 1 {
		t.Fatalf("publish executed main or leaked temporary replica: %v, %d", rt.events, rt.audits)
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
			rt.events, rt.audits = nil, 0
			if _, err := s.Publish(ctx, actor, v.Application.ID, 1); err == nil {
				t.Fatal("invalid program was published")
			}
			if !reflect.DeepEqual(rt.events, []string{"prepare", "delete"}) || rt.audits != 1 {
				t.Fatalf("failed prepare lifecycle: %v, %d", rt.events, rt.audits)
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

func TestUncertainPrepareIsAuditedAndCleaned(t *testing.T) {
	s, _, rt, actor := lifecycleService(t)
	ctx := context.Background()
	v, err := s.Create(ctx, actor, CreateRequest{})
	if err != nil {
		t.Fatal(err)
	}
	rt.prepareFail = errors.New("sensitive transport details")
	if _, err := s.Test(ctx, actor, v.Application.ID, 1, json.RawMessage(`{}`)); err == nil ||
		strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("transport error was lost or leaked: %v", err)
	}
	if !reflect.DeepEqual(rt.events, []string{"prepare", "delete"}) || rt.audits != 1 {
		t.Fatalf("uncertain preparation escaped cleanup/audit: %v, %d", rt.events, rt.audits)
	}
}
