package gormstore

import (
	"context"
	"path/filepath"
	"testing"

	"calcside/internal/store"
	"calcside/internal/store/storetest"
	"calcside/internal/types"
)

func TestStore(t *testing.T) {
	storetest.Run(t, func(t *testing.T) store.Store {
		st, err := open(context.Background(), types.DriverSQLite, filepath.Join(t.TempDir(), "t.db"))
		if err != nil {
			t.Fatal(err)
		}
		return st
	})
}
