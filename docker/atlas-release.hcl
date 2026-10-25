env "sqlite" {
  url = getenv("ATLAS_DB_URL")
  migration {
    dir = "file:///migrations/sqlite"
  }
}

env "postgres" {
  url = getenv("ATLAS_DB_URL")
  migration {
    dir = "file:///migrations/postgres"
  }
}
