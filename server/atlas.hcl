env "sqlite" {
  url = getenv("ATLAS_DB_URL")
  migration {
    dir = "file://cmd/calcside/internal/gormstore/migrations/sqlite"
  }
}

env "postgres" {
  url = getenv("ATLAS_DB_URL")
  migration {
    dir = "file://cmd/calcside/internal/gormstore/migrations/postgres"
  }
}
