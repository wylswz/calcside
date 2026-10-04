package gormstore

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ariga.io/atlas/sql/migrate"
	"ariga.io/atlas/sql/schema"
	"ariga.io/atlas/sql/sqlite"
	"gorm.io/gorm"
	glogger "gorm.io/gorm/logger"

	"calcside/internal/types"

	gormsqlite "github.com/glebarez/sqlite"
)

func openTest(t *testing.T, path string) *sqlDB {
	t.Helper()
	st, err := open(context.Background(), types.DriverSQLite, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st.(*sqlDB)
}

func rawDB(t *testing.T, d *sqlDB) *sql.DB {
	t.Helper()
	db, err := d.g.DB()
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func inspectMain(t *testing.T, db schema.ExecQuerier) (migrate.Driver, *schema.Schema) {
	t.Helper()
	drv, err := sqlite.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	s, err := drv.InspectSchema(context.Background(), "main",
		&schema.InspectOptions{Exclude: []string{revisionsTable}})
	if err != nil {
		t.Fatal(err)
	}
	return drv, s
}

// assertMatchesModels fails when the migrated schema drifts from the
// GORM models, i.e. a model changed without `make migrate-diff`.
func assertMatchesModels(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	ddl, err := ModelDDL(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer want.Close()
	want.SetMaxOpenConns(1)
	if _, err := want.ExecContext(ctx, ddl); err != nil {
		t.Fatal(err)
	}
	_, wantS := inspectMain(t, want)
	drv, gotS := inspectMain(t, db)
	changes, err := drv.SchemaDiff(gotS, wantS)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) == 0 {
		return
	}
	plan, err := drv.PlanChanges(ctx, "drift", changes)
	if err != nil {
		t.Fatal(err)
	}
	var cmds []string
	for _, c := range plan.Changes {
		cmds = append(cmds, c.Cmd)
	}
	t.Fatalf("migrations drift from models; run `make migrate-diff name=<desc>`. Missing:\n%s",
		strings.Join(cmds, ";\n"))
}

func readRevs(t *testing.T, db *sql.DB) []*migrate.Revision {
	t.Helper()
	revs, err := revisions{db}.ReadRevisions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return revs
}

func TestMigrateFresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	d := openTest(t, path)
	db := rawDB(t, d)
	assertMatchesModels(t, db)

	dir, err := migrationDir()
	if err != nil {
		t.Fatal(err)
	}
	files, _ := dir.Files()
	revs := readRevs(t, db)
	if len(revs) != len(files) {
		t.Fatalf("revisions = %d, files = %d", len(revs), len(files))
	}
	for i, r := range revs {
		if r.Version != files[i].Version() || r.Type != migrate.RevisionTypeExecute ||
			r.Applied != r.Total || r.Error != "" || r.ExecutedAt.IsZero() {
			t.Fatalf("bad revision %+v", r)
		}
	}
	var fk int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys after migrate = %d, %v", fk, err)
	}

	// Reopening is a no-op.
	_ = d.Close()
	d2 := openTest(t, path)
	if got := readRevs(t, rawDB(t, d2)); len(got) != len(revs) {
		t.Fatalf("reopen changed revisions: %d -> %d", len(revs), len(got))
	}
}

type legacyPolicyRow struct {
	ID        string `gorm:"primaryKey"`
	UserID    string
	Name      string
	Rego      string
	Enabled   bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (legacyPolicyRow) TableName() string { return "policies" }

func TestMigrateLegacyBaseline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	// A pre-Atlas database: AutoMigrate of the models as of the policies
	// enabled column (no user/name unique index), with existing rows.
	g, err := gorm.Open(gormsqlite.Open(path), &gorm.Config{Logger: glogger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	old := []any{&legacyPolicyRow{}}
	for _, m := range models {
		if _, ok := m.(*policyRow); !ok {
			old = append(old, m)
		}
	}
	if err := g.AutoMigrate(old...); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"INSERT INTO users (id, email) VALUES ('usr_1', 'a@x.com')",
		"INSERT INTO policies (id, user_id, name, rego, enabled) VALUES ('pol_1', 'usr_1', 'p', 'package p', true)",
	} {
		if err := g.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	if db, _ := g.DB(); db != nil {
		_ = db.Close()
	}

	d := openTest(t, path)
	db := rawDB(t, d)
	assertMatchesModels(t, db)
	revs := readRevs(t, db)
	if len(revs) == 0 || revs[0].Type != migrate.RevisionTypeBaseline {
		t.Fatalf("want baseline first revision, got %+v", revs)
	}
	if _, err := d.GetUser(context.Background(), "usr_1"); err != nil {
		t.Fatalf("legacy row lost: %v", err)
	}
	if p, err := d.GetPolicy(context.Background(), "pol_1"); err != nil || p.Rego != "package p" {
		t.Fatalf("legacy policy: %v %+v", err, p)
	}
}

func TestRevisionsRoundTrip(t *testing.T) {
	d := openTest(t, filepath.Join(t.TempDir(), "t.db"))
	rrw := revisions{rawDB(t, d)}
	ctx := context.Background()
	in := &migrate.Revision{
		Version: "99990101000000", Description: "x", Type: migrate.RevisionTypeExecute,
		Applied: 1, Total: 2, ExecutedAt: time.Now().UTC().Truncate(time.Microsecond),
		ExecutionTime: 3 * time.Millisecond, Error: "boom", ErrorStmt: "SELECT 1",
		Hash: "h1:abc", PartialHashes: []string{"h1:a"}, OperatorVersion: "calcside",
	}
	if err := rrw.WriteRevision(ctx, in); err != nil {
		t.Fatal(err)
	}
	in.Applied, in.Error, in.ErrorStmt, in.PartialHashes = 2, "", "", nil
	if err := rrw.WriteRevision(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, err := rrw.ReadRevision(ctx, in.Version)
	if err != nil {
		t.Fatal(err)
	}
	if got.Applied != 2 || got.Error != "" || got.PartialHashes != nil ||
		!got.ExecutedAt.Equal(in.ExecutedAt) || got.ExecutionTime != in.ExecutionTime {
		t.Fatalf("round trip: %+v", got)
	}
	if err := rrw.DeleteRevision(ctx, in.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := rrw.ReadRevision(ctx, in.Version); err != migrate.ErrRevisionNotExist {
		t.Fatalf("after delete: %v", err)
	}
}
