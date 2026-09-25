package sqlite

import (
	"path/filepath"
	"testing"

	"calcside/internal/store"
	"calcside/internal/store/storetest"
)

func TestStore(t *testing.T) {
	storetest.Run(t, func(t *testing.T) store.Store {
		t.Helper()
		s, err := Open(t.Context(), filepath.Join(t.TempDir(), "test.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		return s
	})
}
