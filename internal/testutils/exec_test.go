package testutils

import (
	"testing"

	"github.com/alecthomas/assert/v2"
)

func TestExec(t *testing.T) {
	e, err := NewEngine(t)
	if err != nil {
		t.Fatal(err)
	}
	result, starErr := e.Exec(t, "print(1+1)")
	if starErr != nil {
		t.Fatal(starErr)
	}
	assert.Equal(t, "2\n", result)
}
