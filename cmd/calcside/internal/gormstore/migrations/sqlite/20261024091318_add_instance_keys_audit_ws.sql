ALTER TABLE api_keys ADD COLUMN instance_id text;
CREATE INDEX idx_api_keys_instance ON api_keys(instance_id);

ALTER TABLE audit_events ADD COLUMN workspace_id text NOT NULL DEFAULT '';
UPDATE audit_events SET workspace_id = (SELECT workspace_id FROM instances WHERE instances.id = audit_events.instance_id) WHERE instance_id != '';
CREATE INDEX idx_audit_ws_ts ON audit_events(workspace_id, ts DESC);
