CREATE TABLE users (
    id            TEXT PRIMARY KEY,
    email         TEXT NOT NULL UNIQUE,
    name          TEXT NOT NULL DEFAULT '',
    google_sub    TEXT NOT NULL DEFAULT '',
    created_at    INTEGER NOT NULL,
    last_login_at INTEGER NOT NULL
);

CREATE TABLE api_keys (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users(id),
    name         TEXT NOT NULL DEFAULT '',
    prefix       TEXT NOT NULL,
    hash         TEXT NOT NULL UNIQUE,
    created_at   INTEGER NOT NULL,
    last_used_at INTEGER,
    expires_at   INTEGER,
    revoked_at   INTEGER
);
CREATE INDEX idx_api_keys_user ON api_keys(user_id);

CREATE TABLE sessions (
    hash       TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id),
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
);
CREATE INDEX idx_sessions_expires ON sessions(expires_at);

CREATE TABLE instances (
    id             TEXT PRIMARY KEY,
    user_id        TEXT NOT NULL REFERENCES users(id),
    spec           BLOB NOT NULL,
    labels         TEXT NOT NULL DEFAULT '{}',
    status         TEXT NOT NULL,
    created_at     INTEGER NOT NULL,
    last_active_at INTEGER NOT NULL,
    expires_at     INTEGER NOT NULL,
    ended_at       INTEGER
);
CREATE INDEX idx_instances_user ON instances(user_id, status);

CREATE TABLE executions (
    id           TEXT PRIMARY KEY,
    instance_id  TEXT NOT NULL REFERENCES instances(id),
    user_id      TEXT NOT NULL,
    code_sha256  TEXT NOT NULL,
    code_snippet TEXT NOT NULL,
    status       TEXT NOT NULL,
    error_type   TEXT NOT NULL DEFAULT '',
    duration_ms  INTEGER NOT NULL,
    steps        INTEGER NOT NULL,
    output_bytes INTEGER NOT NULL,
    created_at   INTEGER NOT NULL
);
CREATE INDEX idx_executions_instance ON executions(instance_id, created_at);

CREATE TABLE audit_events (
    id          TEXT PRIMARY KEY,
    ts          INTEGER NOT NULL,
    user_id     TEXT NOT NULL,
    instance_id TEXT NOT NULL,
    exec_id     TEXT NOT NULL,
    capability  TEXT NOT NULL,
    op          TEXT NOT NULL,
    args        TEXT NOT NULL,
    phase       TEXT NOT NULL DEFAULT '',
    decision    TEXT NOT NULL,
    reason      TEXT NOT NULL DEFAULT '',
    error       TEXT NOT NULL DEFAULT '',
    duration_ms INTEGER NOT NULL
);
CREATE INDEX idx_audit_user_ts ON audit_events(user_id, ts);
CREATE INDEX idx_audit_instance ON audit_events(instance_id, ts);
CREATE INDEX idx_audit_exec ON audit_events(exec_id);

CREATE TABLE policies (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id),
    name       TEXT NOT NULL,
    rego       TEXT NOT NULL,
    enabled    INTEGER NOT NULL DEFAULT 1,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE INDEX idx_policies_user ON policies(user_id);

CREATE TABLE schema_version (
    version INTEGER NOT NULL
);
