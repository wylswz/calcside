DROP INDEX idx_interrupts_status_fire;
ALTER TABLE interrupts DROP COLUMN fire_at,
                    DROP COLUMN kind;
ALTER TABLE applications DROP COLUMN handlers;
