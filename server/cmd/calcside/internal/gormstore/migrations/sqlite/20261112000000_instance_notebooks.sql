-- EP-0022 instance notebooks: durable named sections, notebook revision
-- CAS, incarnation/restoration tracking, drift and the per-instance
-- operation lease.

ALTER TABLE instances ADD COLUMN incarnation integer NOT NULL DEFAULT 1;
ALTER TABLE instances ADD COLUMN restored_at datetime NULL;
ALTER TABLE instances ADD COLUMN notebook_rev integer NOT NULL DEFAULT 0;
ALTER TABLE instances ADD COLUMN run_seq integer NOT NULL DEFAULT 0;
ALTER TABLE instances ADD COLUMN drift integer NOT NULL DEFAULT 0;
ALTER TABLE instances ADD COLUMN op_token text NOT NULL DEFAULT '';
ALTER TABLE instances ADD COLUMN op_expires_at datetime NULL;
CREATE INDEX idx_instances_op_token ON instances (op_token);

ALTER TABLE executions ADD COLUMN section_name text NOT NULL DEFAULT '';
ALTER TABLE executions ADD COLUMN incarnation integer NOT NULL DEFAULT 0;

CREATE TABLE notebook_sections (
  instance_id text NOT NULL,
  name text NOT NULL,
  position integer NOT NULL DEFAULT 0,
  description text NOT NULL DEFAULT '',
  code text NOT NULL DEFAULT '',
  code_sha256 text NOT NULL DEFAULT '',
  rerun text NOT NULL DEFAULT 'safe',
  defines text NOT NULL DEFAULT '[]',
  uses text NOT NULL DEFAULT '[]',
  effects text NOT NULL DEFAULT '[]',
  depends_on text NOT NULL DEFAULT '[]',
  last_exec_id text NOT NULL DEFAULT '',
  last_incarnation integer NOT NULL DEFAULT 0,
  last_run_seq integer NOT NULL DEFAULT 0,
  last_status text NOT NULL DEFAULT '',
  last_code_sha256 text NOT NULL DEFAULT '',
  last_run_at datetime NULL,
  succeeded_before boolean NOT NULL DEFAULT false,
  created_by text NOT NULL DEFAULT '',
  updated_by text NOT NULL DEFAULT '',
  edited_at datetime NULL,
  created_at datetime NULL,
  updated_at datetime NULL,
  PRIMARY KEY (instance_id, name)
);
CREATE INDEX idx_notebook_sections_instance ON notebook_sections (instance_id, position);
