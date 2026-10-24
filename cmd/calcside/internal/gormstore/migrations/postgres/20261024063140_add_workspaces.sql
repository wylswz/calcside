-- Create "workspaces" table
CREATE TABLE "workspaces" (
  "id" text NOT NULL,
  "name" text NOT NULL,
  "personal" boolean NOT NULL,
  "created_at" timestamptz NOT NULL,
  PRIMARY KEY ("id")
);
-- Create "members" table
CREATE TABLE "members" (
  "workspace_id" text NOT NULL,
  "user_id" text NOT NULL,
  "role" text NOT NULL,
  PRIMARY KEY ("workspace_id", "user_id")
);
-- Create index "idx_members_user" to table: "members"
CREATE INDEX "idx_members_user" ON "members" ("user_id");
-- Modify "instances" table
ALTER TABLE "instances" ADD COLUMN "workspace_id" text NULL;
CREATE INDEX "idx_instances_workspace" ON "instances" ("workspace_id");
-- Modify "policies" table
ALTER TABLE "policies" ADD COLUMN "workspace_id" text NULL;
DROP INDEX "idx_policies_user_name";
CREATE UNIQUE INDEX "idx_policies_workspace_name" ON "policies" ("workspace_id", "name");
-- Modify "secrets" table
ALTER TABLE "secrets" ADD COLUMN "workspace_id" text NULL;
DROP INDEX "idx_secrets_user_name";
CREATE UNIQUE INDEX "idx_secrets_workspace_name" ON "secrets" ("workspace_id", "name");
-- Modify "api_keys" table
ALTER TABLE "api_keys" ADD COLUMN "workspace_id" text NULL;
CREATE INDEX "idx_api_keys_workspace" ON "api_keys" ("workspace_id");
-- workspace_id stays NULLable here (Atlas/SQLite cannot add a NOT NULL
-- column in place); the store layer rejects empty WorkspaceID on insert.
-- Backfill: one personal workspace per user + owner membership
INSERT INTO "workspaces" ("id", "name", "personal", "created_at")
SELECT 'ws_' || substr("id", 5), COALESCE(NULLIF("name", ''), "email"), true, "created_at" FROM "users";
INSERT INTO "members" ("workspace_id", "user_id", "role")
SELECT 'ws_' || substr("id", 5), "id", 'owner' FROM "users";
UPDATE "instances" SET "workspace_id" = 'ws_' || substr("user_id", 5);
UPDATE "policies" SET "workspace_id" = 'ws_' || substr("user_id", 5);
UPDATE "secrets" SET "workspace_id" = 'ws_' || substr("user_id", 5);
UPDATE "api_keys" SET "workspace_id" = 'ws_' || substr("user_id", 5);
