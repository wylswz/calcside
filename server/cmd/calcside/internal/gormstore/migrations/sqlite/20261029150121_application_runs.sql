-- EP-0016 application runs: handler declarations on application
-- drafts, plus durable runs, per-call steps and interrupts. All
-- timestamps are UTC; opaque ciphertext lives in blob columns.
ALTER TABLE applications ADD COLUMN handlers text NULL;

CREATE TABLE runs (
  id text NOT NULL,
  workspace_id text NOT NULL,
  application_id text NOT NULL,
  release_id text NOT NULL,
  user_id text NOT NULL,
  trigger_kind text NOT NULL,
  status text NOT NULL,
  steps integer NOT NULL DEFAULT 0 CHECK (steps >= 0),
  interrupt_id text NOT NULL DEFAULT '',
  error_code text NOT NULL DEFAULT '',
  output blob NULL,
  created_at datetime NOT NULL,
  updated_at datetime NOT NULL,
  ended_at datetime NULL,
  PRIMARY KEY (id)
);
CREATE INDEX idx_runs_ws_app_created ON runs (workspace_id, application_id, created_at);

CREATE TABLE run_steps (
  run_id text NOT NULL,
  seq integer NOT NULL,
  attempt integer NOT NULL,
  entry text NOT NULL,
  interrupt_id text NOT NULL DEFAULT '',
  status text NOT NULL,
  error_code text NOT NULL DEFAULT '',
  duration_ms bigint NOT NULL DEFAULT 0,
  started_at datetime NOT NULL,
  ended_at datetime NULL,
  PRIMARY KEY (run_id, seq, attempt)
);

CREATE TABLE interrupts (
  id text NOT NULL,
  workspace_id text NOT NULL,
  application_id text NOT NULL,
  run_id text NOT NULL,
  step_seq integer NOT NULL,
  kind text NOT NULL DEFAULT '',
  status text NOT NULL,
  title text NOT NULL DEFAULT '',
  assignees text NOT NULL DEFAULT '[]',
  event_schema text NULL,
  handler text NOT NULL DEFAULT '',
  on_expire text NOT NULL DEFAULT '',
  context blob NULL,
  expires_at datetime NULL,
  fire_at datetime NULL,
  event blob NULL,
  resolved_by text NOT NULL DEFAULT '',
  resolved_at datetime NULL,
  key_id text NOT NULL DEFAULT '',
  dispatch_state text NOT NULL DEFAULT '',
  dispatch_after datetime NULL,
  dispatch_claimed_until datetime NULL,
  dispatch_attempts integer NOT NULL DEFAULT 0 CHECK (dispatch_attempts >= 0),
  attempt integer NOT NULL DEFAULT 0 CHECK (attempt >= 0),
  created_at datetime NOT NULL,
  updated_at datetime NOT NULL,
  PRIMARY KEY (id)
);
CREATE INDEX idx_interrupts_ws_status_created ON interrupts (workspace_id, status, created_at);
CREATE INDEX idx_interrupts_run ON interrupts (run_id);
CREATE INDEX idx_interrupts_status_expires ON interrupts (status, expires_at);
CREATE INDEX idx_interrupts_status_fire ON interrupts (status, fire_at);
CREATE INDEX idx_interrupts_dispatch_after ON interrupts (dispatch_state, dispatch_after);
CREATE INDEX idx_interrupts_dispatch_claimed ON interrupts (dispatch_state, dispatch_claimed_until);
