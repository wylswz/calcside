package storetest

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"calcside/internal/types"
)

func Atlas(t *testing.T, driver types.StoreDriver, dsn string, args ...string) []byte {
	t.Helper()
	_, source, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(source), "..", "..", "..")
	env := os.Environ()
	if driver == types.DriverSQLite {
		env = append(env, "TMPDIR="+filepath.Dir(dsn))
		dsn = "sqlite://" + filepath.ToSlash(dsn)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "atlas", append(append([]string{"migrate"}, args...), "--env", string(driver))...)
	cmd.Dir = root
	cmd.Env = append(env, "ATLAS_DB_URL="+dsn, "ATLAS_NO_UPDATE_NOTIFIER=true")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("atlas migrate %v: %v\n%s", args, err, out)
	}
	return out
}

func SQLite(t *testing.T) string {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "t.db")
	Atlas(t, types.DriverSQLite, dsn, "apply")
	return dsn
}
