CREATE TABLE module_debug_settings (
    module_id    text PRIMARY KEY,
    workspace_id text NOT NULL,
    version      bigint NOT NULL DEFAULT 1,
    script       text NOT NULL DEFAULT '',
    spec         text NOT NULL DEFAULT '{}',
    updated_at   datetime NOT NULL
);
