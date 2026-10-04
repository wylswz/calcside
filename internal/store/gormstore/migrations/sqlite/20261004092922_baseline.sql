-- Create "api_keys" table
CREATE TABLE `api_keys` (
  `id` text NULL,
  `user_id` text NULL,
  `name` text NULL,
  `prefix` text NULL,
  `hash` text NULL,
  `created_at` datetime NULL,
  `last_used_at` datetime NULL,
  `expires_at` datetime NULL,
  `revoked_at` datetime NULL,
  PRIMARY KEY (`id`)
);
-- Create index "idx_api_keys_hash" to table: "api_keys"
CREATE UNIQUE INDEX `idx_api_keys_hash` ON `api_keys` (`hash`);
-- Create "audit_events" table
CREATE TABLE `audit_events` (
  `id` text NULL,
  `ts` datetime NULL,
  `user_id` text NULL,
  `instance_id` text NULL,
  `exec_id` text NULL,
  `capability` text NULL,
  `op` text NULL,
  `args` text NULL,
  `phase` text NULL,
  `decision` text NULL,
  `reason` text NULL,
  `error` text NULL,
  `duration_ms` integer NULL,
  PRIMARY KEY (`id`)
);
-- Create index "idx_audit_events_instance_id" to table: "audit_events"
CREATE INDEX `idx_audit_events_instance_id` ON `audit_events` (`instance_id`);
-- Create index "idx_audit_user_ts" to table: "audit_events"
CREATE INDEX `idx_audit_user_ts` ON `audit_events` (`ts`, `user_id`);
-- Create "executions" table
CREATE TABLE `executions` (
  `id` text NULL,
  `instance_id` text NULL,
  `user_id` text NULL,
  `code_sha256` text NULL,
  `code_snippet` text NULL,
  `code` text NULL,
  `status` text NULL,
  `error_type` text NULL,
  `duration_ms` integer NULL,
  `steps` integer NULL,
  `output_bytes` integer NULL,
  `created_at` datetime NULL,
  PRIMARY KEY (`id`)
);
-- Create index "idx_executions_instance_created" to table: "executions"
CREATE INDEX `idx_executions_instance_created` ON `executions` (`instance_id`, `created_at`);
-- Create "instances" table
CREATE TABLE `instances` (
  `id` text NULL,
  `user_id` text NULL,
  `spec` blob NULL,
  `labels` text NULL,
  `status` text NULL,
  `created_at` datetime NULL,
  `last_active_at` datetime NULL,
  `expires_at` datetime NULL,
  `ended_at` datetime NULL,
  `node_id` text NULL,
  `lease_epoch` integer NULL,
  PRIMARY KEY (`id`)
);
-- Create index "idx_instances_node" to table: "instances"
CREATE INDEX `idx_instances_node` ON `instances` (`node_id`);
-- Create index "idx_instances_user_status" to table: "instances"
CREATE INDEX `idx_instances_user_status` ON `instances` (`user_id`, `status`);
-- Create "policies" table
CREATE TABLE `policies` (
  `id` text NULL,
  `user_id` text NULL,
  `name` text NULL,
  `rego` text NULL,
  `created_at` datetime NULL,
  `updated_at` datetime NULL,
  PRIMARY KEY (`id`)
);
-- Create index "idx_policies_user_name" to table: "policies"
CREATE UNIQUE INDEX `idx_policies_user_name` ON `policies` (`user_id`, `name`);
-- Create "secrets" table
CREATE TABLE `secrets` (
  `id` text NULL,
  `user_id` text NULL,
  `name` text NULL,
  `ciphertext` blob NULL,
  `allowed_domains` text NULL,
  `created_at` datetime NULL,
  `updated_at` datetime NULL,
  PRIMARY KEY (`id`)
);
-- Create index "idx_secrets_user_name" to table: "secrets"
CREATE UNIQUE INDEX `idx_secrets_user_name` ON `secrets` (`user_id`, `name`);
-- Create "sessions" table
CREATE TABLE `sessions` (
  `hash` text NULL,
  `user_id` text NULL,
  `created_at` datetime NULL,
  `expires_at` datetime NULL,
  PRIMARY KEY (`hash`)
);
-- Create "users" table
CREATE TABLE `users` (
  `id` text NULL,
  `email` text NULL,
  `name` text NULL,
  `google_sub` text NULL,
  `created_at` datetime NULL,
  `last_login_at` datetime NULL,
  PRIMARY KEY (`id`)
);
-- Create index "idx_users_email" to table: "users"
CREATE UNIQUE INDEX `idx_users_email` ON `users` (`email`);
