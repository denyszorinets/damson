-- +goose Up
-- Run model (M2): projects, jobs, immutable job versions, runs, step
-- instances, executions and the log manifest (ARCHITECTURE sections 4, 7.3,
-- 8.3 and 10). Ids are UUIDv7 generated in Go; JSON is handled in Go.
-- Timestamps are fixed-width UTC text (see 00002_agents.sql); JSON columns are text.
CREATE TABLE projects (
    id         TEXT        PRIMARY KEY,
    name       TEXT        NOT NULL UNIQUE,
    created_at TEXT NOT NULL
);
-- The single seeded project of M2; the project tree arrives with access control.
INSERT INTO projects (id, name, created_at)
VALUES ('00000000-0000-7000-8000-000000000001', 'default', '1970-01-01T00:00:00.000000Z');

-- current_version is the number of the newest job_versions row; it is the CAS
-- guard for publishing a new version.
CREATE TABLE jobs (
    id              TEXT        PRIMARY KEY,
    project_id      TEXT        NOT NULL REFERENCES projects (id),
    name            TEXT        NOT NULL,
    managed_by      TEXT        NOT NULL DEFAULT 'ui',
    current_version INTEGER     NOT NULL,
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    UNIQUE (project_id, name)
);

-- spec is TEXT, not TEXT, so the stored bytes match the checksum exactly.
CREATE TABLE job_versions (
    id         TEXT        PRIMARY KEY,
    job_id     TEXT        NOT NULL REFERENCES jobs (id),
    version    INTEGER     NOT NULL,
    spec       TEXT        NOT NULL,
    checksum   TEXT        NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE (job_id, version)
);

-- version is the optimistic-concurrency counter bumped by every advance.
CREATE TABLE runs (
    id                  TEXT        PRIMARY KEY,
    job_id              TEXT        NOT NULL REFERENCES jobs (id),
    job_version_id      TEXT        NOT NULL REFERENCES job_versions (id),
    trigger             TEXT       NOT NULL DEFAULT '{}',
    options             TEXT       NOT NULL DEFAULT '{}',
    status              TEXT        NOT NULL,
    version             INTEGER      NOT NULL DEFAULT 1,
    cancel_requested_at TEXT,
    concurrency_key     TEXT        NOT NULL DEFAULT '',
    created_at          TEXT NOT NULL,
    started_at          TEXT,
    finished_at         TEXT
);
CREATE INDEX runs_job_id_idx ON runs (job_id, id);
CREATE INDEX runs_concurrency_key_idx ON runs (concurrency_key);
CREATE INDEX runs_status_idx ON runs (status);

-- path is stable and unique within a run (for example main/step-id). input
-- holds resolved plugin input with secret refs only, never secret values.
CREATE TABLE step_instances (
    id          TEXT        PRIMARY KEY,
    run_id      TEXT        NOT NULL REFERENCES runs (id),
    path        TEXT        NOT NULL,
    kind        TEXT        NOT NULL DEFAULT 'step',
    plugin      TEXT        NOT NULL DEFAULT '',
    input       TEXT       NOT NULL DEFAULT '{}',
    status      TEXT        NOT NULL,
    outputs     TEXT       NOT NULL DEFAULT '{}',
    error       TEXT        NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL,
    started_at  TEXT,
    finished_at TEXT,
    UNIQUE (run_id, path)
);

-- attempt_id is the fencing token: unique, and rotated at every dispatch claim.
-- agent_id and controller_id have no foreign keys (rows outlive agents and
-- controller registrations).
CREATE TABLE executions (
    id               TEXT        PRIMARY KEY,
    run_id           TEXT        NOT NULL REFERENCES runs (id),
    step_instance_id TEXT        NOT NULL REFERENCES step_instances (id),
    attempt_no       INTEGER     NOT NULL,
    attempt_id       TEXT        NOT NULL UNIQUE,
    state            TEXT        NOT NULL,
    scheduled_at     TEXT NOT NULL,
    priority         INTEGER     NOT NULL DEFAULT 0,
    pool             TEXT        NOT NULL,
    agent_id         TEXT,
    controller_id    TEXT,
    lease_expires_at TEXT,
    outputs          TEXT       NOT NULL DEFAULT '{}',
    error            TEXT        NOT NULL DEFAULT '',
    created_at       TEXT NOT NULL,
    started_at       TEXT,
    finished_at      TEXT,
    UNIQUE (step_instance_id, attempt_no)
);
CREATE INDEX executions_run_id_idx ON executions (run_id);
CREATE INDEX executions_claim_idx ON executions (pool, priority, scheduled_at) WHERE state = 'queued';
CREATE INDEX executions_lease_idx ON executions (lease_expires_at) WHERE state IN ('dispatched', 'running');
CREATE INDEX executions_controller_idx ON executions (controller_id) WHERE state IN ('dispatched', 'running');

-- Manifest of stored log chunks; backend names the LogStore that holds the object.
CREATE TABLE log_chunks (
    id           TEXT        PRIMARY KEY,
    execution_id TEXT        NOT NULL REFERENCES executions (id),
    seq_from     INTEGER      NOT NULL,
    seq_to       INTEGER      NOT NULL,
    backend      TEXT        NOT NULL,
    object_key   TEXT        NOT NULL,
    bytes        INTEGER      NOT NULL,
    ts_first     TEXT NOT NULL,
    ts_last      TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    UNIQUE (execution_id, seq_from)
);

-- Object bytes of the default "db" LogStore.
CREATE TABLE log_chunks_data (
    object_key TEXT        PRIMARY KEY,
    data       BLOB       NOT NULL,
    created_at TEXT NOT NULL
);

-- +goose Down
DROP TABLE log_chunks_data;
DROP TABLE log_chunks;
DROP TABLE executions;
DROP TABLE step_instances;
DROP TABLE runs;
DROP TABLE job_versions;
DROP TABLE jobs;
DROP TABLE projects;
