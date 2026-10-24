package gormstore

import (
	"fmt"
	"os"
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

// The workspaces migration backfills personal workspaces, memberships,
// and workspace_id on every workspace-owned table.
func TestWorkspaceBackfill(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "t.db")
	dir := "migrations/sqlite"
	files, _ := filepath.Glob(dir + "/*.sql")
	// Apply everything before the workspaces migration, seed rows in
	// that schema, then apply the rest.
	before := 0
	for i, f := range files {
		if strings.Contains(f, "add_workspaces") {
			before = i
		}
	}
	storetest.Atlas(t, types.DriverSQLite, dsn, "apply", fmt.Sprintf("%d", before))
	d := openTest(t, types.DriverSQLite, dsn)
	ctx := t.Context()
	uid := "usr_seed001"
	if tx := d.g.WithContext(ctx).Exec(
		`INSERT INTO users (id, email, name, google_sub, is_admin, created_at, last_login_at) VALUES (?,?,?,?,?,?,?)`,
		uid, "seed@x.com", "Seed", "", false, time.Now(), time.Now()); tx.Error != nil {
		t.Fatal(tx.Error)
	}
	if tx := d.g.WithContext(ctx).Exec(
		`INSERT INTO instances (id, user_id, spec, status, node_id, lease_epoch, created_at, last_active_at, expires_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		"ins_seed01", uid, "{}", "stopped", "n1", 1, time.Now(), time.Now(), time.Now()); tx.Error != nil {
		t.Fatal(tx.Error)
	}
	_ = d.Close()
	storetest.Atlas(t, types.DriverSQLite, dsn, "apply")
	d = openTest(t, types.DriverSQLite, dsn)
	var wsID, role string
	if err := d.g.Raw(`SELECT workspace_id, role FROM members WHERE user_id = ?`, uid).Row().Scan(&wsID, &role); err != nil {
		t.Fatal(err)
	}
	if wsID != store.PersonalWorkspaceID(uid) || role != string(store.RoleOwner) {
		t.Fatalf("membership %s %s", wsID, role)
	}
	var inWS string
	if err := d.g.Raw(`SELECT workspace_id FROM instances WHERE id = ?`, "ins_seed01").Row().Scan(&inWS); err != nil {
		t.Fatal(err)
	}
	if inWS != store.PersonalWorkspaceID(uid) {
		t.Fatalf("instance workspace %s", inWS)
	}
	_ = d.Close()
}
