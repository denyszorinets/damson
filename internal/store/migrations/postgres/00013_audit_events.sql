-- +goose Up
-- Insert-only audit trail (ARCHITECTURE 12.4). A row is written in the same
-- transaction as the change it records, never holds secret values, and has no
-- foreign keys so it outlives what it describes. project_path is the project
-- scope of the target ('' when none), kept for per-subtree visibility (M8).
CREATE TABLE audit_events (
    id           TEXT        PRIMARY KEY,
    at           TIMESTAMPTZ NOT NULL,
    actor        TEXT        NOT NULL,
    action       TEXT        NOT NULL,
    target_type  TEXT        NOT NULL,
    target       TEXT        NOT NULL,
    project_path TEXT        NOT NULL DEFAULT '',
    details      TEXT        NOT NULL,
    request_id   TEXT        NOT NULL DEFAULT ''
);

CREATE INDEX audit_events_at ON audit_events (at, id);
CREATE INDEX audit_events_target ON audit_events (target_type, target, at, id);

-- +goose Down
DROP TABLE audit_events;
