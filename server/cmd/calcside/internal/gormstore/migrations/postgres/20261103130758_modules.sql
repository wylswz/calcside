CREATE TABLE modules (
  id text PRIMARY KEY,
  workspace_id text NOT NULL,
  user_id text NOT NULL,
  name text NOT NULL,
  description text NOT NULL,
  files text NOT NULL,
  draft_version bigint NOT NULL,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  UNIQUE (workspace_id, name)
);

CREATE TABLE module_versions (
  module_id text NOT NULL,
  version bigint NOT NULL,
  workspace_id text NOT NULL,
  sum text NOT NULL,
  files text NOT NULL,
  published_by text NOT NULL,
  note text NOT NULL,
  base_version bigint,
  created_at timestamptz NOT NULL,
  PRIMARY KEY (module_id, version)
);
