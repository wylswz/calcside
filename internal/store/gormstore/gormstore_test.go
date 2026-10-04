package gormstore

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/url"
	"os"
	"testing"

	"calcside/internal/store"
	"calcside/internal/store/storetest"
	"calcside/internal/types"

	_ "github.com/jackc/pgx/v5/stdlib"
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

func newPGDatabase(t *testing.T, admin string) string {
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
	storetest.Atlas(t, types.DriverPostgres, u.String(), "apply")
	return u.String()
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
