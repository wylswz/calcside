-- Modify "api_keys" table
ALTER TABLE `api_keys` ADD COLUMN `spec` text NULL;
ALTER TABLE `api_keys` ADD COLUMN `max_instances` integer NOT NULL DEFAULT 0;
-- Modify "instances" table
ALTER TABLE `instances` ADD COLUMN `key_id` text NULL;
CREATE INDEX `idx_instances_key_id` ON `instances` (`key_id`);
