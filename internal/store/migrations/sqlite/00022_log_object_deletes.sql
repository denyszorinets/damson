-- +goose Up
-- Deferred deletes of log objects held outside the database (ARCHITECTURE 10,
-- ADR-0067). See the PostgreSQL migration.
CREATE TABLE log_object_deletes (
    id         TEXT    PRIMARY KEY,
    prefix     TEXT    NOT NULL,
    not_before TEXT    NOT NULL,
    tries      INTEGER NOT NULL DEFAULT 0,
    created_at TEXT    NOT NULL
);
CREATE INDEX log_object_deletes_due ON log_object_deletes (not_before);

-- +goose Down
DROP TABLE log_object_deletes;
