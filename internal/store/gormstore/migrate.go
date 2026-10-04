package gormstore

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"strings"
	"time"

	"ariga.io/atlas/sql/migrate"
	"ariga.io/atlas/sql/schema"
	"ariga.io/atlas/sql/sqlite"
	"gorm.io/gorm"
	glogger "gorm.io/gorm/logger"

	gormsqlite "github.com/glebarez/sqlite"
)

// Schema changes are versioned Atlas migrations generated from the row
// models: edit the structs, then `make migrate-diff name=<desc>` (see
// atlas.hcl). Never edit an applied migration file; atlas.sum guards it.
//
//go:embed migrations/sqlite
var migrationsFS embed.FS

// models is the desired schema; ModelDDL renders it for atlas.
var models = []any{
	&userRow{}, &keyRow{}, &sessionRow{}, &instanceRow{},
	&execRow{}, &auditRow{}, &policyRow{}, &secretRow{},
}

// revisionsTable matches the Atlas CLI's history table, so `atlas
// migrate status/apply --url sqlite://<db>` work against the same DB.
const revisionsTable = "atlas_schema_revisions"

// ModelDDL returns the CREATE statements GORM generates for the row
// models on an empty SQLite database. It is the desired state for
// `atlas migrate diff` (via cmd schemadump) and the drift test.
func ModelDDL(ctx context.Context) (string, error) {
	g, err := gorm.Open(gormsqlite.Open(":memory:"), &gorm.Config{Logger: glogger.Discard})
	if err != nil {
		return "", err
	}
	db, err := g.DB()
	if err != nil {
		return "", err
	}
	defer db.Close()
	// Every :memory: connection is its own database.
	db.SetMaxOpenConns(1)
	g = g.WithContext(ctx)
	if err := g.AutoMigrate(models...); err != nil {
		return "", err
	}
	var stmts []string
	if err := g.Raw(`SELECT sql FROM sqlite_master
		WHERE sql IS NOT NULL AND name NOT LIKE 'sqlite_%'
		ORDER BY type = 'index', name`).Scan(&stmts).Error; err != nil {
		return "", err
	}
	return strings.Join(stmts, ";\n") + ";\n", nil
}

func migrationDir() (*migrate.MemDir, error) {
	sub, err := fs.Sub(migrationsFS, "migrations/sqlite")
	if err != nil {
		return nil, err
	}
	entries, err := fs.ReadDir(sub, ".")
	if err != nil {
		return nil, err
	}
	dir := &migrate.MemDir{}
	for _, e := range entries {
		b, err := fs.ReadFile(sub, e.Name())
		if err != nil {
			return nil, err
		}
		if err := dir.WriteFile(e.Name(), b); err != nil {
			return nil, err
		}
	}
	return dir, nil
}

// migrateSchema applies pending migrations in one BEGIN IMMEDIATE
// transaction, so concurrent openers of the same file serialize and a
// failure leaves the schema untouched. Databases created before Atlas
// (AutoMigrate, no history table) are brought up to the baseline
// schema and recorded as baselined at the first migration.
func migrateSchema(ctx context.Context, g *gorm.DB) error {
	dir, err := migrationDir()
	if err != nil {
		return err
	}
	files, err := dir.Files()
	if err != nil || len(files) == 0 {
		return fmt.Errorf("no migration files: %w", err)
	}
	opts := []migrate.ExecutorOption{migrate.WithLogger(migrateLog{})}
	legacy, err := isLegacy(ctx, g)
	if err != nil {
		return err
	}
	if legacy {
		if err := upgradeLegacy(g.WithContext(ctx)); err != nil {
			return fmt.Errorf("legacy upgrade: %w", err)
		}
		opts = append(opts, migrate.WithBaselineVersion(files[0].Version()))
		slog.Info("store: baselining pre-atlas database", "version", files[0].Version())
	}

	db, err := g.DB()
	if err != nil {
		return err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	// Table rebuilds need foreign keys off, and the pragma is a no-op
	// inside a transaction (same as atlas' own SQLite tx mode).
	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys = off"); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	if err := applyPending(ctx, conn, dir, opts); err != nil {
		_, _ = conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
		return err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	// The conn returns to the pool; it must not keep foreign keys off.
	_, err = conn.ExecContext(ctx, "PRAGMA foreign_keys = on")
	return err
}

func applyPending(ctx context.Context, conn *sql.Conn, dir migrate.Dir, opts []migrate.ExecutorOption) error {
	rrw := revisions{conn}
	if err := rrw.init(ctx); err != nil {
		return err
	}
	drv, err := sqlite.Open(conn)
	if err != nil {
		return err
	}
	ex, err := migrate.NewExecutor(drv, dir, rrw, opts...)
	if err != nil {
		return err
	}
	if err := ex.ExecuteN(ctx, 0); err != nil && !errors.Is(err, migrate.ErrNoPendingFiles) {
		return err
	}
	rows, err := conn.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		var tbl string
		if err := rows.Scan(&tbl, new(any), new(any), new(any)); err != nil {
			return err
		}
		return fmt.Errorf("foreign key violation in table %q after migration", tbl)
	}
	return rows.Err()
}

// isLegacy reports a database that has tables but no Atlas history.
func isLegacy(ctx context.Context, g *gorm.DB) (bool, error) {
	var names []string
	err := g.WithContext(ctx).Raw(`SELECT name FROM sqlite_master
		WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`).Scan(&names).Error
	if err != nil {
		return false, err
	}
	for _, n := range names {
		if n == revisionsTable {
			return false, nil
		}
	}
	return len(names) > 0, nil
}

