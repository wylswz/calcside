package config

import (
	"testing"
	"time"

	common "calcside/internal/config"
)

func TestWorkerFlags(t *testing.T) {
	t.Setenv("CALCSIDE_WORKER_SHARED_KEY", "env-key")
	t.Setenv("CALCSIDE_API_ADDR", "http://api:8787")
	t.Setenv("CALCSIDE_INSTANCE_ISOLATION", common.IsolationProcess)
	cfg, err := ParseWorker([]string{"--node-id=w1", "--listen=127.0.0.1:8099", "--max-instances-per-node=12", "--ext-local-roots=/tmp/contrib", "--ext-allow-sources=github.com/acme", "--net-allow-cidrs=198.18.0.0/15", "--max-exec-timeout=2m"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.NodeID != "w1" || cfg.Addr != "127.0.0.1:8099" || cfg.SharedKey != "env-key" || cfg.APIAddr != "http://api:8787" || cfg.MaxInstances != 12 || cfg.InstanceIsolation != common.IsolationProcess {
		t.Fatal("worker configuration not preserved")
	}
	limits := cfg.ExecLimits()
	if limits.MaxExecTimeout != 2*time.Minute || len(limits.NetAllowCIDRs) != 1 || limits.NetAllowCIDRs[0].String() != "198.18.0.0/15" || len(cfg.ExtLocalRoots) != 1 || len(cfg.ExtAllowSources) != 1 {
		t.Fatal("execution settings not preserved")
	}
	cfg, err = ParseWorker([]string{"--shared-key=flag-key", "--api-addr=http://other:8787", "--instance-isolation=inproc"})
	if err != nil || cfg.SharedKey != "flag-key" || cfg.APIAddr != "http://other:8787" || cfg.InstanceIsolation != common.IsolationInproc {
		t.Fatal("flags did not override environment")
	}
}

func TestWorkerRejectsAPIFlags(t *testing.T) {
	for _, flag := range []string{"--dev", "--dsn=api.db", "--admin-username=admin", "--google-client-id=client", "--instance-isolation=invalid", "--net-allow-cidrs=invalid"} {
		if _, err := ParseWorker([]string{flag}); err == nil {
			t.Errorf("unexpectedly accepted %q", flag)
		}
	}
}
