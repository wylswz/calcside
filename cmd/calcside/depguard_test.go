package main

import (
	"go/build"
	"testing"
)

// The API tier must never execute code — its non-test sources cannot
// import node/subproc/instance internals (tests may use them as
// worker fixtures).
func TestAPINoExecutionImports(t *testing.T) {
	forbidden := map[string]bool{
		"calcside/internal/node":         true,
		"calcside/internal/node/subproc": true,
		"calcside/internal/instance":     true,
	}
	pkg, err := build.Default.ImportDir(".", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, imp := range pkg.Imports {
		if forbidden[imp] {
			t.Fatalf("API imports execution package %s", imp)
		}
	}
}
