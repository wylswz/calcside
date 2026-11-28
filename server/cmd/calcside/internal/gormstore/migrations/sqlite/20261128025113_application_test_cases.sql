CREATE TABLE application_test_cases (
    id                   text PRIMARY KEY,
    application_id       text NOT NULL,
    workspace_id         text NOT NULL,
    name                 text NOT NULL,
    description          text NOT NULL DEFAULT '',
    input_json           text NOT NULL DEFAULT '{}',
    interrupts_json      text NOT NULL DEFAULT '[]',
    expect_status        text NOT NULL DEFAULT 'succeeded',
    expected_output_json text NOT NULL DEFAULT '',
    output_match_mode    text NOT NULL DEFAULT 'exact',
    expected_error_code  text NOT NULL DEFAULT '',
    created_at           datetime NOT NULL,
    updated_at           datetime NOT NULL
);
CREATE INDEX idx_application_test_cases_ws_app ON application_test_cases (workspace_id, application_id, created_at);

CREATE TABLE application_test_runs (
    application_id text NOT NULL,
    workspace_id   text NOT NULL,
    candidate      text NOT NULL,
    status         text NOT NULL,
    total          integer NOT NULL DEFAULT 0,
    passed         integer NOT NULL DEFAULT 0,
    failed         integer NOT NULL DEFAULT 0,
    duration_ms    bigint NOT NULL DEFAULT 0,
    results_json   text NOT NULL DEFAULT '[]',
    run_by         text NOT NULL,
    run_at         datetime NOT NULL,
    PRIMARY KEY (application_id, candidate)
);
CREATE INDEX idx_application_test_runs_ws_app ON application_test_runs (workspace_id, application_id);
