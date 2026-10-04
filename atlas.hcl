# Atlas config for the store schema. The GORM row models in
# internal/store/gormstore are the source of truth:
#   make migrate-diff name=<desc>   # generate a migration from model changes
#   make migrate-hash               # re-sum after hand-editing a pending file
# Migrations are embedded and applied by the server on open.

data "external_schema" "gorm" {
  program = ["go", "run", "./internal/store/gormstore/schemadump"]
}

env "sqlite" {
  src = data.external_schema.gorm.url
  dev = "sqlite://dev?mode=memory"
  migration {
    dir = "file://internal/store/gormstore/migrations/sqlite"
  }
  format {
    migrate {
      diff = "{{ sql . \"  \" }}"
    }
  }
}
