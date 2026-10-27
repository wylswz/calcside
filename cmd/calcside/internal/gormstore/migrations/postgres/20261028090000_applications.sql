-- EP-0014 application drafts: durable named JSON-function drafts with a
-- CAS draft version and a publish pointer to the persistent instance.
CREATE TABLE applications (
  id text NOT NULL,
  workspace_id text NOT NULL,
  user_id text NOT NULL,
  name text NOT NULL,
  code text NOT NULL,
  spec text NOT NULL,
  input_schema text NULL,
  output_schema text NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version >= 1),
  published_version bigint NOT NULL DEFAULT 0 CHECK (published_version >= 0),
  persistent_instance_id text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  deleted_at timestamptz NULL,
  PRIMARY KEY (id)
);
CREATE UNIQUE INDEX idx_applications_ws_id ON applications (workspace_id, id);
CREATE INDEX idx_applications_user ON applications (workspace_id, user_id, id);
CREATE INDEX idx_applications_deleted ON applications (deleted_at);
