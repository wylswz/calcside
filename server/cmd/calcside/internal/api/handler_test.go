package api

import "testing"

// TestHandlerPanicsOnMissingDeps checks the fail-fast wiring check:
// any nil service dep panics at Handler() time, not mid-request.
func TestHandlerPanicsOnMissingDeps(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("Handler(Deps{}) did not panic")
		}
	}()
	Handler(Deps{})
}
