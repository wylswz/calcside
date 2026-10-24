package gormstore

import (
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

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
		if len(files) != 1 || files[0] != "20261024133846_baseline.sql" {
			t.Fatalf("%s migrations = %v, want exactly 20261024133846_baseline.sql", driver, files)
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

// The consolidated baseline creates every workspace artifact end to end on a
// fresh SQLite database.
func TestBaselineWorkspaceSQLite(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "baseline.db")
	storetest.Atlas(t, types.DriverSQLite, dsn, "apply")
	d := openTest(t, types.DriverSQLite, dsn)
	ctx := t.Context()

	u, err := d.UpsertUserByEmail(ctx, "base@example.com", "base", "")
	if err != nil {
		t.Fatal(err)
	}
	w, err := d.GetWorkspace(ctx, store.PersonalWorkspaceID(u.ID))
	if err != nil {
		t.Fatalf("personal workspace: %v", err)
	}
	if !w.Personal || w.CreatedAt.IsZero() || w.CreatedAt.Location() != time.UTC {
		t.Fatalf("personal workspace row %+v", w)
	}
	ms, err := d.ListMemberships(ctx, u.ID)
	if err != nil || len(ms) != 1 || ms[0].Role != types.RoleOwner {
		t.Fatalf("memberships %v %+v", err, ms)
	}

	team := &store.Workspace{Name: "team"}
	if err := d.CreateWorkspace(ctx, team); err != nil {
		t.Fatal(err)
	}
	if err := d.AddMember(ctx, &store.Member{WorkspaceID: team.ID, UserID: u.ID, Role: types.RoleEditor}); err != nil {
		t.Fatal(err)
	}
	ms, err = d.ListMemberships(ctx, u.ID)
	if err != nil || len(ms) != 2 {
		t.Fatalf("memberships after team add %v %+v", err, ms)
	}

	// workspaces.created_at must declare a plain datetime, not the legacy
	// TIMESTAMPTZ the old migration used.
	rows, err := d.g.DB()
	if err != nil {
		t.Fatal(err)
	}
	r, err := rows.QueryContext(ctx, `SELECT type FROM pragma_table_info('workspaces') WHERE name = 'created_at'`)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var typ string
	if !r.Next() || r.Scan(&typ) != nil || !strings.EqualFold(typ, "datetime") {
		t.Fatalf("created_at type %q, want datetime", typ)
	}
}
