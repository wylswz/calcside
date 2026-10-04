package engine

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.starlark.net/starlark"

	"calcside/internal/capability"
	capfs "calcside/internal/capability/fs"
	capio "calcside/internal/capability/io"
	capnet "calcside/internal/capability/net"
)

type opaqueCompletionValue struct{ starlark.Value }

func (opaqueCompletionValue) String() string      { panic("must not render values") }
func (opaqueCompletionValue) AttrNames() []string { panic("must not inspect custom attributes") }
func (opaqueCompletionValue) Attr(string) (starlark.Value, error) {
	panic("must not access custom attributes")
}

func TestCompletionsDoNotEvaluateValues(t *testing.T) {
	s, _ := newSession(t, nil, 1024)
	s.Globals["opaque"] = opaqueCompletionValue{starlark.String("private")}
	out, err := s.Completions(context.Background(), capability.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	for _, symbol := range out.Symbols {
		if symbol.Name == "opaque" {
			if symbol.Detail != "string" {
				t.Fatalf("unexpected type: %v", symbol)
			}
			return
		}
	}
	t.Fatal("missing variable")
}

func TestCompletionsBounded(t *testing.T) {
	s, _ := newSession(t, nil, 1024)
	for i := 0; i < maxCompletionSymbols+10; i++ {
		s.Globals[fmt.Sprintf("v%04d", i)] = starlark.None
	}
	s.Globals[strings.Repeat("z", maxCompletionName+1)] = starlark.None
	out, err := s.Completions(context.Background(), capability.NewRegistry())
	if err != nil || !out.Truncated || len(out.Symbols) > maxCompletionSymbols {
		t.Fatalf("unbounded: %v %+v", err, out)
	}
	for _, symbol := range out.Symbols {
		if len(symbol.Name) > maxCompletionName {
			t.Fatal("unbounded symbol name")
		}
	}
}

func TestCompletionsCancelWhileExecuting(t *testing.T) {
	s, _ := newSession(t, nil, 1024)
	s.ExecMu.Lock()
	defer s.ExecMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := s.Completions(ctx, capability.NewRegistry()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline, got %v", err)
	}
}

func TestCompletionFunctionParams(t *testing.T) {
	s, buf := newSession(t, nil, 1024)
	r := New(1).Exec(context.Background(), s, "e", "def f(a, b=1, *args, flag, c=2, **kwargs):\n    pass\n", 0, 1000, buf.String)
	if r.Error != nil {
		t.Fatal(r.Error)
	}
	got := strings.Join(functionParams(s.Globals["f"].(*starlark.Function)), ", ")
	if got != "a, b?, *args, flag, c?, **kwargs" {
		t.Fatalf("signature: %s", got)
	}
}

type completionSchemaHook struct {
	docs map[string]capability.OpInfo
	seen map[string]bool
}

func (*completionSchemaHook) Name() string { return "completion-schema" }

func (h *completionSchemaHook) Before(_ context.Context, call *capability.Call) error {
	name := string(call.Capability) + "." + string(call.Op)
	return checkCompletionFields(name+" args", h.docs[name].PolicyArgs, call.Args)
}

func (h *completionSchemaHook) After(_ context.Context, call *capability.Call, result *capability.Result) error {
	name := string(call.Capability) + "." + string(call.Op)
	h.seen[name] = true
	return checkCompletionFields(name+" result", h.docs[name].ResultMeta, result.Meta)
}

func checkCompletionFields(name string, expected map[string]string, values map[string]any) error {
	if len(values) != len(expected) {
		return fmt.Errorf("%s fields: expected %v, got %v", name, expected, values)
	}
	for key, value := range values {
		kind := reflect.TypeOf(value).Kind()
		actual := ""
		switch kind {
		case reflect.String:
			actual = "string"
		case reflect.Bool:
			actual = "boolean"
		case reflect.Int, reflect.Int64, reflect.Uint64, reflect.Float64:
			actual = "number"
		case reflect.Slice:
			if _, ok := value.([]string); ok {
				actual = "array<string>"
			}
		}
		if actual != expected[key] {
			return fmt.Errorf("%s.%s: expected %s, got %s", name, key, expected[key], actual)
		}
	}
	return nil
}

func TestCompletionSchemasMatchGatedOperations(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer ts.Close()
	reg := capability.NewRegistry()
	limits := capability.ServerLimits{MaxFSQuotaBytes: 256 << 20, NetAllowPrivate: true}
	caps := map[string]any{}
	hook := &completionSchemaHook{docs: map[string]capability.OpInfo{}, seen: map[string]bool{}}
	for _, f := range []capability.Factory{capfs.Factory(), capio.Factory(), capnet.Factory()} {
		reg.Register(f)
		cfg, err := f.Validate(nil, limits)
		if err != nil {
			t.Fatal(err)
		}
		caps[string(f.Name())] = cfg
		for _, op := range f.Ops() {
			hook.docs[string(f.Name())+"."+string(op.Name)] = op
		}
	}
	hook.docs["io.print"] = hook.docs["io.println"]
	s, err := NewSession(SessionDeps{Registry: reg, Limits: limits, Hooks: []capability.Hook{hook}}, SessionCreation{Capabilities: caps, MaxOutputBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	code := fmt.Sprintf(`fs.mkdir("dir")
fs.write("dir/a", "hello")
fs.append("dir/a", " world")
fs.read("dir/a")
fs.stat("dir/a")
fs.exists("dir/a")
fs.list("dir")
fs.walk("dir")
fs.delete("dir", recursive=True)
io.println("hello")
print("world")
net.get(%q)
net.post(%q, "body")
net.request("GET", %q)
`, ts.URL, ts.URL, ts.URL)
	res := New(1).Exec(context.Background(), s, "schema", code, time.Second, 10000, s.Out.String)
	if res.Error != nil {
		t.Fatal(res.Error)
	}
	for name := range hook.docs {
		if !hook.seen[name] {
			t.Errorf("operation not exercised: %s", name)
		}
	}
}
