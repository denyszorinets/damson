-- +goose Up
-- Cron schedules (ARCHITECTURE 12.3). options is JSON text. next_fire_at is the
-- next due time (NULL while disabled). The scheduler duty fires a schedule with
-- a compare-and-swap on next_fire_at in the same transaction that creates the
-- run, so each fire time starts exactly one run (ADR-0058).
CREATE TABLE schedules (
    id           TEXT        PRIMARY KEY,
    job_id       TEXT        NOT NULL REFERENCES jobs (id) ON DELETE CASCADE,
    name         TEXT        NOT NULL,
    cron         TEXT        NOT NULL,
    timezone     TEXT        NOT NULL,
    options      TEXT        NOT NULL DEFAULT '{}',
    enabled      INTEGER     NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    misfire      TEXT        NOT NULL DEFAULT 'run_once',
    next_fire_at TEXT,
    last_fire_at TEXT,
    last_run_id  TEXT        NOT NULL DEFAULT '',
    last_error   TEXT        NOT NULL DEFAULT '',
    version      INTEGER     NOT NULL DEFAULT 1,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL,
    UNIQUE (job_id, name)
);

CREATE INDEX schedules_due ON schedules (next_fire_at);

-- +goose Down
DROP TABLE schedules;
