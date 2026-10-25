CREATE TABLE llm_providers (
  id text NULL,
  user_id text NULL,
  workspace_id text NULL,
  name text NULL,
  source text NULL,
  sum text NULL,
  config text NULL,
  created_at datetime NULL,
  updated_at datetime NULL,
  PRIMARY KEY (id)
);
CREATE UNIQUE INDEX idx_llm_providers_workspace_name ON llm_providers (workspace_id, name);
