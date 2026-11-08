-- +goose Up
-- CEL access rules (ARCHITECTURE 12.4, ADR-0064). A project carries a JSON list
-- of {role, rule}; a job's own rules live in job_access, keyed by the job, with
-- a version for compare-and-swap (no row means no rules and version 0). The
-- rules are validated and compiled in Go before they are written.
ALTER TABLE projects ADD COLUMN access_rules TEXT NOT NULL DEFAULT '[]';

CREATE TABLE job_access (
    job_id     TEXT        PRIMARY KEY REFERENCES jobs (id) ON DELETE CASCADE,
    rules      TEXT        NOT NULL,
    version    INTEGER     NOT NULL,
    updated_at TEXT NOT NULL
);

-- +goose Down
DROP TABLE job_access;
ALTER TABLE projects DROP COLUMN access_rules;
