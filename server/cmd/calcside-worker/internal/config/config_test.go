package config

import (
	"strings"
	"testing"
	"time"
)

func TestWorkerFlags(t *testing.T) {
	t.Setenv("CALCSIDE_WORKER_SHARED_KEY", "env-key")
	t.Setenv("CALCSIDE_API_ADDR", "http://api:8787")
	t.Setenv("CALCSIDE_WORKER_ADVERTISE_ADDR", "worker:8090")
	cfg, err := ParseWorker([]string{"--node-id=w1", "--listen=127.0.0.1:8099", "--max-instances-per-node=12", "--net-allow-cidrs=198.18.0.0/15", "--max-exec-timeout=2m"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.NodeID != "w1" || cfg.Addr != "127.0.0.1:8099" || cfg.SharedKey != "env-key" || cfg.APIAddr != "http://api:8787" || cfg.MaxInstances != 12 {
		t.Fatal("worker configuration not preserved")
	}
	if cfg.AdvertiseAddr != "worker:8090" {
		t.Fatalf("advertise addr: %q", cfg.AdvertiseAddr)
	}
	limits := cfg.ExecLimits()
	if limits.MaxExecTimeout != 2*time.Minute || len(limits.NetAllowCIDRs) != 1 || limits.NetAllowCIDRs[0].String() != "198.18.0.0/15" {
		t.Fatal("execution settings not preserved")
	}
	cfg, err = ParseWorker([]string{"--shared-key=flag-key", "--api-addr=http://other:8787", "--advertise-addr=[::1]:8090"})
	if err != nil || cfg.SharedKey != "flag-key" || cfg.APIAddr != "http://other:8787" {
		t.Fatal("flags did not override environment")
	}
	if cfg.AdvertiseAddr != "[::1]:8090" {
		t.Fatalf("advertise addr not canonicalized: %q", cfg.AdvertiseAddr)
	}
}

func TestWorkerRejectsAPIFlags(t *testing.T) {
	t.Setenv("CALCSIDE_API_ADDR", "")
	t.Setenv("CALCSIDE_WORKER_SHARED_KEY", "")
	t.Setenv("CALCSIDE_WORKER_ADVERTISE_ADDR", "")
	for _, flag := range []string{"--dev", "--dsn=api.db", "--admin-username=admin", "--google-client-id=client", "--instance-isolation=inproc", "--instance-isolation=process", "--ext-local-roots=/tmp/x", "--ext-allow-sources=x", "--net-allow-cidrs=invalid"} {
		if _, err := ParseWorker([]string{"--api-addr=http://127.0.0.1:1", "--shared-key=k", "--advertise-addr=127.0.0.1:8090", flag}); err == nil || !strings.Contains(err.Error(), strings.Split(strings.TrimPrefix(flag, "--"), "=")[0]) {
			t.Errorf("unexpectedly accepted %q", flag)
		}
	}
	// required credentials: each missing alone must fail
	if _, err := ParseWorker([]string{"--shared-key=k", "--advertise-addr=127.0.0.1:8090"}); err == nil {
		t.Error("missing --api-addr accepted")
	}
	if _, err := ParseWorker([]string{"--api-addr=http://x:1", "--advertise-addr=127.0.0.1:8090"}); err == nil {
		t.Error("missing --shared-key accepted")
	}
	if _, err := ParseWorker([]string{"--api-addr=http://x:1", "--shared-key=k"}); err == nil {
		t.Error("missing --advertise-addr accepted")
	}
	if _, err := ParseWorker([]string{"--api-addr=http://x:1", "--shared-key=k", "--advertise-addr=http://evil:1"}); err == nil {
		t.Error("invalid --advertise-addr accepted")
	}
}
