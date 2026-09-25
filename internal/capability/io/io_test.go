package io

import (
	"fmt"
	"strings"
	"testing"

	"calcside/internal/capability"
)

func TestPrompt(t *testing.T) {
	cfg, err := factory{}.Validate(nil, capability.ServerLimits{})
	if err != nil {
		t.Fatal(err)
	}
	p := factory{}.Prompt(cfg)
	for _, want := range []string{"`io`", "io.println", fmt.Sprintf("%d bytes", defaultMaxOutput), "`print(...)`"} {
		if !strings.Contains(p, want) {
			t.Fatalf("missing %q in:\n%s", want, p)
		}
	}
}
