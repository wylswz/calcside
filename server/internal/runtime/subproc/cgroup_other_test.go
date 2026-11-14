//go:build !linux

package subproc

import (
	"context"
	"testing"
	"time"

	"calcside/internal/runtime"
)

// On non-Linux hosts the supervisor warns and runs uncapped — a
// memory-max request must not fail startup or spawn fallback threads.
func TestMemoryMaxUncappedNotLinux(t *testing.T) {
	s := newSupervisor(t, func(o *Options) { o.MemoryMax = 64 << 20 })
	if s.cg != nil {
		t.Fatal("cgroup tree created on a non-Linux host")
	}
	resp, err := s.Create(context.Background(), &runtime.CreateRequest{
		InstanceID: "inst_cg", Spec: []byte(`{"capabilities":{"io":{}}}`),
		ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.Exec(context.Background(), &runtime.ExecRequest{
		InstanceID: "inst_cg", Code: "print(42)",
	})
	if err != nil || out == nil || out.Result.Output != "42\n" {
		t.Fatalf("uncapped exec: %v %+v", err, out)
	}
	_ = resp
}
