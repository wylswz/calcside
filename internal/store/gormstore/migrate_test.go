package gormstore

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"calcside/internal/store"
	"calcside/internal/store/storetest"
	"calcside/internal/types"
)

func openTest(t *testing.T, driver types.StoreDriver, dsn string) *sqlDB {
	t.Helper()
	st, err := open(t.Context(), driver, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st.(*sqlDB)
}

// Every schema change ships for every dialect under the same version.
func TestMigrationDirsInLockstep(t *testing.T) {
	var want []string
	for _, driver := range types.AllStoreDrivers() {
		files, err := filepath.Glob(filepath.Join("migrations", string(driver), "*.sql"))
		if err != nil || len(files) == 0 {
			t.Fatalf("migration files for %s: %v", driver, err)
		}
		for i := range files {
			files[i] = filepath.Base(files[i])
		}
		if want == nil {
			want = files
		} else if !reflect.DeepEqual(files, want) {
			t.Fatalf("%s migrations = %v, want %v", driver, files, want)
		}
		cmd := exec.CommandContext(t.Context(), "atlas", "migrate", "validate", "--dir", "file://migrations/"+string(driver))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s checksums: %v\n%s", driver, err, out)
		}
	}
}

func TestMigrateFresh(t *testing.T) {
	for _, b := range backends(t) {
		t.Run(string(b.driver), func(t *testing.T) {
			dsn := b.newDSN(t)
			d := openTest(t, b.driver, dsn)
			u, err := d.UpsertUserByEmail(t.Context(), "migration@example.com", "migration", "")
			if err != nil {
				t.Fatal(err)
			}
			_ = d.Close()
			storetest.Atlas(t, b.driver, dsn, "apply")
			d = openTest(t, b.driver, dsn)
			if got, err := d.GetUser(t.Context(), u.ID); err != nil || got.Email != u.Email {
				t.Fatalf("reopen after migration: %v, %+v", err, got)
			}
			if out := storetest.Atlas(t, b.driver, dsn, "status"); !strings.Contains(string(out), "Pending Files:   0") {
				t.Fatalf("unexpected status: %s", out)
			}
		})
	}
}

func TestMigrateLegacyBaseline(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "legacy.db")
	d := openTest(t, types.DriverSQLite, dsn)
	baseline, err := os.ReadFile("migrations/sqlite/20261004092922_baseline.sql")
	if err != nil {
		t.Fatal(err)
	}
	db, err := d.g.DB()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), string(baseline)); err != nil {
		t.Fatal(err)
	}
	u := store.User{ID: "usr_legacy", Email: "legacy@example.com", Name: "legacy"}
	if _, err := db.ExecContext(t.Context(), "INSERT INTO users (id, email, name) VALUES (?, ?, ?)", u.ID, u.Email, u.Name); err != nil {
		t.Fatal(err)
	}
	_ = d.Close()
	storetest.Atlas(t, types.DriverSQLite, dsn, "apply", "--baseline", "20261004092922")
	d = openTest(t, types.DriverSQLite, dsn)
	if got, err := d.GetUser(t.Context(), u.ID); err != nil || got.Email != u.Email || got.IsAdmin {
		t.Fatalf("existing user changed identity or became admin during migration: %v, %+v", err, got)
	}
}

func TestOpenDoesNotMigrate(t *testing.T) {
	d := openTest(t, types.DriverSQLite, filepath.Join(t.TempDir(), "empty.db"))
	var n int
	if err := d.g.WithContext(t.Context()).Raw("SELECT count(*) FROM sqlite_master WHERE type = 'table'").Scan(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("opening a store created %d tables; migration must be explicit", n)
	}
}
