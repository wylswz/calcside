-- Consolidated pre-release baseline. Apply to an empty database.
-- SQLite time columns must use datetime, not PostgreSQL's timestamptz.
CREATE TABLE api_keys (
  id text NULL,
  user_id text NULL,
  name text NULL,
  prefix text NULL,
  hash text NULL,
  created_at datetime NULL,
  last_used_at datetime NULL,
  expires_at datetime NULL,
  revoked_at datetime NULL,
  spec text NULL,
  max_instances integer NOT NULL DEFAULT 0,
  workspace_id text NULL,
  view_only boolean NOT NULL DEFAULT false,
  instance_id text NULL,
  PRIMARY KEY (id)
);
CREATE UNIQUE INDEX idx_api_keys_hash ON api_keys (hash);
CREATE INDEX idx_api_keys_instance ON api_keys (instance_id);

CREATE TABLE audit_events (
  id text NULL,
  ts datetime NULL,
  user_id text NULL,
  instance_id text NULL,
  exec_id text NULL,
  capability text NULL,
  op text NULL,
  args text NULL,
  phase text NULL,
  decision text NULL,
  reason text NULL,
  error text NULL,
  duration_ms integer NULL,
  workspace_id text NOT NULL DEFAULT '',
  PRIMARY KEY (id)
);
CREATE INDEX idx_audit_user_ts ON audit_events (ts, user_id);
CREATE INDEX idx_audit_ws_ts ON audit_events (workspace_id, ts DESC);
CREATE INDEX idx_audit_instance_ts ON audit_events (instance_id, ts DESC);

CREATE TABLE executions (
  id text NULL,
  instance_id text NULL,
  user_id text NULL,
  code_sha256 text NULL,
  code_snippet text NULL,
  code text NULL,
  status text NULL,
  error_type text NULL,
  duration_ms integer NULL,
  steps integer NULL,
  output_bytes integer NULL,
  created_at datetime NULL,
  via text NOT NULL DEFAULT '',
  output text NOT NULL DEFAULT '',
  output_truncated boolean NOT NULL DEFAULT false,
  error_message text NOT NULL DEFAULT '',
  error_backtrace text NOT NULL DEFAULT '',
  PRIMARY KEY (id)
);
CREATE INDEX idx_executions_instance_created ON executions (instance_id, created_at);

CREATE TABLE instances (
  id text NULL,
  user_id text NULL,
  spec blob NULL,
  labels text NULL,
  status text NULL,
  created_at datetime NULL,
  last_active_at datetime NULL,
  expires_at datetime NULL,
  ended_at datetime NULL,
  node_id text NULL,
  lease_epoch integer NULL,
  key_id text NULL,
  workspace_id text NULL,
  PRIMARY KEY (id)
);
CREATE INDEX idx_instances_node ON instances (node_id);
CREATE INDEX idx_instances_user_status ON instances (user_id, status);
CREATE INDEX idx_instances_key_id ON instances (key_id);
CREATE INDEX idx_instances_workspace ON instances (workspace_id);

CREATE TABLE policies (
  id text NULL,
  user_id text NULL,
  name text NULL,
  rego text NULL,
  created_at datetime NULL,
  updated_at datetime NULL,
  workspace_id text NULL,
  PRIMARY KEY (id)
);
CREATE UNIQUE INDEX idx_policies_workspace_name ON policies (workspace_id, name);

CREATE TABLE secrets (
  id text NULL,
  user_id text NULL,
  name text NULL,
  ciphertext blob NULL,
  allowed_domains text NULL,
  created_at datetime NULL,
  updated_at datetime NULL,
  workspace_id text NULL,
  PRIMARY KEY (id)
);
CREATE UNIQUE INDEX idx_secrets_workspace_name ON secrets (workspace_id, name);

CREATE TABLE sessions (
  hash text NULL,
  user_id text NULL,
  created_at datetime NULL,
  expires_at datetime NULL,
  PRIMARY KEY (hash)
);

CREATE TABLE users (
  id text NULL,
  email text NULL,
  name text NULL,
  google_sub text NULL,
  created_at datetime NULL,
  last_login_at datetime NULL,
  is_admin boolean NOT NULL DEFAULT false,
  username text NULL,
  password_hash text NOT NULL DEFAULT '',
  PRIMARY KEY (id)
);
CREATE UNIQUE INDEX idx_users_email ON users (email);
CREATE UNIQUE INDEX idx_users_username ON users (username);

CREATE TABLE workspaces (
  id text NOT NULL,
  name text NOT NULL,
  personal boolean NOT NULL,
  created_at datetime NULL,
  PRIMARY KEY (id)
);

CREATE TABLE members (
  workspace_id text NOT NULL,
  user_id text NOT NULL,
  role text NOT NULL,
  PRIMARY KEY (workspace_id, user_id)
);
CREATE INDEX idx_members_user ON members (user_id);

CREATE TABLE shares (
  instance_id text NOT NULL,
  user_id text NOT NULL,
  role text NOT NULL,
  created_by text NOT NULL,
  created_at datetime NULL,
  PRIMARY KEY (instance_id, user_id)
);
CREATE INDEX idx_shares_user ON shares (user_id);

CREATE TABLE workspace_changes (
  id text NOT NULL,
  workspace_id text NOT NULL,
  by_user_id text NOT NULL,
  change text NOT NULL,
  user_id text NULL,
  instance_id text NULL,
  role text NULL,
  created_at datetime NULL,
  PRIMARY KEY (id)
);
CREATE INDEX idx_changes_workspace ON workspace_changes (workspace_id);

CREATE TABLE llm_providers (
  id text NULL,
  user_id text NULL,
  workspace_id text NULL,
  name text NULL,
  source text NULL,
  sum text NULL,
  models text NULL,
  config text NULL,
  created_at datetime NULL,
  updated_at datetime NULL,
  PRIMARY KEY (id)
);
CREATE UNIQUE INDEX idx_llm_providers_workspace_name ON llm_providers (workspace_id, name);
