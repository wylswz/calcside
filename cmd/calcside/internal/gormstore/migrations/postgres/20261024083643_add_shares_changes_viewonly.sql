-- Create "shares" table
CREATE TABLE "shares" (
  "instance_id" text NOT NULL,
  "user_id" text NOT NULL,
  "role" text NOT NULL,
  "created_by" text NOT NULL,
  "created_at" timestamptz NULL,
  PRIMARY KEY ("instance_id", "user_id")
);
-- Create index "idx_shares_user" to table: "shares"
CREATE INDEX "idx_shares_user" ON "shares" ("user_id");
-- Create "workspace_changes" table
CREATE TABLE "workspace_changes" (
  "id" text NOT NULL,
  "workspace_id" text NOT NULL,
  "by_user_id" text NOT NULL,
  "change" text NOT NULL,
  "user_id" text NULL,
  "instance_id" text NULL,
  "role" text NULL,
  "created_at" timestamptz NULL,
  PRIMARY KEY ("id")
);
-- Create index "idx_changes_workspace" to table: "workspace_changes"
CREATE INDEX "idx_changes_workspace" ON "workspace_changes" ("workspace_id");
-- Modify "api_keys" table: view_only key ceiling
ALTER TABLE "api_keys" ADD COLUMN "view_only" boolean NOT NULL DEFAULT false;
