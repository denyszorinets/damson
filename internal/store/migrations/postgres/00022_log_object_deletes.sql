-- +goose Up
-- Deferred deletes of log objects held outside the database (ARCHITECTURE 10,
-- ADR-0067). A purge or an execution claim writes a prefix here in the same
-- transaction that drops the chunk rows; a sweeper on each controller deletes
-- the objects and then the row.
CREATE TABLE log_object_deletes (
    id         TEXT        PRIMARY KEY,
    prefix     TEXT        NOT NULL,
    not_before TIMESTAMPTZ NOT NULL,
    tries      INTEGER     NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX log_object_deletes_due ON log_object_deletes (not_before);

-- +goose Down
DROP TABLE log_object_deletes;
