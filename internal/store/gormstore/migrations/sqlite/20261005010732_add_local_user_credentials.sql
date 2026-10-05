ALTER TABLE "users" ADD COLUMN "username" text NULL;
ALTER TABLE "users" ADD COLUMN "password_hash" text NOT NULL DEFAULT '';
CREATE UNIQUE INDEX "idx_users_username" ON "users" ("username");
