package node

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"calcside/internal/capability"
	"calcside/internal/runtime"
	"calcside/internal/types"
)

func TestBuild(t *testing.T) {
	nd := Build(Config{
		Limits:             capability.ServerLimits{DefaultTTL: time.Minute, MaxTTL: time.Hour, MaxExecTimeout: time.Minute},
		MaxConcurrentExecs: 2, MaxInstances: 1, ReapInterval: time.Hour,
	})
	t.Cleanup(nd.Close)
	if nd.Engine == nil || nd.Manager == nil {
		t.Fatalf("incomplete node: %+v", nd)
	}
	if got, want := nd.Registry.Names(), []types.CapabilityName{types.CapFS, types.CapNet, types.CapIO, types.CapExt}; !reflect.DeepEqual(got, want) {
		t.Fatalf("capabilities = %v, want %v", got, want)
	}
	nd.Manager.StartReaper()
	owner := runtime.Owner{UserID: "usr_test"}
	_, err := nd.Manager.Create(t.Context(), &runtime.CreateRequest{
		InstanceID: "ins_test", Owner: owner, Spec: []byte(`{"capabilities":{}}`), ExpiresAt: time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = nd.Manager.Create(t.Context(), &runtime.CreateRequest{
		InstanceID: "ins_other", Owner: owner, Spec: []byte(`{"capabilities":{}}`), ExpiresAt: time.Now().Add(time.Minute),
	})
	if !errors.Is(err, runtime.ErrTooMany) {
		t.Fatalf("node instance limit not preserved: %v", err)
	}
	out, err := nd.Manager.Exec(t.Context(), &runtime.ExecRequest{
		InstanceID: "ins_test", Owner: owner, ExecID: "exe_test", Code: "print(42)",
	})
	if err != nil || out.Result.Output != "42\n" {
		t.Fatalf("exec: %v, %+v", err, out)
	}
}
