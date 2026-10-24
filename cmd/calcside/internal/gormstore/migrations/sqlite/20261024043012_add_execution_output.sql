-- Modify "executions" table
ALTER TABLE `executions` ADD COLUMN `via` text NOT NULL DEFAULT '';
ALTER TABLE `executions` ADD COLUMN `output` text NOT NULL DEFAULT '';
ALTER TABLE `executions` ADD COLUMN `output_truncated` boolean NOT NULL DEFAULT false;
ALTER TABLE `executions` ADD COLUMN `error_message` text NOT NULL DEFAULT '';
ALTER TABLE `executions` ADD COLUMN `error_backtrace` text NOT NULL DEFAULT '';
