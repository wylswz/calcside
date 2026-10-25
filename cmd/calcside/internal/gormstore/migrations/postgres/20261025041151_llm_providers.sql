CREATE TABLE llm_providers (
  id text NOT NULL,
  user_id text NULL,
  workspace_id text NULL,
  name text NULL,
  source text NULL,
  sum text NULL,
  config text NULL,
  created_at timestamptz NULL,
  updated_at timestamptz NULL,
  PRIMARY KEY (id)
);
CREATE UNIQUE INDEX idx_llm_providers_workspace_name ON llm_providers (workspace_id, name);
