package node

import (
	"os/exec"
	"strings"
	"testing"
)

func TestSharedPackageBoundaries(t *testing.T) {
	cmd := exec.Command("go", "list", "-f", "{{.ImportPath}} {{join .Imports \" \"}}", "calcside/internal/...")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("list shared packages: %v\n%s", err, out)
	}
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		for _, name := range strings.Fields(line) {
			if strings.HasPrefix(name, "calcside/cmd/") {
				t.Errorf("shared package depends on command implementation: %s", line)
			}
		}
	}
	for _, name := range []string{"api", "auth", "audit", "service", "prompt", "store/gormstore", "runtime/remote/apiclient"} {
		if strings.Contains(string(out), "calcside/internal/"+name+" ") {
			t.Errorf("role-specific package remains shared: %s", name)
		}
	}
}

func TestWorkerDependencyBoundary(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", "calcside/cmd/calcside-worker")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("list worker dependencies: %v\n%s", err, out)
	}
	for name := range strings.FieldsSeq(string(out)) {
		for _, prefix := range []string{"calcside/cmd/calcside/", "calcside/internal/api", "calcside/internal/auth", "calcside/internal/service", "calcside/internal/store", "gorm.io/", "github.com/coreos/go-oidc/"} {
			if strings.HasPrefix(name, prefix) {
				t.Errorf("worker depends on API implementation: %s", name)
			}
		}
	}
}

func TestAPIDependencyBoundary(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", "calcside/cmd/calcside")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("list API dependencies: %v\n%s", err, out)
	}
	for name := range strings.FieldsSeq(string(out)) {
		if strings.HasPrefix(name, "calcside/cmd/calcside-worker/") {
			t.Errorf("API depends on worker implementation: %s", name)
		}
	}
}
