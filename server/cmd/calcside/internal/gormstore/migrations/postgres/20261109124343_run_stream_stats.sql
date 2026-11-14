ALTER TABLE runs ADD COLUMN streaming boolean NOT NULL DEFAULT false;
ALTER TABLE runs ADD COLUMN stream_items bigint NOT NULL DEFAULT 0;
ALTER TABLE runs ADD COLUMN stream_bytes bigint NOT NULL DEFAULT 0;
ALTER TABLE run_steps ADD COLUMN streaming boolean NOT NULL DEFAULT false;
ALTER TABLE run_steps ADD COLUMN stream_items bigint NOT NULL DEFAULT 0;
ALTER TABLE run_steps ADD COLUMN stream_bytes bigint NOT NULL DEFAULT 0;
