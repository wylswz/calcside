-- Baseline: the schema as of the switch to Atlas, mirroring
-- ../sqlite/20261004092922_baseline.sql.

CREATE TABLE "api_keys" (
  "id" text NOT NULL,
  "user_id" text NULL,
  "name" text NULL,
  "prefix" text NULL,
  "hash" text NULL,
  "created_at" timestamptz NULL,
  "last_used_at" timestamptz NULL,
  "expires_at" timestamptz NULL,
  "revoked_at" timestamptz NULL,
  PRIMARY KEY ("id")
);
CREATE UNIQUE INDEX "idx_api_keys_hash" ON "api_keys" ("hash");

CREATE TABLE "audit_events" (
  "id" text NOT NULL,
  "ts" timestamptz NULL,
  "user_id" text NULL,
  "instance_id" text NULL,
  "exec_id" text NULL,
  "capability" text NULL,
  "op" text NULL,
  "args" text NULL,
  "phase" text NULL,
  "decision" text NULL,
  "reason" text NULL,
  "error" text NULL,
  "duration_ms" bigint NULL,
  PRIMARY KEY ("id")
);
CREATE INDEX "idx_audit_events_instance_id" ON "audit_events" ("instance_id");
CREATE INDEX "idx_audit_user_ts" ON "audit_events" ("ts", "user_id");

CREATE TABLE "executions" (
  "id" text NOT NULL,
  "instance_id" text NULL,
  "user_id" text NULL,
  "code_sha256" text NULL,
  "code_snippet" text NULL,
  "code" text NULL,
  "status" text NULL,
  "error_type" text NULL,
  "duration_ms" bigint NULL,
  "steps" bigint NULL,
  "output_bytes" bigint NULL,
  "created_at" timestamptz NULL,
  PRIMARY KEY ("id")
);
CREATE INDEX "idx_executions_instance_created" ON "executions" ("instance_id", "created_at");

CREATE TABLE "instances" (
  "id" text NOT NULL,
  "user_id" text NULL,
  "spec" bytea NULL,
  "labels" text NULL,
  "status" text NULL,
  "created_at" timestamptz NULL,
  "last_active_at" timestamptz NULL,
  "expires_at" timestamptz NULL,
  "ended_at" timestamptz NULL,
  "node_id" text NULL,
  "lease_epoch" bigint NULL,
  PRIMARY KEY ("id")
);
CREATE INDEX "idx_instances_node" ON "instances" ("node_id");
CREATE INDEX "idx_instances_user_status" ON "instances" ("user_id", "status");

CREATE TABLE "policies" (
  "id" text NOT NULL,
  "user_id" text NULL,
  "name" text NULL,
  "rego" text NULL,
  "created_at" timestamptz NULL,
  "updated_at" timestamptz NULL,
  PRIMARY KEY ("id")
);
CREATE UNIQUE INDEX "idx_policies_user_name" ON "policies" ("user_id", "name");

CREATE TABLE "secrets" (
  "id" text NOT NULL,
  "user_id" text NULL,
  "name" text NULL,
  "ciphertext" bytea NULL,
  "allowed_domains" text NULL,
  "created_at" timestamptz NULL,
  "updated_at" timestamptz NULL,
  PRIMARY KEY ("id")
);
CREATE UNIQUE INDEX "idx_secrets_user_name" ON "secrets" ("user_id", "name");

CREATE TABLE "sessions" (
  "hash" text NOT NULL,
  "user_id" text NULL,
  "created_at" timestamptz NULL,
  "expires_at" timestamptz NULL,
  PRIMARY KEY ("hash")
);

CREATE TABLE "users" (
  "id" text NOT NULL,
  "email" text NULL,
  "name" text NULL,
  "google_sub" text NULL,
  "created_at" timestamptz NULL,
  "last_login_at" timestamptz NULL,
  PRIMARY KEY ("id")
);
CREATE UNIQUE INDEX "idx_users_email" ON "users" ("email");
