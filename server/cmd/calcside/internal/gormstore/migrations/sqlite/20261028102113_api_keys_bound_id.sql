ALTER TABLE api_keys RENAME COLUMN instance_id TO bound_id;
DROP INDEX idx_api_keys_instance;
CREATE INDEX idx_api_keys_bound ON api_keys (bound_id);