// upgradeLegacy reproduces the pre-Atlas AutoMigrate path so every
// legacy database matches the baseline migration before it is
// recorded as applied.
func upgradeLegacy(g *gorm.DB) error {
	// policies.enabled predates per-instance policy selection. Drop it
	// before AutoMigrate: SQLite drops columns by rebuilding the table,
	// which would discard indexes AutoMigrate just created.
	if g.Migrator().HasColumn(&policyRow{}, "enabled") {
		if err := g.Migrator().DropColumn(&policyRow{}, "enabled"); err != nil {
			return err
		}
	}
	return g.AutoMigrate(models...)
}

type migrateLog struct{}

func (migrateLog) Log(e migrate.LogEntry) {
	switch e := e.(type) {
	case migrate.LogFile:
		slog.Info("store: applying migration", "file", e.File.Name(), "skip", e.Skip)
	case migrate.LogError:
		slog.Error("store: migration failed", "sql", e.SQL, "err", e.Error)
	}
}

// revisions implements migrate.RevisionReadWriter on the Atlas CLI's
// history table layout.
type revisions struct{ db schema.ExecQuerier }

var _ migrate.RevisionReadWriter = revisions{}

const revisionCols = "`version`, `description`, `type`, `applied`, `total`, `executed_at`, " +
	"`execution_time`, `error`, `error_stmt`, `hash`, `partial_hashes`, `operator_version`"

func (revisions) Ident() *migrate.TableIdent { return &migrate.TableIdent{Name: revisionsTable} }

func (r revisions) init(ctx context.Context) error {
	_, err := r.db.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS `"+revisionsTable+"` ("+
		"`version` text NOT NULL, `description` text NOT NULL, `type` integer NOT NULL DEFAULT 2, "+
		"`applied` integer NOT NULL DEFAULT 0, `total` integer NOT NULL DEFAULT 0, "+
		"`executed_at` datetime NOT NULL, `execution_time` integer NOT NULL, "+
		"`error` text NULL, `error_stmt` text NULL, `hash` text NOT NULL, "+
		"`partial_hashes` json NULL, `operator_version` text NOT NULL, PRIMARY KEY (`version`))")
	return err
}

func (r revisions) ReadRevisions(ctx context.Context) ([]*migrate.Revision, error) {
	return r.query(ctx, "ORDER BY `version`")
}

func (r revisions) ReadRevision(ctx context.Context, v string) (*migrate.Revision, error) {
	revs, err := r.query(ctx, "WHERE `version` = ?", v)
	if err != nil {
		return nil, err
	}
	if len(revs) == 0 {
		return nil, migrate.ErrRevisionNotExist
	}
	return revs[0], nil
}

func (r revisions) WriteRevision(ctx context.Context, rev *migrate.Revision) error {
	var partial sql.NullString
	if len(rev.PartialHashes) > 0 {
		b, err := json.Marshal(rev.PartialHashes)
		if err != nil {
			return err
		}
		partial = sql.NullString{String: string(b), Valid: true}
	}
	_, err := r.db.ExecContext(ctx, "INSERT INTO `"+revisionsTable+"` ("+revisionCols+
		") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (`version`) DO UPDATE SET "+
		"`type` = excluded.`type`, `applied` = excluded.`applied`, `total` = excluded.`total`, "+
		"`execution_time` = excluded.`execution_time`, `error` = excluded.`error`, "+
		"`error_stmt` = excluded.`error_stmt`, `hash` = excluded.`hash`, "+
		"`partial_hashes` = excluded.`partial_hashes`, `operator_version` = excluded.`operator_version`",
		rev.Version, rev.Description, uint(rev.Type), rev.Applied, rev.Total, rev.ExecutedAt.UTC(),
		int64(rev.ExecutionTime), nullStr(rev.Error), nullStr(rev.ErrorStmt), rev.Hash,
		partial, rev.OperatorVersion)
	return err
}

func (r revisions) DeleteRevision(ctx context.Context, v string) error {
	_, err := r.db.ExecContext(ctx, "DELETE FROM `"+revisionsTable+"` WHERE `version` = ?", v)
	return err
}

func (r revisions) query(ctx context.Context, where string, args ...any) ([]*migrate.Revision, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT "+revisionCols+" FROM `"+revisionsTable+"` "+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*migrate.Revision
	for rows.Next() {
		var (
			rev                      migrate.Revision
			typ                      uint
			execNs                   int64
			errMsg, errStmt, partial sql.NullString
		)
		if err := rows.Scan(&rev.Version, &rev.Description, &typ, &rev.Applied, &rev.Total,
			&rev.ExecutedAt, &execNs, &errMsg, &errStmt, &rev.Hash, &partial,
			&rev.OperatorVersion); err != nil {
			return nil, err
		}
		rev.Type, rev.ExecutionTime = migrate.RevisionType(typ), time.Duration(execNs)
		rev.Error, rev.ErrorStmt = errMsg.String, errStmt.String
		if partial.Valid && partial.String != "" {
			if err := json.Unmarshal([]byte(partial.String), &rev.PartialHashes); err != nil {
				return nil, err
			}
		}
		out = append(out, &rev)
	}
	return out, rows.Err()
}

func nullStr(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }
