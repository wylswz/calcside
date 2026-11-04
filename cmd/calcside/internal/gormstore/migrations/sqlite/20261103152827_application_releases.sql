CREATE TABLE application_releases (
    application_id text NOT NULL,
    workspace_id   text NOT NULL,
    seq            bigint NOT NULL,
    release_id     text NOT NULL,
    draft_version  bigint NOT NULL,
    published_by   text NOT NULL,
    note           text NOT NULL DEFAULT '',
    published_at   datetime NOT NULL,
    UNIQUE (application_id, seq)
);
CREATE INDEX idx_application_releases_ws_app ON application_releases (workspace_id, application_id);
