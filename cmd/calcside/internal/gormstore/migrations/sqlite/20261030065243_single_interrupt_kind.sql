DROP INDEX idx_interrupts_status_fire;
ALTER TABLE interrupts DROP COLUMN fire_at;
ALTER TABLE interrupts DROP COLUMN kind;
ALTER TABLE applications DROP COLUMN handlers;
