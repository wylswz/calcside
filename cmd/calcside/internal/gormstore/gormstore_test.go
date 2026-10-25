package gormstore

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"calcside/internal/store"
	"calcside/internal/store/storetest"
	"calcside/internal/types"
)

// pgEnv names an admin URL of a PostgreSQL server for tests (e.g.
// postgres://postgres:postgres@127.0.0.1:5432/postgres?sslmode=disable);
// each test gets its own throwaway database. `make test-postgres`
// starts one in Docker. Unset: postgres cases are skipped.
const pgEnv = "CALCSIDE_TEST_POSTGRES_DSN"

type backend struct {
	driver types.StoreDriver
	newDSN func(t *testing.T) string
}

func backends(t *testing.T) []backend {
	bs := []backend{{types.DriverSQLite, func(t *testing.T) string {
		return storetest.SQLite(t)
	}}}
	if admin := os.Getenv(pgEnv); admin != "" {
		bs = append(bs, backend{types.DriverPostgres, func(t *testing.T) string { return newPGDatabase(t, admin) }})
	} else {
		t.Logf("%s unset: skipping postgres", pgEnv)
	}
	return bs
}

// newEmptyPGDatabase creates a throwaway database and returns its DSN;
// no migrations are applied.
func newEmptyPGDatabase(t *testing.T, admin string) string {
	t.Helper()
	db, err := sql.Open("pgx", admin)
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	name := "calcside_test_" + hex.EncodeToString(b)
	if _, err := db.Exec("CREATE DATABASE " + name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		defer db.Close()
		if _, err := db.Exec("DROP DATABASE " + name + " WITH (FORCE)"); err != nil {
			t.Errorf("drop %s: %v", name, err)
		}
	})
	u, err := url.Parse(admin)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	q := u.Query()
	q.Set("search_path", "public")
	u.RawQuery = q.Encode()
	return u.String()
}

func newPGDatabase(t *testing.T, admin string) string {
	t.Helper()
	dsn := newEmptyPGDatabase(t, admin)
	storetest.Atlas(t, types.DriverPostgres, dsn, "apply")
	return dsn
}

// TestBaselineLLMProviders applies ONLY the consolidated baseline migration
// (from a scratch migration dir, not the repo chain) to an empty database and
// proves the llm_providers table carries its final schema.
func TestBaselineLLMProviders(t *testing.T) {
	type be struct {
		driver types.StoreDriver
		newDSN func(t *testing.T) string
	}
	cases := []be{{types.DriverSQLite, func(t *testing.T) string {
		return filepath.Join(t.TempDir(), "baseline.db")
	}}}
	if admin := os.Getenv(pgEnv); admin != "" {
		cases = append(cases, be{types.DriverPostgres,
			func(t *testing.T) string { return newEmptyPGDatabase(t, admin) }})
	} else {
		t.Logf("%s unset: skipping postgres", pgEnv)
	}
	for _, c := range cases {
		t.Run(string(c.driver), func(t *testing.T) {
			dir := t.TempDir()
			src := filepath.Join("migrations", string(c.driver), "20261024133846_baseline.sql")
			raw, err := os.ReadFile(src)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "20261024133846_baseline.sql"), raw, 0o644); err != nil {
				t.Fatal(err)
			}
			hash := exec.Command("atlas", "migrate", "hash", "--dir", "file://"+filepath.ToSlash(dir))
			hash.Env = append(os.Environ(), "ATLAS_NO_UPDATE_NOTIFIER=true")
			if out, err := hash.CombinedOutput(); err != nil {
				t.Fatalf("atlas migrate hash: %v\n%s", err, out)
			}
			dsn := c.newDSN(t)
			storetest.Atlas(t, c.driver, dsn, "apply", "--dir", "file://"+filepath.ToSlash(dir))

			st, err := open(context.Background(), c.driver, dsn)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			db := st.(*sqlDB).g
			types_, err := db.Migrator().ColumnTypes(&llmProviderRow{})
			if err != nil {
				t.Fatal(err)
			}
			names := make([]string, 0, len(types_))
			for _, ct := range types_ {
				names = append(names, ct.Name())
			}
			sort.Strings(names)
			want := []string{"config", "created_at", "id", "models", "name",
				"source", "sum", "updated_at", "user_id", "workspace_id"}
			if !reflect.DeepEqual(names, want) {
				t.Fatalf("llm_providers columns: got %v want %v (no legacy type)", names, want)
			}

			ctx := context.Background()
			u, err := st.UpsertUserByEmail(ctx, "baseline@x.com", "", "")
			if err != nil {
				t.Fatal(err)
			}
			p := &store.LLMProvider{
				UserID: u.ID, WorkspaceID: store.PersonalWorkspaceID(u.ID),
				Name: "base", Source: "contrib/llm-openai", Sum: "h1:test",
				Models: []string{"model-a", "model-b"},
				Config: json.RawMessage(`{"key":"v"}`),
			}
			if err := st.CreateLLMProvider(ctx, p); err != nil {
				t.Fatal(err)
			}
			got, err := st.GetLLMProvider(ctx, p.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Source != "contrib/llm-openai" || got.Sum != "h1:test" ||
				!reflect.DeepEqual(got.Models, []string{"model-a", "model-b"}) ||
				string(got.Config) != `{"key":"v"}` {
				t.Fatalf("GetLLMProvider round-trip: %+v", got)
			}
			for name, ts := range map[string]time.Time{"created_at": got.CreatedAt, "updated_at": got.UpdatedAt} {
				if ts.IsZero() || ts.Location() != time.UTC {
					t.Fatalf("%s not UTC/nonzero: %v", name, ts)
				}
			}
			dup := &store.LLMProvider{UserID: u.ID, WorkspaceID: p.WorkspaceID,
				Name: "base", Source: "contrib/llm-openai", Config: json.RawMessage(`{}`)}
			if err := st.CreateLLMProvider(ctx, dup); !errors.Is(err, store.ErrConflict) {
				t.Fatalf("duplicate workspace/name: want ErrConflict, got %v", err)
			}
		})
	}
}

func TestStore(t *testing.T) {
	for _, b := range backends(t) {
		t.Run(string(b.driver), func(t *testing.T) {
			storetest.Run(t, func(t *testing.T) store.Store {
				st, err := open(context.Background(), b.driver, b.newDSN(t))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = st.Close() })
				return st
			})
		})
	}
}
