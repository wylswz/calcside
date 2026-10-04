env "sqlite" {
  url = getenv("ATLAS_DB_URL")
  migration {
    dir = "file://internal/store/gormstore/migrations/sqlite"
  }
}

env "postgres" {
  url = getenv("ATLAS_DB_URL")
  migration {
    dir = "file://internal/store/gormstore/migrations/postgres"
  }
}
