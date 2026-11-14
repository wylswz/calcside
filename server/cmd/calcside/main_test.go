package main

import (
	"strings"
	"testing"

	"calcside/cmd/calcside/internal/config"
)

func TestParseWorkers(t *testing.T) {
	refs, err := config.ParseWorkers(" w1 = 10.0.0.1:8080 ,w2=10.0.0.2:8080")
	if err != nil || len(refs) != 2 || refs[0].NodeID != "w1" || refs[0].Addr != "10.0.0.1:8080" {
		t.Fatalf("parse: %v %+v", err, refs)
	}
	if _, err := config.ParseWorkers("w1=a:1,w1=b:2"); err == nil {
		t.Fatal("duplicate id accepted")
	}
	if _, err := config.ParseWorkers("bad"); err == nil {
		t.Fatal("bad entry accepted")
	}
	if _, err := config.ParseWorkers(strings.Repeat("x", 200) + "=a:1"); err == nil {
		t.Fatal("oversize id accepted")
	}
}
